package topics

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// fluxRig is the shim, the supervisor, the Manager and the hub together over a kubernetes service
// the test swaps. The Manager is driven by hand (rebuild), so each round is where the test says;
// the supervisor's own status tick runs on its own.
type fluxRig struct {
	m      *Manager
	h      *hub.Hub
	sup    *plugin.Supervisor
	holder *kubeHolder
}

func newFluxRig(t *testing.T, f *fakeKube) *fluxRig {
	t.Helper()
	r := &fluxRig{h: hub.New(), holder: &kubeHolder{}}
	r.holder.cur.Store(f)
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "flux", Config: &config.FluxConfig{}})
	r.sup = plugin.NewSupervisor(plugin.Options{Publisher: r.h, Settings: set, StatusEvery: 5 * time.Millisecond})
	r.h.SetOrder(r.sup.TopicOrder)
	r.m = New(Deps{Cfg: config.Defaults(), Cluster: r.holder.get, Hosts: r.sup})
	if err := r.sup.Add(r.m.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); r.sup.Shutdown(context.Background()) })
	eventually(t, "flux to run", func() bool { return r.sup.Running("flux") })
	return r
}

// row is the flux row as core.plugins would show it now.
func (r *fluxRig) row(t *testing.T) model.PluginInfo {
	t.Helper()
	for _, pi := range r.sup.Infos() {
		if pi.ID == "flux" {
			return pi
		}
	}
	t.Fatal("no flux row")
	return model.PluginInfo{}
}

func (r *fluxRig) fluxData(t *testing.T) (model.FluxData, model.Envelope) {
	t.Helper()
	env := mustEnvelope(t, r.h, model.TopicFlux)
	var d model.FluxData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d, env
}

// When the kubernetes service returns, the flux row must not turn visible while the topic still
// holds the emptied payload of the withdrawal: the supervisor resends the stored event of a row
// that just became visible, and the GitOps page and the overview's flux KPIs would show nothing
// until the Manager's next round. The row stays hidden until the topic is built from the service
// that runs now, and what it then resends is that build.
func TestFluxRowStaysHiddenUntilBuiltForTheReturningService(t *testing.T) {
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", false))
	r := newFluxRig(t, f)

	r.m.rebuild()
	eventually(t, "the row to show", func() bool { return plugin.Visible(r.row(t).State) })

	// The service goes: the topic is emptied and the row is hidden.
	r.holder.cur.Store(nil)
	r.m.rebuild()
	if d, _ := r.fluxData(t); len(d.Kustomizations) != 0 {
		t.Fatalf("the withdrawn topic holds %+v", d)
	}
	eventually(t, "the row to hide", func() bool { return !plugin.Visible(r.row(t).State) })

	// A subscriber opens while it is hidden and follows everything from here on.
	_, ch, stop := r.h.Subscribe("", nil)
	defer stop()

	// The service returns, and the Manager has not run yet. The row must not be visible: a
	// healthy row here would resend the empty payload.
	r.holder.cur.Store(f)
	for range 20 {
		if got := r.row(t); plugin.Visible(got.State) {
			t.Fatalf("the row is %s before the Manager built flux for the returning service", got.State)
		}
		time.Sleep(5 * time.Millisecond) // a few status ticks of the supervisor: only gives a wrong row the chance to show
	}

	r.m.rebuild()
	eventually(t, "the row to show again", func() bool { return plugin.Visible(r.row(t).State) })

	// What reached the subscriber after the row turned visible is the build, never the empty topic.
	deadline := time.After(5 * time.Second)
	visible := false
	for {
		select {
		case ev := <-ch:
			switch ev.Topic {
			case string(model.TopicPlugins):
				var env model.Envelope
				var pd model.PluginsData
				if json.Unmarshal(ev.Data, &env) == nil && json.Unmarshal(env.Data, &pd) == nil {
					for _, pi := range pd.Plugins {
						if pi.ID == "flux" && plugin.Visible(pi.State) {
							visible = true
						}
					}
				}
			case string(model.TopicFlux):
				var env model.Envelope
				var d model.FluxData
				if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
					t.Fatalf("flux event %s", ev.Data)
				}
				if len(d.Kustomizations) == 0 {
					t.Fatalf("the empty flux payload reached a subscriber (row visible: %v)", visible)
				}
				if visible {
					return
				}
			}
		case <-deadline:
			t.Fatalf("no flux event after the row turned visible (visible seen: %v)", visible)
		}
	}
}

