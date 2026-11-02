package kubernetes

import (
	"errors"
	"strings"
	"testing"
	"time"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/svc"
)

func usageEnv(t *testing.T, mutate func(*env)) *env {
	t.Helper()
	return newEnv(t, envOpts{
		objs: []k8sruntime.Object{node("k8s-cp-1", "192.0.2.11", true), namespace("media", nil), runningPod("media", "app-0", "k8s-cp-1")},
		prefill: func(e *env) {
			e.prom.samples[queryNodeCPU] = []svc.Sample{sample(0.25, "instance", "192.0.2.11:9100")}
			e.prom.samples[queryNodeMem] = []svc.Sample{sample(0.5, "instance", "192.0.2.11:9100")}
			e.prom.samples[queryPodCPU] = []svc.Sample{sample(0.125, "namespace", "media", "pod", "app-0")}
			e.prom.samples[queryPodMem] = []svc.Sample{sample(1<<20, "namespace", "media", "pod", "app-0")}
			if mutate != nil {
				mutate(e)
			}
		},
	})
}

// The usage polls reach the cluster payload: node ratios by instance, pod usage by namespace and
// name.
func TestUsageReachesTheClusterTopic(t *testing.T) {
	e := usageEnv(t, nil)
	e.healthy()
	var cl model.ClusterData
	waitFor(t, "the usage in the payload", func() bool {
		e.payload("kubernetes.cluster", &cl)
		return len(cl.Nodes) == 1 && cl.Nodes[0].CPURatio == 0.25 && len(cl.Pods) == 1 && cl.Pods[0].CPUCores != nil
	})
	if cl.Nodes[0].MemRatio != 0.5 || *cl.Pods[0].CPUCores != 0.125 || cl.Pods[0].MemBytes == nil || *cl.Pods[0].MemBytes != 1<<20 {
		t.Errorf("usage = %+v / %+v", cl.Nodes[0], cl.Pods[0])
	}
}

// A Prometheus failure is a source note, never a degraded row; the old values stay, and the
// failure is logged once per change of category, not once per poll.
func TestPrometheusFailureIsASourceNote(t *testing.T) {
	e := usageEnv(t, nil)
	e.healthy()
	waitFor(t, "the first usage", func() bool {
		var cl model.ClusterData
		e.payload("kubernetes.cluster", &cl)
		return len(cl.Nodes) == 1 && cl.Nodes[0].CPURatio == 0.25
	})
	e.prom.mu.Lock()
	e.prom.errs[queryNodeCPU] = errors.New("dial tcp 192.0.2.50:9090: connection refused")
	e.prom.mu.Unlock()
	waitFor(t, "the prometheus source to fail", func() bool {
		srcs, _ := e.payload("kubernetes.cluster", &model.ClusterData{})
		for _, s := range srcs {
			if s.Name == sourcePrometheus && !s.OK && s.Error == "unreachable" {
				return true
			}
		}
		return false
	})
	if st := e.info().State; st != plugin.StateHealthy {
		t.Errorf("a prometheus failure made the row %s", st)
	}
	var cl model.ClusterData
	srcs, _ := e.payload("kubernetes.cluster", &cl)
	for _, s := range srcs {
		if strings.Contains(s.Error, "192.0.2.50") {
			t.Errorf("source error %q leaks the upstream address", s.Error)
		}
	}
	if cl.Nodes[0].CPURatio != 0.25 {
		t.Errorf("the last good node usage was dropped: %v", cl.Nodes[0].CPURatio)
	}
	waitFor(t, "several failed polls", func() bool { return e.prom.requests() > 12 })
	if n := strings.Count(e.logs.String(), `msg="upstream source failed"`); n != 1 {
		t.Errorf("the failure was logged %d times, want once", n)
	}
}

// A poll that ends, whatever the outcome, lets the row leave starting; without a Prometheus the
// polls end at once and make no request.
func TestRowDoesNotWaitForPrometheus(t *testing.T) {
	e := newEnv(t, envOpts{noProm: true})
	e.healthy()
	if n := e.prom.requests(); n != 0 {
		t.Errorf("%d queries", n)
	}
}

