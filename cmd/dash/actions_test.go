package main

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/topics"
)

// alertmanagerFake stands in for the alertmanager plugin: its section is
// *pluginconf.Alertmanager and it has the admin action silence.create.
func alertmanagerFake() *plugintest.Fake {
	f := plugintest.New("alertmanager", plugintest.Config(func() any { return &pluginconf.Alertmanager{} }))
	f.M.Actions = []plugin.ActionSpec{{Name: "silence.create", Title: "Silence"}}
	f.ActionFn = func(context.Context, string, auth.Principal, any) (any, error) { return nil, nil }
	return f
}

// startFake runs fake on the framework of the environment and shuts both down with the test.
func startFake(t *testing.T, fw *framework, fakes ...*plugintest.Fake) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	factories := make([]plugin.Factory, len(fakes))
	for i, f := range fakes {
		factories[i] = f.Factory()
	}
	if err := fw.start(ctx, factories, func() {}); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; fw.sup.Shutdown(context.Background()) })
}

func mounted(fw *framework, id plugin.ID, action string) bool {
	_, ok := fw.sup.Action(id, action)
	return ok
}

// plugins.alertmanager.actions.enabled, set in the environment, is what mounts silence.create:
// env -> config tree -> treeSettings -> supervisor. Without it the plugin runs and the action
// is not offered.
func TestActionsEnvMountsTheAction(t *testing.T) {
	for name, tc := range map[string]struct {
		env  []string
		want bool
	}{
		"enabled":     {[]string{"DASH_PLUGINS_ALERTMANAGER_URL=http://x.example", "DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED=true"}, true},
		"not enabled": {[]string{"DASH_PLUGINS_ALERTMANAGER_URL=http://x.example"}, false},
		"disabled":    {[]string{"DASH_PLUGINS_ALERTMANAGER_URL=http://x.example", "DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED=false"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			startEnv(t, t.TempDir(), tc.env...)
			fw, _ := loadFramework(t)
			startFake(t, fw, alertmanagerFake())
			waitFor(t, func() bool { return fw.sup.Running("alertmanager") })

			ref, ok := fw.sup.Action("alertmanager", "silence.create")
			if ok != tc.want {
				t.Fatalf("silence.create mounted = %v, want %v", ok, tc.want)
			}
			if ok && (ref.Spec.MinRole != auth.RoleAdmin || len(ref.DenyNamespaces) != 0) {
				t.Errorf("ref = %+v, want an admin action with no deny list (a silence has no namespace)", ref)
			}
		})
	}
}

// Flipping actions.enabled in the live configuration restarts the instance with the new policy:
// the supervisor compares the whole InstanceConfig, and the policy is part of it.
func TestActionsReloadMountsAndUnmounts(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	file := func(enabled string) string {
		return "plugins:\n  alertmanager:\n    url: http://x.example\n    actions:\n      enabled: " + enabled + "\n"
	}
	writeTestFile(t, cfgPath, file("false"))
	startEnv(t, dir, "DASH_CONFIG="+cfgPath)
	fw, _ := loadFramework(t)
	f := alertmanagerFake()
	startFake(t, fw, f)
	waitFor(t, func() bool { return f.Starts() == 1 && fw.sup.Running("alertmanager") })
	if mounted(fw, "alertmanager", "silence.create") {
		t.Fatal("mounted with actions.enabled false")
	}

	writeTestFile(t, cfgPath, file("true"))
	waitFor(t, func() bool { return mounted(fw, "alertmanager", "silence.create") })
	if f.Starts() != 2 {
		t.Errorf("starts = %d after enabling, want the instance restarted once", f.Starts())
	}

	writeTestFile(t, cfgPath, file("false"))
	waitFor(t, func() bool { return f.Starts() == 3 && fw.sup.Running("alertmanager") })
	if mounted(fw, "alertmanager", "silence.create") {
		t.Error("still mounted after actions.enabled went back to false")
	}
}

