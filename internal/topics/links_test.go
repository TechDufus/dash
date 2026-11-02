package topics

import (
	"context"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// linkRig is a Manager over a real supervisor that runs the real grafana, headlamp and alertmanager
// plugins next to a plugin that always contributes (so the overview is recomposed every round, as
// it is on a dash with a cluster), as cmd/dash registers them.
type linkRig struct {
	m   *Manager
	sup *plugin.Supervisor
	set *plugintest.Settings
	h   *hub.Hub
}

func newLinkRig(t *testing.T, lab ...plugin.InstanceConfig) *linkRig {
	t.Helper()
	r := &linkRig{h: hub.New(), set: &plugintest.Settings{}}
	r.sup = plugin.NewSupervisor(plugin.Options{Publisher: r.h, Settings: r.set, StatusEvery: 10 * time.Millisecond})
	r.h.SetOrder(r.sup.TopicOrder)
	for _, ic := range lab {
		r.set.Set(ic)
	}
	r.m = New(Deps{Hosts: r.sup, Contributors: r.sup.Contributors})
	if err := r.sup.Add(plugintest.New("steady").Factory(), grafana.New, headlamp.New, alertmanager.New); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.sup.Shutdown(context.Background()) })
	r.m.Run(ctx)
	return r
}

func headlampOn(url string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: "headlamp", Config: &pluginconf.Headlamp{URL: url, Cluster: "main"}}
}

func grafanaOn(url string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: "grafana", Config: &pluginconf.Grafana{URL: url}}
}

func grafanaOff() plugin.InstanceConfig {
	off := false
	return plugin.InstanceConfig{Name: "grafana", Enabled: &off, Config: &pluginconf.Grafana{URL: "https://gf.example.com"}}
}