// The usage and storage polls honour the plugin's stop: nothing is left running.
func TestPollsAreSkippedWhilePrometheusIsAbsent(t *testing.T) {
	e := newEnv(t, envOpts{noProm: true})
	e.healthy()
	r := e.runtime()
	r.mu.RLock()
	u, s := r.usageOut, r.storageOut
	r.mu.RUnlock()
	if !u.polled || u.err == nil || !s.polled || s.err == nil {
		t.Errorf("outcomes = %+v, %+v", u, s)
	}
}

// A gatus poll that moves Revision republishes the apps without any cluster change; an absent
// gatus keeps its chip reading not configured; a gatus whose first poll has not ended holds the
// topic back.
func TestAppsHealthFollowsGatusRevision(t *testing.T) {
	route := obj(kube.GVRHTTPRoutes, "monitoring", "grafana")
	route.Object["spec"] = map[string]any{
		"hostnames":  []any{"grafana.example.com"},
		"parentRefs": []any{map[string]any{"name": "home", "namespace": "gateway"}},
	}
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{namespace("monitoring", nil), route}, prefill: func(e *env) { e.gatus.rev = 0 }})
	e.state(plugin.StateHealthy)
	time.Sleep(100 * time.Millisecond) // a negative: the topic has to stay unpublished for a while
	if _, ok := e.hub.Latest("", "kubernetes.apps"); ok {
		t.Fatal("apps published before gatus's first poll ended")
	}
	e.gatus.set("grafana.example.com", model.HealthOK)
	app := func() *model.App {
		var ap model.AppsData
		if _, ok := e.payload("kubernetes.apps", &ap); !ok || len(ap.Apps) != 1 {
			return nil
		}
		return &ap.Apps[0]
	}
	waitFor(t, "the app, healthy", func() bool { a := app(); return a != nil && a.Healthy != nil && *a.Healthy })
	e.gatus.set("grafana.example.com", model.HealthCrit)
	waitFor(t, "the app, unhealthy", func() bool { a := app(); return a != nil && a.Healthy != nil && !*a.Healthy })
}

func TestAppsWithoutGatusKeepTheNotConfiguredChip(t *testing.T) {
	e := newEnv(t, envOpts{noGatus: true})
	e.healthy()
	srcs, _ := e.payload("kubernetes.apps", &model.AppsData{})
	found := false
	for _, s := range srcs {
		if s.Name == "gatus" {
			found = true
			if s.OK || s.Error != "not_configured" {
				t.Errorf("gatus source = %+v", s)
			}
		}
	}
	if !found {
		t.Errorf("no gatus chip in %+v", srcs)
	}
}

// The published payloads carry unique, well-formed refs under colliding names: a Deployment and a
// StatefulSet of one name, an HTTPRoute with several hostnames, a route and a pod sharing a name.
func TestPublishedRefsAreUniqueUnderCollidingNames(t *testing.T) {
	route := obj(kube.GVRHTTPRoutes, "media", "app")
	route.Object["spec"] = map[string]any{
		"hostnames":  []any{"a.example.com", "b.example.com"},
		"parentRefs": []any{map[string]any{"name": "home", "namespace": "gateway"}},
	}
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{
		node("n1", "192.0.2.11", true), namespace("media", nil), runningPod("media", "app", "n1"),
		deployment("media", "app", 1, 1), statefulSet("media", "app", 1, 1), pvc("media", "app", 1<<30), route,
	}, prefill: func(e *env) { e.gatus.set("a.example.com", model.HealthOK) }})
	e.healthy()
	var cl model.ClusterData
	var st model.StorageData
	var ap model.AppsData
	e.payload("kubernetes.cluster", &cl)
	e.payload("kubernetes.storage", &st)
	e.payload("kubernetes.apps", &ap)
	plugintest.AssertRefs(t, e.sup, ID, cl, model.Node{}, model.Namespace{}, model.Workload{}, model.Pod{})
	plugintest.AssertRefs(t, e.sup, ID, st, model.PVC{})
	plugintest.AssertRefs(t, e.sup, ID, ap, model.App{})
	if len(ap.Apps) != 2 {
		t.Errorf("apps = %+v", ap.Apps)
	}
}
