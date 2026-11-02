package kubernetes

import (
	"errors"
	"reflect"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// ovPlugin is a running plugin whose last published payloads are cl and st (either may be nil).
// It runs as a twin, whose row is always healthy: the service is never withdrawn.
func ovPlugin(cl *model.ClusterData, st *model.StorageData) *Plugin {
	p := &Plugin{}
	p.rt.Store(&runtime{cluster: cl, storageD: st, twin: &twinSource{}})
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

// No opinion (nil, which leaves the supervisor's sightings alone) until both payloads exist: a
// half-built list would prune the sightings of the other half.
func TestAttentionIsNilUntilBothPayloadsExist(t *testing.T) {
	cl, st := &model.ClusterData{Nodes: []model.Node{{Name: "cp-1"}}}, &model.StorageData{}
	for name, p := range map[string]*Plugin{
		"neither":      ovPlugin(nil, nil),
		"cluster only": ovPlugin(cl, nil),
		"storage only": ovPlugin(nil, st),
	} {
		if items := p.Attention(clNow); items != nil {
			t.Errorf("%s: items = %+v, want nil", name, items)
		}
	}
	if items := ovPlugin(cl, st).Attention(clNow); len(items) != 1 || items[0].Since != (time.Time{}) {
		t.Errorf("both payloads: items = %+v, want the node, unstamped (the supervisor stamps)", items)
	}
	if items := ovPlugin(&model.ClusterData{}, st).Attention(clNow); items == nil || len(items) != 0 {
		t.Errorf("nothing to raise: items = %#v, want a non-nil empty list", items)
	}
}

// An item without a time of its own gets the time the supervisor first saw it, and keeps it
// when the plugin restarts (N5).
func TestAttentionIsStampedBySupervisorAndSurvivesARestart(t *testing.T) {
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{node("cp-1", "192.0.2.10", false)}})
	e.healthy()
	at := func(now time.Time) []model.AttentionItem {
		for _, c := range e.sup.Contributors() {
			if c.ID == ID && c.Attention != nil {
				return c.Attention.Attention(now)
			}
		}
		t.Fatal("kubernetes is not an attention contributor")
		return nil
	}
	since := func(now time.Time) time.Time {
		items := at(now)
		if len(items) != 1 || items[0].ID != "node:cp-1" {
			t.Fatalf("items = %+v, want the node", items)
		}
		return items[0].Since
	}
	if got := since(clNow); !got.Equal(clNow) {
		t.Fatalf("node since = %v, want the first sighting %v", got, clNow)
	}
	if got := since(clNow.Add(10 * time.Minute)); !got.Equal(clNow) {
		t.Errorf("node since after another call = %v, want it to stay %v", got, clNow)
	}

	// A restart: a changed setting stops the plugin and starts it again.
	cfg := *e.cfg
	cfg.Actions.DenyNamespaces = []string{"kube-system"}
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg, DenyNamespaces: cfg.Actions.DenyNamespaces})
	e.sup.Reload()
	waitFor(t, "the plugin to restart", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.connections == 2
	})
	// Until the new runtime has built both payloads it has no opinion, and nothing is pruned.
	var got time.Time
	later := clNow.Add(time.Hour)
	waitFor(t, "the restarted plugin's attention", func() bool {
		items := at(later)
		if len(items) == 1 {
			got = items[0].Since
		}
		return len(items) == 1
	})
	if !got.Equal(clNow) {
		t.Errorf("node since after the restart = %v, want %v", got, clNow)
	}
}

// A cluster that has published and has nothing to raise says so with a non-nil empty list; nil
// would leave the supervisor's first sightings of items that are gone in place.
func TestAttentionOfAHealthyClusterIsNonNilEmpty(t *testing.T) {
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{node("cp-1", "192.0.2.10", true)}})
	e.healthy()
	for _, c := range e.sup.Contributors() {
		if c.ID == ID {
			plugintest.AssertAttentionNonNil(t, c)
			if items := c.Attention.Attention(clNow); len(items) != 0 {
				t.Errorf("items = %+v, want none", items)
			}
			return
		}
	}
	t.Fatal("kubernetes is not an attention contributor")
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

