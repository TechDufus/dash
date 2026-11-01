package headlamp_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

const root = "https://headlamp.example.com"

func on(base, cluster string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: headlamp.ID, Config: &pluginconf.Headlamp{URL: base, Cluster: cluster}}
}

// run starts a supervisor with the headlamp plugin and ics, and returns it with the settings the
// test changes.
func run(t *testing.T, demo bool, ics ...plugin.InstanceConfig) (*plugin.Supervisor, *plugintest.Settings) {
	t.Helper()
	set := &plugintest.Settings{}
	set.Set(ics...)
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher:   &plugintest.Hub{},
		Settings:    set,
		StatusEvery: 10 * time.Millisecond,
		StopTimeout: time.Second,
		Log:         slog.New(slog.DiscardHandler),
		Demo:        demo,
	})
	if err := sup.Add(headlamp.New); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return sup, set
}

func state(sup *plugin.Supervisor) model.PluginState {
	for _, pi := range sup.Infos() {
		if pi.ID == string(headlamp.ID) {
			return pi.State
		}
	}
	return ""
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Every Kubernetes kind the old link builder knew, with its path in Headlamp.
func TestHeadlampKinds(t *testing.T) {
	const base = root + "/c/main/"
	tests := []struct {
		kind, ns, name, want string
	}{
		{"Pod", "homepage", "homepage-abc", base + "pods/homepage/homepage-abc"},
		{"Deployment", "homepage", "homepage", base + "deployments/homepage/homepage"},
		{"StatefulSet", "monitoring", "vmsingle", base + "statefulsets/monitoring/vmsingle"},
		{"DaemonSet", "kube-system", "cilium", base + "daemonsets/kube-system/cilium"},
		{"Job", "rustfs", "j", base + "jobs/rustfs/j"},
		{"CronJob", "rustfs", "nas-backup", base + "cronjobs/rustfs/nas-backup"},
		{"Node", "", "node-1", base + "nodes/node-1"},
		{"Namespace", "", "monitoring", base + "namespaces/monitoring"},
		{"PersistentVolumeClaim", "monitoring", "data", base + "storage/persistentvolumeclaims/monitoring/data"},
		{"Kustomization", "flux-system", "app-homepage", base + "customresources/kustomizations.kustomize.toolkit.fluxcd.io/flux-system/app-homepage"},
		{"HelmRelease", "monitoring", "vm", base + "customresources/helmreleases.helm.toolkit.fluxcd.io/monitoring/vm"},
		{"GitRepository", "flux-system", "flux-system", base + "customresources/gitrepositories.source.toolkit.fluxcd.io/flux-system/flux-system"},
		{"OCIRepository", "flux-system", "x", base + "customresources/ocirepositories.source.toolkit.fluxcd.io/flux-system/x"},
		{"HelmRepository", "flux-system", "x", base + "customresources/helmrepositories.source.toolkit.fluxcd.io/flux-system/x"},
		{"Cluster", "db", "pg", base + "customresources/clusters.postgresql.cnpg.io/db/pg"},
		{"Certificate", "gateway", "wild", base + "customresources/certificates.cert-manager.io/gateway/wild"},
	}
	sup, _ := run(t, false, on(root, "main"))
	res := sup.Links()
	for _, tc := range tests {
		l := res.Resolve(plugin.LinkRequest{Kind: tc.kind, Namespace: tc.ns, Name: tc.name})
		if len(l) != 1 || l[0].Kind != model.LinkKindHeadlamp || l[0].Href != tc.want || l[0].Label != "Open in Headlamp" {
			t.Errorf("%s: link %+v, want %q", tc.kind, l, tc.want)
		}
	}
	if len(tests) != 16 {
		t.Errorf("the table lists %d kinds, the provider serves 16", len(tests))
	}
	for _, kind := range []string{"Widget", "Alerts", "Host", "VM", ""} {
		if l := res.Resolve(plugin.LinkRequest{Kind: kind, Namespace: "a", Name: "b"}); l == nil || len(l) != 0 {
			t.Errorf("kind %q must give an empty non-nil slice, got %#v", kind, l)
		}
	}
}

func TestClusterAndNamesAreEscaped(t *testing.T) {
	p := headlamp.New().(*headlamp.Plugin)
	if err := p.Start(context.Background(), nil, &pluginconf.Headlamp{URL: root + "/", Cluster: "my cluster"}); err != nil {
		t.Fatal(err)
	}
	got := p.Links(plugin.LinkRequest{Kind: "Pod", Namespace: "a b", Name: "c/d"})
	want := root + "/c/my%20cluster/pods/a%20b/c%2Fd"
	if len(got) != 1 || got[0].Href != want {
		t.Errorf("got %+v, want %q", got, want)
	}
}

// An unconfigured provider contributes nothing.
func TestUnconfiguredToolsGiveNoLinks(t *testing.T) {
	p := headlamp.New().(*headlamp.Plugin)
	req := plugin.LinkRequest{Kind: "Pod", Namespace: "a", Name: "b"}
	if got := p.Links(req); got == nil || len(got) != 0 {
		t.Errorf("before Start: want an empty non-nil slice, got %#v", got)
	}
	if err := p.Start(context.Background(), nil, &pluginconf.Headlamp{Cluster: "main"}); err != nil {
		t.Fatal(err)
	}
	if got := p.Links(req); got == nil || len(got) != 0 {
		t.Errorf("no URL: want an empty non-nil slice, got %#v", got)
	}
	if err := p.Start(context.Background(), nil, &pluginconf.Headlamp{URL: root, Cluster: "main"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.Links(req); got == nil || len(got) != 0 {
		t.Errorf("after Stop: want an empty non-nil slice, got %#v", got)
	}
	if p.Configured(&pluginconf.Headlamp{Cluster: "main"}) || !p.Configured(&pluginconf.Headlamp{URL: root}) || p.Configured(7) {
		t.Error("Configured must be true exactly for a section with a URL")
	}
	if err := p.Start(context.Background(), nil, 7); err == nil {
		t.Error("a configuration of the wrong type must fail Start")
	}
}

// With no URL the plugin is disabled (auto-enable); enabled: true without one is unconfigured.
func TestHeadlampWithoutURLIsHiddenAndGivesNoLinks(t *testing.T) {
	yes := true
	sup, _ := run(t, false, on("", "main"))
	if s := state(sup); s != model.PluginDisabled {
		t.Errorf("no URL, enabled unset: state %s, want disabled", s)
	}
	sup2, _ := run(t, false, plugin.InstanceConfig{Name: headlamp.ID, Enabled: &yes, Config: &pluginconf.Headlamp{Cluster: "main"}})
	if s := state(sup2); s != model.PluginUnconfigured {
		t.Errorf("no URL, enabled true: state %s, want unconfigured", s)
	}
	for _, s := range []*plugin.Supervisor{sup, sup2} {
		if got := s.Links().Resolve(plugin.LinkRequest{Kind: "Pod", Namespace: "a", Name: "b"}); got == nil || len(got) != 0 {
			t.Errorf("want an empty non-nil slice, got %#v", got)
		}
	}
}

// With headlamp disabled no pod or workload carries a headlamp link.
func TestDisablingHeadlampRemovesItsLinks(t *testing.T) {
	sup, set := run(t, false, on(root, "main"))
	req := plugin.LinkRequest{Kind: "Pod", Namespace: "media", Name: "app-0"}
	if len(sup.Links().Resolve(req)) != 1 {
		t.Fatal("headlamp is running and must link")
	}
	off := false
	set.Set(plugin.InstanceConfig{Name: headlamp.ID, Enabled: &off, Config: &pluginconf.Headlamp{URL: root, Cluster: "main"}})
	sup.Reload()
	eventually(t, "headlamp to be disabled", func() bool { return state(sup) == model.PluginDisabled })
	for _, l := range sup.Links().Resolve(req) {
		t.Errorf("disabled headlamp left a link: %+v", l)
	}
	set.Set(on(root, "main"))
	sup.Reload()
	eventually(t, "headlamp to link again", func() bool { return len(sup.Links().Resolve(req)) == 1 })
}

// A changed cluster name applies on reconfigure.
func TestChangingTheClusterReachesTheLinks(t *testing.T) {
	sup, set := run(t, false, on(root, "main"))
	set.Set(on(root, "lab"))
	sup.Reload()
	req := plugin.LinkRequest{Kind: "Node", Name: "n1"}
	eventually(t, "the new cluster", func() bool {
		l := sup.Links().Resolve(req)
		return len(l) == 1 && l[0].Href == root+"/c/lab/nodes/n1"
	})
}

// Visibility rule (a): headlamp has no topic, so the check is that its row turns visible when the
// URL is set, through the same reconfigure path.
func TestRowTurnsVisible(t *testing.T) {
	yes := true
	plugintest.AssertTopicsReachOpenStream(t, headlamp.New,
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Headlamp{Cluster: "main"}},
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Headlamp{URL: root, Cluster: "main"}})
}

func TestDemoTwinLinksToExampleCom(t *testing.T) {
	// The configuration is empty: the twin runs anyway and ignores it.
	sup, _ := run(t, true, plugin.InstanceConfig{Name: headlamp.ID, Config: &pluginconf.Headlamp{}})
	if s := state(sup); !plugin.Visible(s) {
		t.Fatalf("the demo twin is %s, want visible", s)
	}
	got := sup.Links().Resolve(plugin.LinkRequest{Kind: "Pod", Namespace: "media", Name: "app-0"})
	want := world.HeadlampURL + "/c/" + world.HeadlampCluster + "/pods/media/app-0"
	if len(got) != 1 || got[0].Href != want || got[0].Kind != model.LinkKindHeadlamp {
		t.Errorf("demo pod link = %+v, want %q", got, want)
	}
	if want, got := headlamp.New().Manifest(), headlamp.New().(plugin.Demo).Demo().Manifest(); want.ID != got.ID || want.Name != got.Name || want.Version != got.Version {
		t.Errorf("the twin's manifest %+v differs from the plugin's %+v", got, want)
	}
}
