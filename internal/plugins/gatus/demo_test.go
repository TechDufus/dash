package gatus

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/svc"
)

func TestDemoTwin(t *testing.T) {
	hub := &plugintest.Hub{}
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: hub, Settings: &plugintest.Settings{}, Demo: true,
		StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second, Log: slog.New(slog.DiscardHandler),
	})
	if err := sup.Add(New); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })

	var row model.PluginInfo
	eventually(t, "the twin to be healthy", func() bool {
		for _, pi := range sup.Infos() {
			if pi.ID == "gatus" {
				row = pi
			}
		}
		return row.State == model.PluginHealthy
	})

	ev, ok := hub.Last("gatus.status")
	if !ok {
		t.Fatal("the twin published nothing")
	}
	data := ev.Data.(model.StatusData)
	if !slices.Equal(data.CriticalGroups, []string{"infrastructure"}) {
		t.Errorf("criticalGroups = %v", data.CriticalGroups)
	}
	groups := map[string]int{}
	for _, e := range data.Endpoints {
		groups[e.Group]++
		if e.Results == nil || len(e.Results) == 0 {
			t.Errorf("%s has no results", e.Key)
		}
		if !strings.HasSuffix(e.URL, ".example.com") {
			t.Errorf("%s probes %q: demo data names only example.com", e.Key, e.URL)
		}
	}
	if groups["infrastructure"] == 0 || len(groups) < 4 {
		t.Errorf("groups = %v: want an infrastructure group among the others", groups)
	}
	if len(ev.Sources) != 1 || ev.Sources[0].Name != "gatus" || !ev.Sources[0].OK {
		t.Errorf("sources = %+v", ev.Sources)
	}
	plugintest.AssertRefs(t, sup, pluginID, data, model.Endpoint{})

	s, ok := sup.Service(pluginID)
	if !ok {
		t.Fatal("the twin exports no service")
	}
	es := s.(svc.EndpointHealth)
	if es.Revision() == 0 {
		t.Error("Revision is 0 after the twin published")
	}
	if state, known := es.ByHost("gateway.example.com"); !known || state != model.HealthOK {
		t.Errorf("ByHost(gateway) = %s, %v", state, known)
	}
	if up, known := es.ByName("DNS"); !known || !up {
		t.Errorf("ByName(dns) = %v, %v", up, known)
	}
	// Only checks that fail now raise attention; the fake world is healthy at the moment.
	for _, c := range sup.Contributors() {
		if c.ID == pluginID && c.Attention == nil {
			t.Error("the twin is not an attention source")
		}
	}
}

func TestPluginHasATwinAndTheTwinIsNotTheRealThing(t *testing.T) {
	d, ok := New().(plugin.Demo)
	if !ok {
		t.Fatal("the plugin does not implement plugin.Demo")
	}
	twin := d.Demo()
	if twin.Manifest().ID != pluginID || len(twin.Manifest().Topics) != 1 {
		t.Errorf("twin manifest: %+v", twin.Manifest())
	}
	if _, isReal := twin.(*Plugin); isReal {
		t.Error("the twin is the real plugin")
	}
}
