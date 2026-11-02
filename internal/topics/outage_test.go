package topics

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// outageRig is a Manager over a kubernetes plugin that keeps running while its cluster is
// unreachable (its Start succeeded) and another plugin. A plugin that is unavailable has no
// opinion: its contributor returns nothing while it is, and says why through its own source.
type outageRig struct {
	m     *Manager
	h     *hub.Hub
	up    bool
	kube  *source
	other *source
}

// withdrawn wraps a source whose contributions go while the rig is down, as the kubernetes
// plugin's do while its service is withdrawn.
type withdrawn struct {
	r *outageRig
	s *source
}

func (w withdrawn) Attention(now time.Time) []model.AttentionItem {
	if !w.r.up {
		return nil
	}
	return w.s.Attention(now)
}

func (w withdrawn) KPIs() []model.KPI {
	if !w.r.up {
		return nil
	}
	return w.s.KPIs()
}

func (w withdrawn) Topology() model.Topology {
	if !w.r.up {
		return model.Topology{}
	}
	return w.s.Topology()
}

func newOutageRig(t *testing.T) *outageRig {
	t.Helper()
	r := &outageRig{
		h:  hub.New(),
		up: true,
		kube: &source{
			items: []model.AttentionItem{{ID: "node:cp-2", Severity: model.SeverityCrit, Source: "kubernetes", Title: "Node cp-2 not ready"}},
			kpis:  []model.KPI{{Key: "nodes.ready", Plugin: "kubernetes", Label: "Nodes ready", Value: 1}},
			topo:  &model.Topology{Groups: []model.TopologyGroup{{Name: model.GroupApps}}},
		},
		other: &source{items: []model.AttentionItem{{ID: "other:1", Severity: model.SeverityWarn, Source: "other", Title: "other"}}},
	}
	// What the plugin's own Status says while it is unavailable: its informer source, unreachable.
	kubeSources := func() []model.SourceStatus {
		if r.up {
			return []model.SourceStatus{{Name: "kubernetes", OK: true}}
		}
		return []model.SourceStatus{{Name: "kubernetes", Error: "unreachable"}}
	}
	w := withdrawn{r, r.kube}
	r.m = New(Deps{
		Hosts: plugintest.AllRunning(r.h),
		Contributors: func() []plugin.Contributor {
			return []plugin.Contributor{
				{ID: "kubernetes", Attention: w, KPIs: w, Topology: w, Sources: kubeSources},
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
// the overview says why through the plugin's own source, and carries nothing of the plugin's: it
// contributes nothing then, because its last items, KPIs and topology describe a cluster nobody
// can reach. Every other plugin is untouched, and the plugin's contribution returns with the
// service.
func TestOverviewDuringAKubernetesOutage(t *testing.T) {
	r := newOutageRig(t)

	ov := r.overview(t)
	if !hasItem(ov, "node:cp-2") || !hasItem(ov, "other:1") {
		t.Fatalf("with the service the overview carries the plugin's items: %s", ids(ov.Attention))
	}
	if s, _ := sourceNamed(mustEnvelope(t, r.h, model.TopicOverview).Sources, "kubernetes"); !s.OK {
		t.Errorf("kubernetes source with the service = %+v", s)
	}

	r.up = false
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

	r.up = true
	if ov = r.overview(t); !hasItem(ov, "node:cp-2") {
		t.Errorf("the plugin's item did not return with the service: %s", ids(ov.Attention))
	}
}

// A plugin that does not run has no contributor, and so no chip: the overview has no
// "kubernetes: not_configured" of its own for a cluster nobody configured.
func TestOverviewWithoutAKubernetesPluginHasNoKubernetesSource(t *testing.T) {
	r := newOutageRig(t)
	r.m.d.Contributors = func() []plugin.Contributor {
		return []plugin.Contributor{{ID: "other", Attention: r.other}}
	}
	r.m.rebuild()
	if s, found := sourceNamed(mustEnvelope(t, r.h, model.TopicOverview).Sources, "kubernetes"); found {
		t.Errorf("kubernetes source = %+v, want none", s)
	}
}