// victorialogs has no actions but a read policy: plugins.victorialogs.denyNamespaces reaches
// InstanceConfig.ReadDenyNamespaces, and from there the guard of every route of the instance.
func TestVictoriaLogsDenyNamespacesReachTheRoutes(t *testing.T) {
	res, err := config.Resolve(config.Sources{Environ: []string{
		"DASH_DEMO=true", `DASH_PLUGINS_VICTORIALOGS_DENY_NAMESPACES=["kube-system","secrets"]`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	ts := treeSettings{func() config.Tree { return res.Tree }}
	var ic plugin.InstanceConfig
	for _, c := range ts.Instances() {
		if c.Name == "victorialogs" {
			ic = c
		}
	}
	if want := []string{"kube-system", "secrets"}; !slices.Equal(ic.ReadDenyNamespaces, want) || ic.ActionsEnabled || ic.DenyNamespaces != nil {
		t.Fatalf("instance = %+v, want read deny %v and no action policy", ic, want)
	}

	f := plugintest.New("victorialogs", plugintest.Config(func() any { return &pluginconf.VictoriaLogs{} }))
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/logs", func(w http.ResponseWriter, _ *http.Request) {}, plugin.RouteSpec{Summary: "logs"})
	}
	sup := plugin.NewSupervisor(plugin.Options{Publisher: &plugintest.Hub{}, Settings: ts})
	if err := sup.Add(f.Factory()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); sup.Shutdown(context.Background()) }()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var seen []string
	h, ok := sup.Router("victorialogs", plugin.RouteGuard{Serve: func(w http.ResponseWriter, r *http.Request, mr plugin.MountedRoute, next http.HandlerFunc) {
		seen = mr.ReadDenyNamespaces
		next(w, r)
	}})
	if !ok {
		t.Fatal("no router")
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/plugins/victorialogs/logs", nil))
	if want := []string{"kube-system", "secrets"}; !slices.Equal(seen, want) {
		t.Errorf("the route guard saw %v, want %v", seen, want)
	}
}

// plugins.kubernetes.actions reaches the instance's policy, and its deny list arrives in the
// action ref the supervisor hands the API.
func TestActionPolicyOfASectionReachesTheActionRef(t *testing.T) {
	section := &pluginconf.Kubernetes{Actions: pluginconf.Actions{Enabled: true, DenyNamespaces: []string{"kube-system"}}}
	ic := instanceConfig(config.PluginSection{Name: "kubernetes", Type: "kubernetes", Value: section})
	if !ic.ActionsEnabled || !slices.Equal(ic.DenyNamespaces, []string{"kube-system"}) || ic.ReadDenyNamespaces != nil {
		t.Fatalf("instance = %+v", ic)
	}

	f := plugintest.New("kubernetes", plugintest.Config(func() any { return &pluginconf.Kubernetes{} }))
	f.M.Actions = []plugin.ActionSpec{{Name: "restart", Title: "Restart"}}
	f.ActionFn = func(context.Context, string, auth.Principal, any) (any, error) { return nil, nil }
	set := &plugintest.Settings{}
	set.Set(ic)
	sup := plugin.NewSupervisor(plugin.Options{Publisher: &plugintest.Hub{}, Settings: set})
	if err := sup.Add(f.Factory()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); sup.Shutdown(context.Background()) }()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	ref, ok := sup.Action("kubernetes", "restart")
	if !ok || !slices.Equal(ref.DenyNamespaces, []string{"kube-system"}) {
		t.Fatalf("ref = %+v, %v; want the deny list of the section", ref, ok)
	}
}

// A section without the interfaces leaves the policy off, and either field changing changes the
// InstanceConfig, which is what restarts the instance.
func TestInstanceConfigPolicyFollowsTheSection(t *testing.T) {
	tree := config.DefaultTree()
	instance := func(name string) plugin.InstanceConfig {
		for _, ic := range (treeSettings{func() config.Tree { return tree }}).Instances() {
			if string(ic.Name) == name {
				return ic
			}
		}
		t.Fatalf("no instance %s", name)
		return plugin.InstanceConfig{}
	}
	for _, name := range []string{"prometheus", "gatus", "grafana", "headlamp", "launcher", "proxmox"} {
		if ic := instance(name); ic.ActionsEnabled || ic.DenyNamespaces != nil || ic.ReadDenyNamespaces != nil {
			t.Errorf("%s: policy %+v, want none", name, ic)
		}
	}

	before := instance("alertmanager")
	tree.Plugins.Alertmanager.Actions.Enabled = true
	if after := instance("alertmanager"); !after.ActionsEnabled || reflect.DeepEqual(before, after) {
		t.Errorf("enabling actions: %+v -> %+v, want a different instance config", before, after)
	}
	vlBefore := instance("victorialogs")
	tree.Plugins.VictoriaLogs.DenyNamespaces = []string{"secrets"}
	if vlAfter := instance("victorialogs"); reflect.DeepEqual(vlBefore, vlAfter) {
		t.Error("changing denyNamespaces left the instance config as it was")
	}
}

// The registry lists one plugin per id in display order: the shim, unless a real factory is
// registered for that id, and then exactly that one is replaced.
func TestRegistryOrderAndReplacement(t *testing.T) {
	shims := (&topics.Manager{}).Plugins()
	ids := func(fs []plugin.Factory) (out []plugin.ID) {
		for _, f := range fs {
			out = append(out, f().Manifest().ID)
		}
		return out
	}

	if got := ids(registry(shims, nil)); !slices.Equal(got, displayOrder) {
		t.Fatalf("registry = %v, want %v", got, displayOrder)
	}
	var legacy []plugin.ID
	for _, m := range topics.LegacyManifests() {
		legacy = append(legacy, m.ID)
	}
	// What the shims still cover keeps its place relative to the display order.
	var covered []plugin.ID
	for _, id := range displayOrder {
		if slices.Contains(legacy, id) {
			covered = append(covered, id)
		}
	}
	if !slices.Equal(legacy, covered) {
		t.Errorf("legacy manifests %v are not in display order %v", legacy, displayOrder)
	}
	for id := range realPlugins {
		if !slices.Contains(displayOrder, id) {
			t.Errorf("real plugin %s is not in displayOrder", id)
		}
	}

	// A plugin that is still a shim, replaced by a real one.
	var shimmed plugin.ID
	for _, id := range displayOrder {
		if !hasRealPlugin(id) {
			shimmed = id
			break
		}
	}
	if shimmed == "" {
		t.Fatal("every plugin is real: the replacement needs a shim to replace")
	}
	real := plugintest.New(shimmed, plugintest.Status(plugin.StateHealthy, ""))
	impl := map[plugin.ID]plugin.Factory{shimmed: real.Factory()}
	maps.Copy(impl, realPlugins)
	got := registryOf(impl, shims)
	if !slices.Equal(ids(got), displayOrder) {
		t.Fatalf("registry with a real %s = %v, want the same order", shimmed, ids(got))
	}
	for _, f := range got {
		p := f()
		_, isShim := p.(*topics.Legacy)
		switch id := p.Manifest().ID; {
		case id == shimmed && (isShim || p != plugin.Plugin(real)):
			t.Errorf("%s is %T, want the real plugin", shimmed, p)
		case id != shimmed && !isShim && !hasRealPlugin(id):
			t.Errorf("%s is %T, want its shim", id, p)
		}
	}

	ms := manifestsOf(impl, topics.LegacyManifests())
	if len(ms) != len(displayOrder) {
		t.Fatalf("%d manifests, want %d", len(ms), len(displayOrder))
	}
	for i, m := range ms {
		if m.ID != displayOrder[i] {
			t.Errorf("manifest %d is %s, want %s", i, m.ID, displayOrder[i])
		}
		if m.ID == shimmed && len(m.Topics) != 0 {
			t.Errorf("%s manifest %+v is the shim's, want the real plugin's", shimmed, m)
		}
	}
}

// The registry scrubs what plugins log, the transport hook wraps what their clients send (a
// nil registry and a nil hook change nothing), and both reach the plugin through its Host.
func TestNewFrameworkWiresScrubbingAndTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	startEnv(t, t.TempDir())
	reg := secret.NewRegistry()
	reg.Add(secret.New("hunter2-hunter2", ""))
	res, store, err := loadConfig(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	var wrapped []plugin.ID
	tr := func(id plugin.ID, rt http.RoundTripper) http.RoundTripper { wrapped = append(wrapped, id); return rt }
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, reg, nil, tr, false)
	f := plugintest.New("grafana")
	startFake(t, fw, f)
	waitFor(t, func() bool { return fw.sup.Running("grafana") })

	if got := f.Host().ScrubLog("password hunter2-hunter2 failed"); got != "password [redacted] failed" {
		t.Errorf("ScrubLog = %q", got)
	}
	hc, err := f.Host().HTTPClient(plugin.HTTPConfig{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !slices.Equal(wrapped, []plugin.ID{"grafana"}) {
		t.Errorf("transport hook saw %v, want the one client of grafana", wrapped)
	}

	// No registry and no hook: nothing to scrub with, nothing wrapped, no panic.
	plain := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, false)
	g := plugintest.New("grafana")
	startFake(t, plain, g)
	waitFor(t, func() bool { return plain.sup.Running("grafana") })
	if got := g.Host().ScrubLog("hunter2-hunter2"); got != "hunter2-hunter2" {
		t.Errorf("ScrubLog without a registry = %q, want the line unchanged", got)
	}
}
