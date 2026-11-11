package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/launcher"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// Every plugins.* section of the tree is an instance, of the type its plugin's Config returns.
func TestResolvedSettingsListsEverySectionOnce(t *testing.T) {
	off := false
	tree := config.DefaultTree()
	tree.Plugins.Flux.Enabled = &off
	ics := resolvedSettings{func() *config.Resolved { return &config.Resolved{Tree: tree} }}.Instances()

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

// FromUI of an instance follows the provenance of its section's URL in the same resolution: a
// URL stored from the UI sets it, one from the environment (over a UI value too) does not.
func TestResolvedSettingsMarksURLsFromTheUI(t *testing.T) {
	res, err := config.Resolve(config.Sources{
		Environ: []string{"DASH_DEV=true", "DASH_PLUGINS_GATUS_URL=http://127.0.0.1:8080"},
		UI: map[string]any{"plugins": map[string]any{
			"prometheus": map[string]any{"url": "http://127.0.0.1:9090"},
			"gatus":      map[string]any{"url": "http://127.0.0.1:9999"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fromUI := map[plugin.ID]bool{}
	for _, ic := range (resolvedSettings{func() *config.Resolved { return res }}).Instances() {
		fromUI[ic.Name] = ic.FromUI
	}
	for id, want := range map[plugin.ID]bool{"prometheus": true, "gatus": false, "alertmanager": false} {
		if fromUI[id] != want {
			t.Errorf("%s: FromUI = %v, want %v", id, fromUI[id], want)
		}
	}
}

// A plugin URL that keeps its value but moves between the config file and the UI store restarts
// the instance with the new FromUI: from the UI its loopback dial is refused, from the file it
// is allowed again.
func TestPluginRestartsWhenItsURLMovesIntoOrOutOfTheUI(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer up.Close()
	get := func(f *plugintest.Fake) error {
		c, err := f.Host().HTTPClient(plugin.HTTPConfig{})
		if err != nil {
			return err
		}
		resp, err := c.Get(up.URL)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}

	dir := t.TempDir()
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set("plugins.grafana.url", up.URL); err != nil {
		t.Fatal(err)
	}
	st.Close()
	cfgPath := filepath.Join(dir, "config.yaml")
	inFile := "plugins:\n  grafana:\n    url: " + up.URL + "\n"
	writeTestFile(t, cfgPath, inFile)
	startEnv(t, dir, "DASH_CONFIG="+cfgPath)
	fw, res := loadFramework(t)
	if res.FromUI("plugins.grafana") {
		t.Fatal("at startup the file's URL wins")
	}
	f := plugintest.New("grafana")
	startFake(t, fw, f)
	waitFor(t, func() bool { return f.Starts() == 1 && fw.sup.Running("grafana") })
	if err := get(f); err != nil {
		t.Fatalf("a loopback URL from the file: %v", err)
	}

	// The URL leaves the file; the store holds the same value.
	writeTestFile(t, cfgPath, "plugins: {}\n")
	waitFor(t, func() bool { return f.Starts() == 2 && fw.sup.Running("grafana") })
	if !fw.reloader.Resolved().FromUI("plugins.grafana") {
		t.Fatal("the URL now comes from the UI")
	}
	if err := get(f); !errors.Is(err, plugin.ErrBlockedAddress) {
		t.Fatalf("a loopback URL from the UI: err = %v, want ErrBlockedAddress", err)
	}

	// It comes back to the file.
	writeTestFile(t, cfgPath, inFile)
	waitFor(t, func() bool { return f.Starts() == 3 && fw.sup.Running("grafana") })
	if err := get(f); err != nil {
		t.Fatalf("a loopback URL from the file again: %v", err)
	}
}

func TestBuiltinManifestsAreTheFourteenInDisplayOrder(t *testing.T) {
	var ids []string
	for _, m := range builtinManifests() {
		ids = append(ids, string(m.ID))
	}
	want := []string{"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "ssh", "kubernetes", "flux", "cnpg", "rustfs", "grafana", "headlamp", "launcher"}
	if !slices.Equal(ids, want) {
		t.Fatalf("manifests = %v, want %v", ids, want)
	}
}

// loadFramework loads the configuration of the environment the way run does and builds the
// framework around it. The reloader polls every millisecond.
func loadFramework(t *testing.T) (*framework, *config.Resolved) {
	t.Helper()
	res, store, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, false), res
}

// The framework as run starts it: Supervisor.Run returns before the overview publisher runs, so
// its first publishes find every Host running and none is dropped. The overview it composes from
// what a running plugin contributes arrives, and the plugin follows its section live.
func TestFrameworkStartOrderPublishesFirstPoll(t *testing.T) {
	startEnv(t, t.TempDir())
	fw, _ := loadFramework(t)
	ov := overview.New(overview.Deps{Hosts: fw.sup, Contributors: fw.sup.Contributors})
	fluxFake := plugintest.New("flux", plugintest.Config(func() any { return &pluginconf.Flux{} }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, []plugin.Factory{fluxFake.Factory()}, func() { ov.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())

	want := []string{"core.plugins", "core.overview"}
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

	if !fw.sup.Live("plugins.flux") {
		t.Error("the flux section does not apply live, but flux is a plugin that follows it")
	}
	for _, in := range fw.sup.Infos() {
		if in.State == model.PluginDisabled || in.State == model.PluginError || in.State == model.PluginBlocked {
			t.Errorf("%s is %s %q with nothing configured", in.ID, in.State, in.Message)
		}
	}
}

// The reloader and the supervisor as run joins them: a changed plugins.<id> section of a plugin
// that can be reconfigured reaches it in-process (Subscribe -> Reload) and is not reported as
// needing a restart (the Live hook), while a setting that is only read at startup (server.*) still
// is. flux is one of the plugins that follow their section.
func TestFrameworkAppliesLiveSectionsAndReportsTheRest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	files := func(headlamp, flux, streams string) string {
		return "plugins:\n  headlamp:\n    url: " + headlamp + "\n  flux:\n    suspendReasonAnnotation: " + flux + "\nserver:\n  limits:\n    streams: " + streams + "\n"
	}
	writeTestFile(t, cfgPath, files("https://hl-a.example.com", "example.com/a", "100"))
	startEnv(t, dir, "DASH_CONFIG="+cfgPath)
	fw, _ := loadFramework(t)

	headlamp := plugintest.New("headlamp", plugintest.Config(func() any { return &pluginconf.Headlamp{} }))
	fluxFake := plugintest.New("flux", plugintest.Config(func() any { return &pluginconf.Flux{} }))
	ctx, cancel := context.WithCancel(context.Background())
	if err := fw.start(ctx, []plugin.Factory{headlamp.Factory(), fluxFake.Factory()}, func() {}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done; fw.sup.Shutdown(context.Background()) }()
	waitFor(t, func() bool { return headlamp.Starts() == 1 && fluxFake.Starts() == 1 })

	writeTestFile(t, cfgPath, files("https://hl-b.example.com", "example.com/b", "200"))
	waitFor(t, func() bool { return headlamp.Starts() == 2 && fluxFake.Starts() == 2 })
	if got := headlamp.Config().(*pluginconf.Headlamp).URL; got != "https://hl-b.example.com" {
		t.Errorf("the plugin restarted with %q, want the new section", got)
	}
	if got := fluxFake.Config().(*pluginconf.Flux).SuspendReasonAnnotation; got != "example.com/b" {
		t.Errorf("flux restarted with suspendReasonAnnotation %q, want the new section", got)
	}
	waitFor(t, func() bool { return len(fw.reloader.RestartRequired()) > 0 })
	if got := fw.reloader.RestartRequired(); !slices.Equal(got, []string{"server.limits.streams"}) {
		t.Errorf("restart required = %v, want only the startup setting", got)
	}
}

// Demo mode answers /api/v1/series through the prometheus plugin's demo twin and the series the
// hardware twin registers: the handler asks the running plugin for its Querier and for the
// series.
func TestDemoSeriesComeFromThePrometheusTwin(t *testing.T) {
	fw, cfg := startDemo(t)

	srv := api.New(api.Deps{
		Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup,
		Auth: auth.Fixed(auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}),
	})
	r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/series?name=host.cpu&range=1h", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("series = %d %s", rec.Code, rec.Body)
	}
	var got model.Series
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Series) == 0 || len(got.Series[0].Points) == 0 {
		t.Fatalf("series body = %s (%v), want generated lines", rec.Body, err)
	}
}

// Demo mode publishes the launcher's links through the launcher plugin's demo twin, after every
// other topic (the launcher comes last in the display order); the flux, cnpg and rustfs twins'
// topics are flux.objects, cnpg.clusters and rustfs.usage.
func TestDemoPublishesTheLauncherLinksLast(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw) // exactly demoTopics, in this order

	env, ok := fw.hub.Latest("", model.TopicLauncherLinks)
	if !ok {
		t.Fatal("launcher.links has no snapshot")
	}
	var e model.Envelope
	var data model.LauncherData
	if err := json.Unmarshal(env.Data, &e); err != nil || json.Unmarshal(e.Data, &data) != nil || len(data.Links) != len(world.Launcher().Links) {
		t.Fatalf("launcher.links = %s (%v), want the demo links", env.Data, err)
	}
	// The demo's hosts, published by the hardware twin, carry the fake hosts' models in order.
	hostEnv, ok := fw.hub.Latest("", model.TopicHost)
	if !ok {
		t.Fatal("host has no snapshot")
	}
	var he model.Envelope
	var hosts model.HostsData
	if err := json.Unmarshal(hostEnv.Data, &he); err != nil || json.Unmarshal(he.Data, &hosts) != nil || len(hosts.Hosts) != 2 ||
		hosts.Hosts[0].Model != world.HostModel || hosts.Hosts[1].Model != world.SecondHostModel {
		t.Fatalf("hosts = %s, want models %q and %q", hostEnv.Data, world.HostModel, world.SecondHostModel)
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

// Every plugin says which setting turns it on, so its "not configured" row
// reads "not configured: set plugins.<id>.…".
func TestEveryRealPluginSaysWhatToSet(t *testing.T) {
	for _, f := range registry(nil, nil) {
		p := f()
		id := p.Manifest().ID
		h, ok := p.(plugin.ConfigHinter)
		if !ok {
			t.Errorf("%s: no ConfigHint", id)
			continue
		}
		if hint := h.ConfigHint(); !strings.HasPrefix(hint, "plugins."+string(id)+".") {
			t.Errorf("%s: hint %q", id, hint)
		}
	}
}
