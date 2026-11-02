package kubernetes

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/queries"
)

// demoEnv runs the twin under the real supervisor: no connection, nothing for a fake apiserver to
// see.
func demoEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, envOpts{demo: true, noProm: true, noGatus: true, connErr: errBoom})
	return e
}

var errBoom = &plugin.ActionError{Status: 500, Code: "x", Message: "the twin must not connect"}

func TestDemoRunsTheTwinAndContactsNoCluster(t *testing.T) {
	e := demoEnv(t)
	e.healthy()
	if e.requests.Load() != 0 {
		t.Errorf("%d requests reached the fake API server", e.requests.Load())
	}
	e.mu.Lock()
	n := e.connections
	e.mu.Unlock()
	if n != 0 {
		t.Errorf("the twin connected %d times", n)
	}
	var cl model.ClusterData
	var st model.StorageData
	var ap model.AppsData
	e.payload("kubernetes.cluster", &cl)
	e.payload("kubernetes.storage", &st)
	e.payload("kubernetes.apps", &ap)
	if len(cl.Nodes) == 0 || len(cl.Pods) == 0 || len(st.PVCs) == 0 || len(ap.Apps) == 0 {
		t.Fatalf("the twin published empty data: %d nodes, %d pods, %d pvcs, %d apps", len(cl.Nodes), len(cl.Pods), len(st.PVCs), len(ap.Apps))
	}
	plugintest.AssertRefs(t, e.sup, ID, cl, model.Node{}, model.Namespace{}, model.Workload{}, model.Pod{}, model.Cert{}, model.ExtSecret{})
	plugintest.AssertRefs(t, e.sup, ID, st, model.PVC{}, model.BackupJob{})
	plugintest.AssertRefs(t, e.sup, ID, ap, model.App{})
	if v, ok := e.sup.Service(ID); ok || v != nil {
		t.Errorf("the twin exports a service: %v", v)
	}
}

// The twin's overview share, series and pod detail are the real code's, on the fake world.
func TestDemoTwinServesOverviewSeriesAndPodDetail(t *testing.T) {
	e := demoEnv(t)
	e.healthy()
	var contributor *plugin.Contributor
	waitFor(t, "the contributor", func() bool {
		for _, c := range e.sup.Contributors() {
			if c.ID == ID {
				contributor = &c
			}
		}
		return contributor != nil
	})
	var kpis []model.KPI
	waitFor(t, "the KPIs", func() bool { kpis = contributor.KPIs.KPIs(); return len(kpis) > 0 })
	keys := map[string]bool{}
	for _, k := range kpis {
		keys[k.Key] = true
	}
	if !keys["nodes.ready"] || !keys["nodes.total"] || !keys["pvc.worst"] {
		t.Errorf("KPIs = %+v", kpis)
	}
	items := contributor.Attention.Attention(time.Now())
	if len(items) == 0 {
		t.Error("the demo world's crash-looping pod raised no attention item")
	}
	if topo := contributor.Topology.Topology(); len(topo.Groups) != 3 {
		t.Errorf("topology groups = %d", len(topo.Groups))
	}

	spec, _, ok := e.sup.Series("node.cpu")
	if !ok {
		t.Fatal("the twin serves no node.cpu")
	}
	if expr, err := spec.Expr(queries.Args{Instance: "k8s-cp-1"}); err != nil || expr == "" {
		t.Errorf("node.cpu = %q, %v", expr, err)
	}
	if _, err := spec.Expr(queries.Args{Instance: "nope"}); err == nil {
		t.Error("an unknown demo node resolved")
	}

	var cl model.ClusterData
	e.payload("kubernetes.cluster", &cl)
	p := cl.Pods[0]
	w := e.get("/pods/" + p.Namespace + "/" + p.Name)
	if w.Code != http.StatusOK {
		t.Fatalf("pod detail = %d %s", w.Code, w.Body)
	}
	if w := e.get("/pods/media/nope"); w.Code != http.StatusNotFound {
		t.Errorf("unknown pod = %d", w.Code)
	}
}

// The fake restart applies the typed-name rule of the real one and changes nothing.
func TestDemoRestartIsFake(t *testing.T) {
	e := demoEnv(t)
	e.healthy()
	ref, ok := e.sup.Action(ID, "restart")
	if !ok {
		t.Fatal("the twin's restart is not mounted (demo forces actions on)")
	}
	var cl model.ClusterData
	e.payload("kubernetes.cluster", &cl)
	i := slices.IndexFunc(cl.Namespaces, func(n model.Namespace) bool { return n.Protected })
	j := slices.IndexFunc(cl.Workloads, func(w model.Workload) bool { return !w.Protected && w.Restartable })
	if i < 0 || j < 0 {
		t.Fatal("the world has no protected namespace or no plain workload")
	}
	protected := cl.Namespaces[i].Name
	var wl model.Workload
	for _, w := range cl.Workloads {
		if w.Namespace == protected {
			wl = w
		}
	}
	in := func(ns, name, confirm string) any {
		x := ref.Spec.Input().(*restartInput)
		x.Kind, x.Namespace, x.Name, x.Confirm = wl.Kind, ns, name, confirm
		return x
	}
	if _, err := ref.Run(t.Context(), auth.Principal{Role: auth.RoleAdmin}, in(wl.Namespace, wl.Name, "")); err == nil {
		t.Error("a protected namespace restarted without the typed name")
	}
	if _, err := ref.Run(t.Context(), auth.Principal{Role: auth.RoleAdmin}, in(wl.Namespace, wl.Name, wl.Name)); err != nil {
		t.Errorf("restart with the typed name: %v", err)
	}
	plain := cl.Workloads[j]
	if _, err := ref.Run(t.Context(), auth.Principal{Role: auth.RoleAdmin}, in(plain.Namespace, plain.Name, "")); err != nil {
		t.Errorf("restart of an unprotected workload: %v", err)
	}
	if e.requests.Load() != 0 {
		t.Error("the fake restart reached an API server")
	}
}
