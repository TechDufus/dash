package topics

import (
	"encoding/json"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// outageRig is a Manager over a kubernetes service the test removes, with a kubernetes plugin that
// keeps contributing (it runs while unavailable: its Start succeeded) and another plugin.
type outageRig struct {
	m       *Manager
	h       *hub.Hub
	holder  *kubeHolder
	cluster *fakeKube
	kube    *source
	other   *source
}

func newOutageRig(t *testing.T) *outageRig {
	t.Helper()
	r := &outageRig{
		h:       hub.New(),
		holder:  &kubeHolder{},
		cluster: newFakeKube(),
		kube: &source{
			items: []model.AttentionItem{{ID: "node:cp-2", Severity: model.SeverityCrit, Source: "kubernetes", Title: "Node cp-2 not ready"}},
			kpis:  []model.KPI{{Key: "nodes.ready", Plugin: "kubernetes", Label: "Nodes ready", Value: 1}},
			topo:  &model.Topology{Groups: []model.TopologyGroup{{Name: model.GroupApps}}},
		},
		other: &source{items: []model.AttentionItem{{ID: "other:1", Severity: model.SeverityWarn, Source: "other", Title: "other"}}},
	}
	r.cluster.add(kube.GVRKustomizations, kustomization("apps", false))
	r.holder.cur.Store(r.cluster)
	// What the plugin's own Status says while it is unavailable: its informer source, unreachable.
	kubeSources := func() []model.SourceStatus {
		if _, up := r.holder.get(); up {
			return []model.SourceStatus{{Name: "kubernetes", OK: true}}
		}
		return []model.SourceStatus{{Name: "kubernetes", Error: "unreachable"}}
	}
	r.m = New(Deps{
		Cfg: config.Defaults(), Cluster: r.holder.get, Hosts: plugintest.AllRunning(r.h, LegacyManifests()...),
		Contributors: func() []plugin.Contributor {
			return []plugin.Contributor{
				{ID: "kubernetes", Attention: r.kube, KPIs: r.kube, Topology: r.kube, Sources: kubeSources},
				{ID: "other", Attention: r.other},
			}
		},
	})
	return r
}

func (r *outageRig) overview(t *testing.T) model.OverviewData {
	t.Helper()
	r.m.rebuild()
	return overviewOf(t, r.h)
}

func hasItem(ov model.OverviewData, id string) bool {
	for _, it := range ov.Attention {
		if it.ID == id {
			return true
		}
	}
	return false
}

func kpiKeys(ov model.OverviewData) []string {
	var out []string
	for _, k := range ov.KPIList {
		out = append(out, k.Plugin+"/"+k.Key)
	}
	return out
}

// While the kubernetes service is withdrawn (the plugin is unavailable, hidden, and keeps running)
// the overview says why through the plugin's own source, not the Manager's "not_configured", and
// carries nothing of the plugin's: its last items, KPIs and topology describe a cluster nobody can
// reach. Every other plugin is untouched, and the plugin's contribution returns with the service.
func TestOverviewDuringAKubernetesOutage(t *testing.T) {
	r := newOutageRig(t)

	ov := r.overview(t)
	if !hasItem(ov, "node:cp-2") || !hasItem(ov, "other:1") {
		t.Fatalf("with the service the overview carries the plugin's items: %s", ids(ov.Attention))
	}
	if s, _ := sourceNamed(mustEnvelope(t, r.h, model.TopicOverview).Sources, "kubernetes"); !s.OK {
		t.Errorf("kubernetes source with the service = %+v", s)
	}

	r.holder.cur.Store(nil)
	ov = r.overview(t)
	if hasItem(ov, "node:cp-2") {
		t.Errorf("the hidden plugin's item stayed on the overview: %s", ids(ov.Attention))
	}
	if !hasItem(ov, "other:1") {
		t.Errorf("another plugin's item went with it: %s", ids(ov.Attention))
	}
	for _, k := range kpiKeys(ov) {
		if k == "kubernetes/nodes.ready" {
			t.Errorf("the hidden plugin's KPI stayed: %v", kpiKeys(ov))
		}
	}
	if len(ov.Topology.Groups) != 0 {
		t.Errorf("the hidden plugin's topology stayed: %+v", ov.Topology.Groups)
	}
	srcs := mustEnvelope(t, r.h, model.TopicOverview).Sources
	if s, found := sourceNamed(srcs, "kubernetes"); !found || s.OK || s.Error != "unreachable" {
		t.Errorf("kubernetes source = %+v, want the plugin's own unreachable", s)
	}
	n := 0
	for _, s := range srcs {
		if s.Name == "kubernetes" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d kubernetes sources, want one: %+v", n, srcs)
	}

	r.holder.cur.Store(r.cluster)
	if ov = r.overview(t); !hasItem(ov, "node:cp-2") {
		t.Errorf("the plugin's item did not return with the service: %s", ids(ov.Attention))
	}
}

// A kubernetes plugin that reports no source of its own (no cluster at all: it has nothing to
// say) leaves the Manager's "not_configured" chip.
func TestOverviewWithoutAKubernetesPluginSourceSaysNotConfigured(t *testing.T) {
	r := newOutageRig(t)
	r.m.d.Contributors = func() []plugin.Contributor { return nil }
	r.m.rebuild()
	r.holder.cur.Store(nil)
	r.m.rebuild()
	s, found := sourceNamed(mustEnvelope(t, r.h, model.TopicOverview).Sources, "kubernetes")
	if !found || s.OK || s.Error != "not_configured" {
		t.Errorf("kubernetes source = %+v, want not_configured", s)
	}
}

// Flux is unavailable while kubernetes is: after the service is withdrawn the overview carries
// no flux KPI and no flux attention item, and the flux topic no longer holds the last list (which
// a subscriber opening after the row turns visible would be handed); when the service returns
// both are built again.
func TestWithdrawnKubernetesTakesFluxOffTheOverviewAndTheTopic(t *testing.T) {
	r := newOutageRig(t)
	ov := r.overview(t)
	if ov.Kpis.FluxTotal != 1 || !hasItem(ov, "flux:Kustomization/flux-system/apps") {
		t.Fatalf("with the service: flux total %d, items %s", ov.Kpis.FluxTotal, ids(ov.Attention))
	}
	var d model.FluxData
	if err := json.Unmarshal(mustEnvelope(t, r.h, model.TopicFlux).Data, &d); err != nil || len(d.Kustomizations) != 1 {
		t.Fatalf("flux topic = %+v, %v", d, err)
	}

	r.holder.cur.Store(nil)
	ov = r.overview(t)
	if ov.Kpis.FluxTotal != 0 || ov.Kpis.FluxReady != 0 || hasItem(ov, "flux:Kustomization/flux-system/apps") {
		t.Errorf("flux stayed on the overview: kpis %+v, items %s", ov.Kpis, ids(ov.Attention))
	}
	env := mustEnvelope(t, r.h, model.TopicFlux)
	d = model.FluxData{}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Kustomizations)+len(d.Sources)+len(d.HelmReleases) != 0 {
		t.Errorf("the flux topic kept the last list: %+v", d)
	}
	if s, found := sourceNamed(env.Sources, "kubernetes"); !found || s.OK {
		t.Errorf("flux sources = %+v, want a failing kubernetes", env.Sources)
	}

	r.holder.cur.Store(r.cluster)
	ov = r.overview(t)
	if ov.Kpis.FluxTotal != 1 {
		t.Errorf("flux did not return with the service: %+v", ov.Kpis)
	}
	d = model.FluxData{}
	if err := json.Unmarshal(mustEnvelope(t, r.h, model.TopicFlux).Data, &d); err != nil || len(d.Kustomizations) != 1 {
		t.Errorf("flux topic after the return = %+v, %v", d, err)
	}
}

