package kubernetes

import (
	"reflect"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// ovPlugin is a running plugin whose last published payloads are cl and st (either may be nil).
func ovPlugin(cl *model.ClusterData, st *model.StorageData) *Plugin {
	p := &Plugin{}
	p.rt.Store(&runtime{cluster: cl, storageD: st})
	return p
}

func ovKPIs(ks []model.KPI) map[string]model.KPI {
	out := map[string]model.KPI{}
	for _, k := range ks {
		out[k.Key] = k
	}
	return out
}

func TestAttentionIsNilWhileThePluginDoesNotRun(t *testing.T) {
	var p Plugin
	if items := p.Attention(clNow); items != nil {
		t.Errorf("items = %+v", items)
	}
	if ks := p.KPIs(); ks != nil {
		t.Errorf("kpis = %+v", ks)
	}
	if topo := p.Topology(); topo.VMs == nil || topo.Groups == nil || len(topo.Groups) != 0 {
		t.Errorf("topology = %+v, want empty non-nil lists", topo)
	}
}

func TestAttentionBeforeTheFirstPublishIsEmpty(t *testing.T) {
	items := ovPlugin(nil, nil).Attention(clNow)
	if len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
}

func TestAttentionStampsFirstSeenOnItemsWithoutATime(t *testing.T) {
	created := clNow.Add(-time.Hour)
	cl := &model.ClusterData{
		Nodes: []model.Node{{Name: "cp-1", Ready: false}},
		Pods:  []model.Pod{{Namespace: "a", Name: "p", Health: model.HealthCrit, CreatedAt: created}},
	}
	ratio := 0.85
	st := &model.StorageData{PVCs: []model.PVC{{Namespace: "a", Name: "v", UsedRatio: &ratio}}}
	p := ovPlugin(cl, st)
	r := p.rt.Load()

	first := p.Attention(clNow)
	if len(first) != 3 {
		t.Fatalf("items = %+v", first)
	}
	since := func(items []model.AttentionItem, id string) time.Time {
		it, ok := atItem(items, id)
		if !ok {
			t.Fatalf("no item %s in %+v", id, items)
		}
		return it.Since
	}
	if got := since(first, "node:cp-1"); !got.Equal(clNow) {
		t.Errorf("node since = %v, want the first sighting %v", got, clNow)
	}
	if got := since(first, "pod:a/p"); !got.Equal(created) {
		t.Errorf("pod since = %v, want its creation %v", got, created)
	}

	later := clNow.Add(10 * time.Minute)
	second := p.Attention(later)
	if got := since(second, "node:cp-1"); !got.Equal(clNow) {
		t.Errorf("node since after another call = %v, want it to stay %v", got, clNow)
	}
	if got := since(second, "pvc:a/v"); !got.Equal(clNow) {
		t.Errorf("pvc since = %v, want %v", got, clNow)
	}

	// The node recovers and fails again: it starts over at the time it is seen again.
	r.mu.Lock()
	r.cluster = &model.ClusterData{}
	r.mu.Unlock()
	p.Attention(later.Add(time.Minute))
	r.mu.Lock()
	r.cluster = cl
	r.mu.Unlock()
	again := later.Add(2 * time.Minute)
	if got := since(p.Attention(again), "node:cp-1"); !got.Equal(again) {
		t.Errorf("node since after recovering = %v, want %v", got, again)
	}
}

func TestKPIsOfNodesAndTheFullestPVC(t *testing.T) {
	ratio := func(v float64) *float64 { return &v }
	cl := &model.ClusterData{Nodes: []model.Node{{Name: "a", Ready: true}, {Name: "b", Ready: true}, {Name: "c", Ready: false}}}
	st := &model.StorageData{PVCs: []model.PVC{
		{Namespace: "x", Name: "small", UsedRatio: ratio(0.2)},
		{Namespace: "media", Name: "library", UsedRatio: ratio(0.86)},
		{Namespace: "x", Name: "unmeasured"},
	}}
	got := ovKPIs(ovPlugin(cl, st).KPIs())
	want := map[string]model.KPI{
		"nodes.ready": {Key: "nodes.ready", Plugin: "kubernetes", Label: "Nodes ready", Value: 2, Health: model.HealthCrit},
		"nodes.total": {Key: "nodes.total", Plugin: "kubernetes", Label: "Nodes", Value: 3, Health: model.HealthOK},
		"pvc.worst":   {Key: "pvc.worst", Plugin: "kubernetes", Label: "media/library", Value: 0.86, Health: model.HealthWarn},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kpis = %+v\nwant   %+v", got, want)
	}
}

func TestKPIsWithEveryNodeReadyAndNoPVCRatio(t *testing.T) {
	cl := &model.ClusterData{Nodes: []model.Node{{Name: "a", Ready: true}}}
	st := &model.StorageData{PVCs: []model.PVC{{Namespace: "x", Name: "unmeasured"}}}
	got := ovKPIs(ovPlugin(cl, st).KPIs())
	if len(got) != 2 || got["nodes.ready"].Health != model.HealthOK || got["nodes.ready"].Value != 1 {
		t.Errorf("kpis = %+v, want the two node KPIs, ok, and no pvc.worst while no PVC has a ratio", got)
	}
	if k := ovKPIs(ovPlugin(nil, nil).KPIs()); len(k) != 0 {
		t.Errorf("kpis before the first publish = %+v", k)
	}
}

func TestKPIsPVCHealthFollowsTheRatioThresholds(t *testing.T) {
	for _, tc := range []struct {
		ratio float64
		want  model.Health
	}{{0.79, model.HealthOK}, {0.80, model.HealthWarn}, {0.89, model.HealthWarn}, {0.90, model.HealthCrit}} {
		r := tc.ratio
		st := &model.StorageData{PVCs: []model.PVC{{Namespace: "a", Name: "v", UsedRatio: &r}}}
		if got := ovKPIs(ovPlugin(nil, st).KPIs())["pvc.worst"]; got.Health != tc.want || got.Value != tc.ratio {
			t.Errorf("ratio %v: %+v, want health %s", tc.ratio, got, tc.want)
		}
	}
}

func TestTopologyGroupsNamespacesAndPodsByNode(t *testing.T) {
	cl := &model.ClusterData{
		Namespaces: []model.Namespace{
			{Name: "monitoring", Group: model.GroupPlatform, Health: model.HealthOK, PodsTotal: 2},
			{Name: "flux-system", Group: model.GroupPlatform, Health: model.HealthWarn, PodsTotal: 1},
			{Name: "app-home", Group: model.GroupApps, Health: model.HealthOK, PodsTotal: 1},
			{Name: "acme", Group: model.GroupTenants, Health: model.HealthCrit, PodsTotal: 1},
		},
		Pods: []model.Pod{
			{Namespace: "monitoring", Node: "cp-1", Phase: "Running"},
			{Namespace: "monitoring", Node: "cp-1", Phase: "Running"},
			{Namespace: "flux-system", Node: "cp-2", Phase: "Pending"},
			{Namespace: "app-home", Node: "cp-2", Phase: "Running"},
			{Namespace: "acme", Node: "cp-1", Phase: "Running"},
			{Namespace: "acme", Node: "cp-2", Phase: "Succeeded"},
			{Namespace: "acme", Node: "cp-2", Phase: "Failed"},
			{Namespace: "acme", Node: "", Phase: "Pending"},
			{Namespace: "deleted", Node: "cp-1", Phase: "Running"},
		},
	}
	topo := ovPlugin(cl, nil).Topology()
	want := model.Topology{
		VMs: []model.TopologyVM{},
		Groups: []model.TopologyGroup{
			{Name: model.GroupPlatform, Health: model.HealthWarn, PodsByNode: map[string]int{"cp-1": 2, "cp-2": 1}, Namespaces: []model.TopologyNamespace{
				{Name: "flux-system", Health: model.HealthWarn, Pods: 1}, {Name: "monitoring", Health: model.HealthOK, Pods: 2},
			}},
			{Name: model.GroupApps, Health: model.HealthOK, PodsByNode: map[string]int{"cp-2": 1}, Namespaces: []model.TopologyNamespace{
				{Name: "app-home", Health: model.HealthOK, Pods: 1},
			}},
			{Name: model.GroupTenants, Health: model.HealthCrit, PodsByNode: map[string]int{"cp-1": 1}, Namespaces: []model.TopologyNamespace{
				{Name: "acme", Health: model.HealthCrit, Pods: 1},
			}},
		},
	}
	if !reflect.DeepEqual(topo, want) {
		t.Errorf("topology = %+v\nwant       %+v", topo, want)
	}
}

func TestTopologyOfAnEmptyClusterHasTheThreeGroups(t *testing.T) {
	topo := topologyOf(model.ClusterData{})
	if len(topo.Groups) != 3 || topo.Groups[0].Name != model.GroupPlatform || topo.Groups[1].Name != model.GroupApps || topo.Groups[2].Name != model.GroupTenants {
		t.Fatalf("groups = %+v", topo.Groups)
	}
	for _, g := range topo.Groups {
		if g.Health != model.HealthOK || g.Namespaces == nil || len(g.Namespaces) != 0 || g.PodsByNode == nil || len(g.PodsByNode) != 0 {
			t.Errorf("group %s = %+v, want ok with empty non-nil members", g.Name, g)
		}
	}
	if topo.VMs == nil || topo.Host != (model.TopologyHost{}) {
		t.Errorf("vms = %#v, host = %+v: the plugin has neither", topo.VMs, topo.Host)
	}
}
