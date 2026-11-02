package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/launcher"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/topics"
)

// Every plugins.* section of the tree is an instance, of the type its shim's Config returns.
func TestTreeSettingsListsEverySectionOnce(t *testing.T) {
	off := false
	tree := config.DefaultTree()
	tree.Plugins.Flux.Enabled = &off
	ics := treeSettings{func() config.Tree { return tree }}.Instances()

	manifests := map[plugin.ID]plugin.Manifest{}
	for _, m := range builtinManifests() {
		manifests[m.ID] = m
	}
	if len(ics) != len(manifests) {
		t.Fatalf("%d instances for %d manifests", len(ics), len(manifests))
	}
	for _, ic := range ics {
		m, ok := manifests[ic.Name]
		if !ok {
			t.Errorf("section %s has no plugin", ic.Name)
			continue
		}
		if ic.Type != ic.Name {
			t.Errorf("%s: type %q, want the section name", ic.Name, ic.Type)
		}
		if reflect.TypeOf(ic.Config) != reflect.TypeOf(m.Config()) {
			t.Errorf("%s: config is %T, the plugin takes %T", ic.Name, ic.Config, m.Config())
		}
		if (ic.Name == "flux") != (ic.Enabled != nil && !*ic.Enabled) {
			t.Errorf("%s: enabled = %v", ic.Name, ic.Enabled)
		}
	}
}

