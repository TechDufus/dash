package topics

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// fluxKube is a synced cluster holding two Kustomizations.
func fluxKube() *fakeKube {
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", true))
	f.add(kube.GVRKustomizations, kustomization("infra", true))
	return f
}

// linkRig is a Manager over a real supervisor that runs the real grafana and headlamp plugins
// next to the Manager's shims, as cmd/dash registers them.
type linkRig struct {
	m   *Manager
	sup *plugin.Supervisor
	set *plugintest.Settings
	h   *hub.Hub
	// providerReads counts Supervisor.Links calls: one per rebuild, not one per object.
	providerReads atomic.Int32
}

func newLinkRig(t *testing.T, cfg config.Config, lab ...plugin.InstanceConfig) *linkRig {
	t.Helper()
	r := &linkRig{h: hub.New(), set: &plugintest.Settings{}}
	r.sup = plugin.NewSupervisor(plugin.Options{Publisher: r.h, Settings: r.set, StatusEvery: 10 * time.Millisecond})
	r.h.SetOrder(r.sup.TopicOrder)
	for _, ic := range lab {
		r.set.Set(ic)
	}
	r.m = New(Deps{
		Cfg: cfg, Cluster: kubeOf(fluxKube()), Hosts: r.sup, Contributors: r.sup.Contributors,
		LinkProviders: func() plugin.LinkResolver { r.providerReads.Add(1); return r.sup.Links() },
	})
	if err := r.sup.Add(append(r.m.Plugins(), grafana.New, headlamp.New, alertmanager.New)...); err != nil {
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

func headlampOff() plugin.InstanceConfig {
	off := false
	return plugin.InstanceConfig{Name: "headlamp", Enabled: &off, Config: &pluginconf.Headlamp{URL: "https://hl.example.com", Cluster: "main"}}
}

func grafanaOff() plugin.InstanceConfig {
	off := false
	return plugin.InstanceConfig{Name: "grafana", Enabled: &off, Config: &pluginconf.Grafana{URL: "https://gf.example.com"}}
}

// fluxLinks is the number of Kustomizations of the flux topic and the kinds of their links.
func (r *linkRig) fluxLinks(t *testing.T) (objs int, kinds []model.LinkKind) {
	t.Helper()
	env, ok := latest(r.h, model.TopicFlux)
	if !ok {
		return 0, nil
	}
	var d model.FluxData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	for _, o := range d.Kustomizations {
		for _, l := range o.Links {
			kinds = append(kinds, l.Kind)
		}
	}
	return len(d.Kustomizations), kinds
}

func hasKind(kinds []model.LinkKind, k model.LinkKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

// With headlamp running a Flux object's payload carries its "Open in Headlamp" link; with headlamp
// disabled it carries none. The Manager reads the providers through Supervisor.Links, so a change
// of the rows reaches the next rebuild without a restart. (The host's Grafana link is the
// hardware plugin's.)
func TestLinksFollowTheHeadlampRow(t *testing.T) {
	r := newLinkRig(t, config.Defaults(), headlampOn("https://hl.example.com"), grafanaOn("https://gf.example.com"))

	eventually(t, "headlamp links on the Flux objects", func() bool {
		n, kinds := r.fluxLinks(t)
		return n == 2 && hasKind(kinds, model.LinkKindHeadlamp)
	})

	r.set.Set(headlampOff())
	r.set.Set(grafanaOff())
	r.sup.Reload()
	eventually(t, "both rows to be disabled", func() bool {
		states := map[string]model.PluginState{}
		for _, in := range r.sup.Infos() {
			states[in.ID] = in.State
		}
		return states["headlamp"] == model.PluginDisabled && states["grafana"] == model.PluginDisabled
	})
	r.m.mark(model.TopicFlux)
	eventually(t, "the objects to lose their headlamp links", func() bool {
		n, kinds := r.fluxLinks(t)
		return n == 2 && !hasKind(kinds, model.LinkKindHeadlamp)
	})

	// Back on: the links come back.
	r.set.Set(headlampOn("https://hl.example.com"))
	r.set.Set(grafanaOn("https://gf.example.com"))
	r.sup.Reload()
	eventually(t, "headlamp to run again", func() bool { return r.sup.Running("headlamp") && r.sup.Running("grafana") })
	r.m.mark(model.TopicFlux)
	eventually(t, "the links to return", func() bool {
		_, kinds := r.fluxLinks(t)
		return hasKind(kinds, model.LinkKindHeadlamp)
	})
}

// The providers are read once per rebuild: two objects and the overview are linked from one
// snapshot, and the next rebuild reads them again.
func TestProvidersAreReadOncePerRebuild(t *testing.T) {
	var reads atomic.Int32
	m := New(Deps{
		Cfg: config.Defaults(), Cluster: kubeOf(fluxKube()),
		Hosts:         plugintest.AllRunning(hub.New(), LegacyManifests()...),
		LinkProviders: func() plugin.LinkResolver { reads.Add(1); return plugin.NoLinks },
	})
	m.mark(model.TopicFlux)
	m.rebuild()
	if n := reads.Load(); n != 1 {
		t.Errorf("providers read %d times for a rebuild of flux and the overview, want once", n)
	}
	m.mark(model.TopicFlux)
	m.rebuild()
	if n := reads.Load(); n != 2 {
		t.Errorf("providers read %d times after two rebuilds, want 2", n)
	}
	if m.linkSet.Load() != nil {
		t.Error("the snapshot outlives its rebuild")
	}
}

// A Manager with no supervisor (tests, tools) builds every link-bearing payload with empty,
// non-nil link lists instead of panicking.
func TestManagerWithoutProvidersBuildsEmptyLinks(t *testing.T) {
	f := fluxKube()
	m := New(Deps{Cfg: config.Defaults(), Cluster: kubeOf(f)})
	d := m.buildFlux(f)
	if len(d.Kustomizations) != 2 {
		t.Fatalf("flux = %+v", d)
	}
	for _, o := range d.Kustomizations {
		if o.Links == nil || len(o.Links) != 0 {
			t.Errorf("%s: links = %#v, want an empty non-nil slice", o.Name, o.Links)
		}
	}
}