// The flux shim is starting, not healthy, from the informers' sync until each flux resource has
// listed: its topic is withheld until then, so a healthy row would show an empty page.
func TestLegacyStatusStartsUntilFluxHasListed(t *testing.T) {
	f := newFakeKube()
	f.set(func(f *fakeKube) {
		for _, gvr := range fluxGVRs {
			f.pending[gvr] = true
		}
	})
	holder := &kubeHolder{}
	holder.cur.Store(f)
	m := New(Deps{Cfg: config.Defaults(), Cluster: holder.get})
	if got := m.legacyStatus(); got.State != plugin.StateStarting {
		t.Fatalf("synced but unlisted: %s %q, want starting", got.State, got.Message)
	}
	f.set(func(f *fakeKube) {
		for gvr := range f.pending {
			if gvr != kube.GVRHelmReleases {
				delete(f.pending, gvr)
			}
		}
	})
	if got := m.legacyStatus(); got.State != plugin.StateStarting {
		t.Fatalf("one resource still unlisted: %s, want starting", got.State)
	}
	f.set(func(f *fakeKube) { delete(f.pending, kube.GVRHelmReleases) })
	if got := m.legacyStatus(); got.State != plugin.StateHealthy {
		t.Errorf("everything listed: %s %q, want healthy", got.State, got.Message)
	}
}