// A reload of the kubernetes plugin replaces its service: the topic holds a build of the old one,
// and the row is hidden until the Manager has built it again from the new one, whatever the new
// service's sync says meanwhile.
func TestFluxShimWaitsForABuildOfTheReloadedService(t *testing.T) {
	old := newFakeKube()
	old.add(kube.GVRKustomizations, kustomization("apps", true))
	holder := &kubeHolder{}
	holder.cur.Store(old)
	m := New(Deps{Cfg: config.Defaults(), Cluster: holder.get, Hosts: plugintest.AllRunning(hub.New(), LegacyManifests()...)})
	m.rebuild()
	if got := m.legacyStatus(); got.State != plugin.StateHealthy {
		t.Fatalf("with the first service built: %s", got.State)
	}

	next := newFakeKube()
	next.add(kube.GVRKustomizations, kustomization("apps", true))
	next.set(func(f *fakeKube) { f.synced = false })
	holder.cur.Store(next)
	for _, step := range []struct {
		what   string
		synced bool
	}{{"unsynced", false}, {"synced, not built", true}} {
		next.set(func(f *fakeKube) { f.synced = step.synced })
		if got := m.legacyStatus(); plugin.Visible(got.State) {
			t.Errorf("%s: the row is %s while the topic holds a build of the replaced service", step.what, got.State)
		}
	}
	m.rebuild()
	if got := m.legacyStatus(); got.State != plugin.StateHealthy {
		t.Errorf("after the build for the new service: %s %q", got.State, got.Message)
	}
}

// A failed discovery of a flux API group hides that group's objects although the cluster has
// them, and Listed counts the resource as listed: the row says so, and the topic carries an
// unreachable flux source for as long as it lasts. A group that is not a flux group changes
// nothing.
func TestFailedDiscoveryOfAFluxGroupDegradesTheRowAndNotesTheTopic(t *testing.T) {
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", true))
	r := newFluxRig(t, f)
	r.m.rebuild()
	eventually(t, "healthy", func() bool { return r.row(t).State == model.PluginHealthy })

	f.set(func(f *fakeKube) {
		f.failed = []schema.GroupVersion{{Group: "metrics.k8s.io", Version: "v1beta1"}}
	})
	r.m.rebuild()
	if got := r.row(t); got.State != model.PluginHealthy {
		t.Errorf("a failed group that flux does not read: row %s %q", got.State, got.Message)
	}

	f.set(func(f *fakeKube) {
		f.failed = []schema.GroupVersion{kube.GVRKustomizations.GroupVersion(), kube.GVRHelmReleases.GroupVersion()}
	})
	r.m.rebuild()
	got := r.row(t)
	if got.State != model.PluginDegraded {
		t.Fatalf("row = %s %q, want degraded", got.State, got.Message)
	}
	want := "discovery failed for: helm.toolkit.fluxcd.io, kustomize.toolkit.fluxcd.io"
	if got.Message != want {
		t.Errorf("message = %q, want %q", got.Message, want)
	}
	_, env := r.fluxData(t)
	if s, ok := sourceNamed(env.Sources, "flux"); !ok || s.OK || s.Error != "unreachable" {
		t.Errorf("flux sources = %+v, want an unreachable flux note", env.Sources)
	}

	f.set(func(f *fakeKube) { f.failed = nil })
	r.m.rebuild()
	if got := r.row(t); got.State != model.PluginHealthy {
		t.Errorf("after the groups recovered: row %s %q", got.State, got.Message)
	}
	_, env = r.fluxData(t)
	if _, ok := sourceNamed(env.Sources, "flux"); ok {
		t.Errorf("flux sources = %+v, want no flux note once discovery works", env.Sources)
	}
}