func TestBuiltinManifestsAreTheElevenInDisplayOrder(t *testing.T) {
	var ids []string
	for _, m := range builtinManifests() {
		ids = append(ids, string(m.ID))
	}
	want := []string{"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "kubernetes", "flux", "grafana", "headlamp", "launcher"}
	if !slices.Equal(ids, want) {
		t.Fatalf("manifests = %v, want %v", ids, want)
	}
}

// testCluster is an empty cluster whose informers sync at once.
func testCluster() *kube.Cluster {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	return kube.NewFromClients(kubefake.NewClientset(), dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
}

// loadFramework loads the configuration of the environment the way run does and builds the
// framework around it. The reloader polls every millisecond.
func loadFramework(t *testing.T) (*framework, *config.Resolved) {
	t.Helper()
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, false), res
}

// The framework as run starts it: Supervisor.Run returns before Manager.Run starts, so the
// Manager's first publishes find every Host running and none is dropped. Everything the Manager
// publishes arrives, and the shims are live, and restart-only.
func TestFrameworkStartOrderPublishesFirstPoll(t *testing.T) {
	startEnv(t, t.TempDir())
	fw, res := loadFramework(t)
	cfg := res.Config
	mgr := topics.New(topics.Deps{
		Cfg: cfg, Cluster: testCluster(), Hosts: fw.sup, Contributors: fw.sup.Contributors,
		Services: fw.sup.Service,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, mgr.Plugins(), func() { mgr.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	want := []string{"core.plugins", "core.overview", "cluster", "storage", "apps", "flux"}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _, cancelSub := fw.hub.Subscribe("", nil)
		cancelSub()
		var got []string
		for _, ev := range snap {
			got = append(got, ev.Topic)
		}
		if slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("topics = %v, want %v (in this order)", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if fw.sup.Live("plugins.kubernetes") {
		t.Error("a legacy plugin's section applies live, but only a restart can change the Manager")
	}
	for _, in := range fw.sup.Infos() {
		if in.State == model.PluginDisabled || in.State == model.PluginError || in.State == model.PluginBlocked {
			t.Errorf("%s is %s %q with nothing configured", in.ID, in.State, in.Message)
		}
	}
}

// The reloader and the supervisor as run joins them: a changed plugins.<id> section of a plugin
// that can be reconfigured reaches it in-process (Subscribe -> Reload) and is not reported as
// needing a restart (the Live hook), while a section nothing applies live still is.
func TestFrameworkAppliesLiveSectionsAndReportsTheRest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	files := func(headlamp, flux string) string {
		return "plugins:\n  headlamp:\n    url: " + headlamp + "\n  flux:\n    suspendReasonAnnotation: " + flux + "\n"
	}
	writeTestFile(t, cfgPath, files("https://hl-a.example.com", "example.com/a"))
	startEnv(t, dir, "DASH_CONFIG="+cfgPath)
	fw, _ := loadFramework(t)

	// headlamp is a plugin that follows its section; flux has no plugin in this framework (it is
	// the Manager's, which only a restart changes).
	headlamp := plugintest.New("headlamp", plugintest.Config(func() any { return &pluginconf.Headlamp{} }))
	ctx, cancel := context.WithCancel(context.Background())
	if err := fw.start(ctx, []plugin.Factory{headlamp.Factory()}, func() {}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done; fw.sup.Shutdown(context.Background()) }()
	waitFor(t, func() bool { return headlamp.Starts() == 1 })

	writeTestFile(t, cfgPath, files("https://hl-b.example.com", "example.com/b"))
	waitFor(t, func() bool { return headlamp.Starts() == 2 })
	if got := headlamp.Config().(*pluginconf.Headlamp).URL; got != "https://hl-b.example.com" {
		t.Errorf("the plugin restarted with %q, want the new section", got)
	}
	waitFor(t, func() bool { return len(fw.reloader.RestartRequired()) > 0 })
	if got := fw.reloader.RestartRequired(); !slices.Equal(got, []string{"plugins.flux.suspendReasonAnnotation"}) {
		t.Errorf("restart required = %v, want only the section no plugin applies", got)
	}
}

// Demo mode answers /api/v1/series through the prometheus plugin's demo twin and the series the
// hardware twin registers: the handler asks the running plugin for its Querier and for the
// series, and the demo source serves neither.
func TestDemoSeriesComeFromThePrometheusTwin(t *testing.T) {
	startEnv(t, t.TempDir(), "DASH_DEMO=true")
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	cfg := res.Config
	fw := newFramework(res, store, config.ReloadOptions{}, nil, nil, true)
	src := demo.New(cfg)
	src.HasReal = hasRealPlugin
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, registry(src.Plugins(fw.sup)), func() { src.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	srv := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Data: src, Health: fw.sup})
	r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/series?name=host.cpu&range=1h", nil)
	rec := httptest.NewRecorder()
	demo.Wrap(srv.Handler()).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("series = %d %s", rec.Code, rec.Body)
	}
	var got model.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Series) == 0 || len(got.Series[0].Points) == 0 {
		t.Fatalf("series body = %s (%v), want generated lines", rec.Body, err)
	}
}

// Demo mode publishes the launcher's links through the launcher plugin's demo twin, after every
// other topic (the launcher comes last in the display order), and the shim of the Manager
// registers nothing under that name.
func TestDemoPublishesTheLauncherLinksLast(t *testing.T) {
	startEnv(t, t.TempDir(), "DASH_DEMO=true")
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	fw := newFramework(res, store, config.ReloadOptions{}, nil, nil, true)
	src := demo.New(res.Config)
	src.HasReal = hasRealPlugin
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, registry(src.Plugins(fw.sup)), func() { src.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	want := []string{"core.plugins", "core.overview", "alertmanager.alerts", "gatus.status", "proxmox.vms", "proxmox.node", "proxmox.storage", "hardware.hosts", "hardware.machines", "cluster", "storage", "apps", "flux", "launcher.links"}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _, cancelSub := fw.hub.Subscribe("", nil)
		cancelSub()
		var got []string
		for _, ev := range snap {
			got = append(got, ev.Topic)
		}
		if slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("topics = %v, want %v (in this order)", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	env, ok := fw.hub.Latest("", model.TopicLauncherLinks)
	if !ok {
		t.Fatal("launcher.links has no snapshot")
	}
	var e model.Envelope
	var data model.LauncherData
	if err := json.Unmarshal(env.Data, &e); err != nil || json.Unmarshal(e.Data, &data) != nil || len(data.Links) != len(world.Launcher().Links) {
		t.Fatalf("launcher.links = %s (%v), want the demo links", env.Data, err)
	}
	// The demo's host, published by the hardware twin, carries the fake host's model.
	hostEnv, ok := fw.hub.Latest("", model.TopicHost)
	if !ok {
		t.Fatal("host has no snapshot")
	}
	var he model.Envelope
	var host model.HostData
	if err := json.Unmarshal(hostEnv.Data, &he); err != nil || json.Unmarshal(he.Data, &host) != nil || host.Model != world.HostModel {
		t.Fatalf("host = %s, want model %q", hostEnv.Data, world.HostModel)
	}
}

// The launcher file is read by the launcher plugin at Start: an unreadable file makes its row
// error, as any plugin that fails to start, and no longer stops dash from starting (it used to
// abort loadConfig). A readable file is published as launcher.links.
func TestUnreadableLauncherFileMakesTheRowErrorAndNotTheStartup(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "launcher.yaml")
	writeTestFile(t, good, "- {name: Router, href: 'http://192.0.2.1', icon: unifi}\n")
	row := func(fw *framework) (model.PluginInfo, bool) {
		for _, in := range fw.sup.Infos() {
			if in.ID == "launcher" {
				return in, true
			}
		}
		return model.PluginInfo{}, false
	}
	run := func(file string) *framework {
		t.Helper()
		startEnv(t, t.TempDir(), "DASH_PLUGINS_LAUNCHER_FILE="+file)
		fw, _ := loadFramework(t) // loadConfig does not read the file
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		if err := fw.start(ctx, []plugin.Factory{launcher.New}, func() {}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fw.sup.Shutdown(context.Background()) })
		return fw
	}

	fw := run(filepath.Join(dir, "missing.yaml"))
	waitFor(t, func() bool { in, ok := row(fw); return ok && in.State == model.PluginError })
	if _, ok := fw.hub.Latest("", model.TopicLauncherLinks); ok {
		t.Error("launcher.links is published although the file could not be read")
	}

	fw = run(good)
	waitFor(t, func() bool { in, ok := row(fw); return ok && in.State == model.PluginHealthy })
	env, ok := fw.hub.Latest("", model.TopicLauncherLinks)
	if !ok {
		t.Fatal("launcher.links is not published")
	}
	var e model.Envelope
	var data model.LauncherData
	if err := json.Unmarshal(env.Data, &e); err != nil || json.Unmarshal(e.Data, &data) != nil ||
		len(data.Links) != 1 || data.Links[0].Name != "Router" || data.Links[0].Icon != "unifi" {
		t.Errorf("launcher.links = %s (%v)", env.Data, err)
	}
}

// Every plugin that runs on its own says which setting turns it on, so its "not configured" row
// reads "not configured: set plugins.<id>.…" as the shims' rows did.
func TestEveryRealPluginSaysWhatToSet(t *testing.T) {
	for id, f := range realPlugins {
		h, ok := f().(plugin.ConfigHinter)
		if !ok {
			t.Errorf("%s: no ConfigHint", id)
			continue
		}
		if hint := h.ConfigHint(); !strings.HasPrefix(hint, "plugins."+string(id)+".") {
			t.Errorf("%s: hint %q", id, hint)
		}
	}
}
