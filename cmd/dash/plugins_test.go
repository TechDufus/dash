package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
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
// Manager's first publishes find every Host running and none is dropped. Everything the SPA
// reads arrives, and the shims are live, and restart-only.
func TestFrameworkStartOrderPublishesFirstPoll(t *testing.T) {
	startEnv(t, t.TempDir())
	fw, res := loadFramework(t)
	cfg := res.Config
	mgr := topics.New(topics.Deps{
		Cfg: cfg, Cluster: testCluster(), Hosts: fw.sup, Contributors: fw.sup.Contributors, Links: links.New(cfg),
		AM: upstream.NewAlertmanager("", nil), Gatus: upstream.NewGatus("", nil),
		PVE: upstream.NewProxmox("", "", "", cfg.ProxmoxTokenSecret, nil),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, mgr.Plugins(), func() { mgr.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	want := []string{"core.plugins", "core.overview", "alerts", "status", "vms", "host", "cluster", "storage", "apps", "flux"}
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

	if fw.sup.Live("plugins.proxmox") {
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
	files := func(headlamp, grafana string) string {
		return "plugins:\n  headlamp:\n    url: " + headlamp + "\n  grafana:\n    url: " + grafana + "\n"
	}
	writeTestFile(t, cfgPath, files("https://hl-a.example.com", "https://gf-a.example.com"))
	startEnv(t, dir, "DASH_CONFIG="+cfgPath)
	fw, _ := loadFramework(t)

	// headlamp is a plugin that follows its section; grafana has no plugin in this framework.
	headlamp := plugintest.New("headlamp", plugintest.Config(func() any { return &pluginconf.Headlamp{} }))
	ctx, cancel := context.WithCancel(context.Background())
	if err := fw.start(ctx, []plugin.Factory{headlamp.Factory()}, func() {}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done; fw.sup.Shutdown(context.Background()) }()
	waitFor(t, func() bool { return headlamp.Starts() == 1 })

	writeTestFile(t, cfgPath, files("https://hl-b.example.com", "https://gf-b.example.com"))
	waitFor(t, func() bool { return headlamp.Starts() == 2 })
	if got := headlamp.Config().(*pluginconf.Headlamp).URL; got != "https://hl-b.example.com" {
		t.Errorf("the plugin restarted with %q, want the new section", got)
	}
	waitFor(t, func() bool { return len(fw.reloader.RestartRequired()) > 0 })
	if got := fw.reloader.RestartRequired(); !slices.Equal(got, []string{"plugins.grafana.url"}) {
		t.Errorf("restart required = %v, want only the section no plugin applies", got)
	}
}

// Demo mode answers /api/v1/series through the prometheus plugin's demo twin: the series
// handler asks the running plugin for its Querier, and the demo source no longer serves it.
func TestDemoSeriesComeFromThePrometheusTwin(t *testing.T) {
	startEnv(t, t.TempDir(), "DASH_DEMO=true")
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	cfg := demo.Config(res.Config)
	fw := newFramework(res, store, config.ReloadOptions{}, nil, nil, true)
	src := demo.New(cfg)
	src.HasReal = hasRealPlugin
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, registry(src.Plugins(fw.sup)), func() { src.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	srv := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, AM: src, VLogs: src, Data: src, Health: fw.sup})
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