// kubeContributor is the plugin's contributor as the overview sees it.
func kubeContributor(t *testing.T, e *env) plugin.Contributor {
	t.Helper()
	for _, c := range e.sup.Contributors() {
		if c.ID == ID {
			return c
		}
	}
	t.Fatal("the plugin has no contributor")
	return plugin.Contributor{}
}

// An errored kubernetes row is visible, so the supervisor keeps its contributor; the plugin itself
// has no opinion while its service is withdrawn, and has one again when the sync completes.
func TestAttentionAndKPIsAreNilWhileTheServiceIsWithdrawn(t *testing.T) {
	var forbid atomicBool
	forbid.set(true)
	e := newEnv(t, envOpts{fakeClock: true, objs: []k8sruntime.Object{node("cp-1", "192.0.2.10", false)}, prefill: func(e *env) {
		e.kc.PrependReactor("list", "pods", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
			if forbid.get() {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
			}
			return false, nil, nil
		})
	}})
	e.clock.advance(60 * time.Second)
	e.state(plugin.StateError)

	c := kubeContributor(t, e)
	if c.Sources == nil || len(c.Sources()) == 0 {
		t.Error("the errored plugin lost its sources")
	}
	if c.Attention == nil || c.KPIs == nil || c.Topology == nil {
		t.Fatalf("the visible errored plugin lost a source: attention %v, kpis %v, topology %v", c.Attention != nil, c.KPIs != nil, c.Topology != nil)
	}
	if items := c.Attention.Attention(clNow); items != nil {
		t.Errorf("attention while the service is withdrawn: %+v", items)
	}
	if ks := c.KPIs.KPIs(); ks != nil {
		t.Errorf("KPIs while the service is withdrawn: %+v", ks)
	}
	if topo := c.Topology.Topology(); topo.VMs == nil || topo.Groups == nil || len(topo.Groups) != 0 {
		t.Errorf("topology while the service is withdrawn = %+v, want empty non-nil lists", topo)
	}

	forbid.set(false)
	e.healthy()
	c = kubeContributor(t, e)
	waitFor(t, "the nodes, once the sync completes", func() bool {
		return c.Attention != nil && len(c.Attention.Attention(clNow)) == 1 && len(c.KPIs.KPIs()) > 0 && len(c.Topology.Topology().Groups) == 3
	})
}

// What the plugin built is not shown while its service is withdrawn: the payloads exist (the
// plugin is healthy, then the probe is cut past the debounce), and still Attention, KPIs and
// Topology have no opinion until the API server answers again.
func TestOverviewSourcesHaveNoOpinionOnBuiltPayloadsWhileUnavailable(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true, objs: []k8sruntime.Object{node("cp-1", "192.0.2.10", false)}})
	e.healthy()
	p := e.plugin()
	waitFor(t, "the built payloads to give items", func() bool {
		return len(p.Attention(clNow)) == 1 && len(p.KPIs()) > 0 && len(p.Topology().Groups) == 3
	})

	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(10 * time.Second)
	e.state(plugin.StateUnavailable)
	if items := p.Attention(clNow); items != nil {
		t.Errorf("attention while unavailable: %+v", items)
	}
	if ks := p.KPIs(); ks != nil {
		t.Errorf("KPIs while unavailable: %+v", ks)
	}
	if topo := p.Topology(); topo.VMs == nil || topo.Groups == nil || len(topo.Groups) != 0 {
		t.Errorf("topology while unavailable = %+v, want empty non-nil lists", topo)
	}

	e.failProbe.Store(false)
	e.state(plugin.StateHealthy)
	waitFor(t, "the built payloads to be shown again", func() bool {
		return len(p.Attention(clNow)) == 1 && len(p.KPIs()) > 0 && len(p.Topology().Groups) == 3
	})
}
