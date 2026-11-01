package grafana_test

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/grafana"
)

const base = "https://grafana.example.com"

func on(url string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: grafana.ID, Config: &pluginconf.Grafana{URL: url}}
}

// run starts a supervisor with the grafana plugin and ics, and returns it with the settings the
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
	if err := sup.Add(grafana.New); err != nil {
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
		if pi.ID == string(grafana.ID) {
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

func hrefs(ls []model.Link) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Href)
	}
	return out
}

// The ported grafana half of the old links_test.go: the alert list and the home page, of kind
// grafana.
func TestGrafanaLinks(t *testing.T) {
	sup, _ := run(t, false, on(base))
	res := sup.Links()
	for _, tc := range []struct{ kind, label, want string }{
		{"Alerts", "Grafana alerts", base + "/alerting/list"},
		{"Host", "Grafana", base + "/"},
	} {
		got := res.Resolve(plugin.LinkRequest{Kind: tc.kind})
		if len(got) != 1 || got[0].Href != tc.want || got[0].Label != tc.label || got[0].Kind != model.LinkKindGrafana {
			t.Errorf("%s: got %+v, want %s %q of kind grafana", tc.kind, got, tc.label, tc.want)
		}
	}
	for _, kind := range []string{"Pod", "VM", "HostShell", "ProxmoxUI", ""} {
		if got := res.Resolve(plugin.LinkRequest{Kind: kind, Namespace: "a", Name: "b"}); len(got) != 0 {
			t.Errorf("kind %q must get no Grafana link, got %+v", kind, got)
		}
	}
}

func TestTrailingSlashIsTrimmed(t *testing.T) {
	p := grafana.New().(*grafana.Plugin)
	if err := p.Start(context.Background(), nil, &pluginconf.Grafana{URL: base + "/"}); err != nil {
		t.Fatal(err)
	}
	if got := p.Links(plugin.LinkRequest{Kind: "Alerts"}); len(got) != 1 || got[0].Href != base+"/alerting/list" {
		t.Errorf("got %+v", got)
	}
}

// A provider that is not configured, not started or stopped contributes an empty non-nil slice.
func TestUnconfiguredGrafanaGivesNoLinks(t *testing.T) {
	p := grafana.New().(*grafana.Plugin)
	for _, kind := range []string{"Alerts", "Host"} {
		if got := p.Links(plugin.LinkRequest{Kind: kind}); got == nil || len(got) != 0 {
			t.Errorf("before Start, %s: want an empty non-nil slice, got %#v", kind, got)
		}
	}
	if err := p.Start(context.Background(), nil, &pluginconf.Grafana{URL: base}); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.Links(plugin.LinkRequest{Kind: "Alerts"}); got == nil || len(got) != 0 {
		t.Errorf("after Stop: want an empty non-nil slice, got %#v", got)
	}
	if p.Configured(&pluginconf.Grafana{}) || !p.Configured(&pluginconf.Grafana{URL: base}) || p.Configured("nope") {
		t.Error("Configured must be true exactly for a section with a URL")
	}
	if err := p.Start(context.Background(), nil, "nope"); err == nil {
		t.Error("a configuration of the wrong type must fail Start")
	}
}

// With no URL the plugin is disabled (auto-enable) and gives no link; enabled: true without one is
// unconfigured. Both are hidden, and the Manager's link snapshot is empty.
func TestGrafanaWithoutURLIsHiddenAndGivesNoLinks(t *testing.T) {
	yes := true
	sup, _ := run(t, false, on(""))
	if s := state(sup); s != model.PluginDisabled {
		t.Errorf("no URL, enabled unset: state %s, want disabled", s)
	}
	sup2, _ := run(t, false, plugin.InstanceConfig{Name: grafana.ID, Enabled: &yes, Config: &pluginconf.Grafana{}})
	if s := state(sup2); s != model.PluginUnconfigured {
		t.Errorf("no URL, enabled true: state %s, want unconfigured", s)
	}
	for _, s := range []*plugin.Supervisor{sup, sup2} {
		if got := s.Links().Resolve(plugin.LinkRequest{Kind: "Alerts"}); got == nil || len(got) != 0 {
			t.Errorf("want an empty non-nil slice, got %#v", got)
		}
	}
}

// With grafana disabled the alert link and the host link are absent, and they come back when it is
// enabled again.
func TestDisablingGrafanaRemovesItsLinks(t *testing.T) {
	sup, set := run(t, false, on(base))
	req := []plugin.LinkRequest{{Kind: "Alerts"}, {Kind: "Host"}}
	for _, r := range req {
		if len(sup.Links().Resolve(r)) != 1 {
			t.Fatalf("%s: grafana is running and must link", r.Kind)
		}
	}
	off := false
	set.Set(plugin.InstanceConfig{Name: grafana.ID, Enabled: &off, Config: &pluginconf.Grafana{URL: base}})
	sup.Reload()
	eventually(t, "grafana to be disabled", func() bool { return state(sup) == model.PluginDisabled })
	for _, r := range req {
		if got := sup.Links().Resolve(r); got == nil || len(got) != 0 {
			t.Errorf("%s: disabled grafana must give an empty non-nil slice, got %#v", r.Kind, got)
		}
	}
	set.Set(on(base))
	sup.Reload()
	eventually(t, "grafana to link again", func() bool { return len(sup.Links().Resolve(req[0])) == 1 })
}

// A changed URL applies on reconfigure without a restart of dash.
func TestChangingTheURLReachesTheLinks(t *testing.T) {
	sup, set := run(t, false, on(base))
	set.Set(on("https://g2.example.com"))
	sup.Reload()
	eventually(t, "the new URL", func() bool {
		return slices.Equal(hrefs(sup.Links().Resolve(plugin.LinkRequest{Kind: "Host"})), []string{"https://g2.example.com/"})
	})
}

// Visibility rule (a): grafana has no topic, so the check is that its row turns visible when the
// URL is set, through the same reconfigure path.
func TestRowTurnsVisible(t *testing.T) {
	yes := true
	plugintest.AssertTopicsReachOpenStream(t, grafana.New,
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Grafana{}},
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Grafana{URL: base}})
}

func TestDemoTwinLinksToExampleCom(t *testing.T) {
	// The configuration is empty: the twin runs anyway and ignores it.
	sup, _ := run(t, true, plugin.InstanceConfig{Name: grafana.ID, Config: &pluginconf.Grafana{}})
	if s := state(sup); !plugin.Visible(s) {
		t.Fatalf("the demo twin is %s, want visible", s)
	}
	got := sup.Links().Resolve(plugin.LinkRequest{Kind: "Alerts"})
	if len(got) != 1 || got[0].Href != world.GrafanaURL+"/alerting/list" || got[0].Kind != model.LinkKindGrafana {
		t.Errorf("demo alert link = %+v", got)
	}
	if want, got := grafana.New().Manifest(), grafana.New().(plugin.Demo).Demo().Manifest(); want.ID != got.ID || want.Name != got.Name || want.Version != got.Version {
		t.Errorf("the twin's manifest %+v differs from the plugin's %+v", got, want)
	}
}
