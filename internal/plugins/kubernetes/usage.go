package kubernetes

import (
	"context"
	"sort"
	"sync"

	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// The PromQL of the node and pod usage on the cluster topic.
const (
	queryPodCPU  = `sum by (namespace,pod)(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD"}[2m]))`
	queryPodMem  = `sum by (namespace,pod)(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD"})`
	queryNodeCPU = `1 - avg by (instance)(rate(node_cpu_seconds_total{job="node-exporter",mode="idle"}[2m]))`
	queryNodeMem = `1 - node_memory_MemAvailable_bytes{job="node-exporter"} / node_memory_MemTotal_bytes{job="node-exporter"}`
)

// usageSnap is the last good usage values: pods by "<ns>/<pod>", nodes by instance "<ip>:9100".
type usageSnap struct {
	podCPU, podMem   map[string]float64
	nodeCPU, nodeMem map[string]float64
}

// pollUsage runs the four usage queries and keeps the values of those that answered.
func (r *runtime) pollUsage() {
	q := r.querier()
	if q == nil {
		r.endUsage(usageSnap{}, upstream.ErrDisabled, false)
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, pollTimeout)
	defer cancel()
	res := queryAll(ctx, q, map[string]string{
		"podCPU": queryPodCPU, "podMem": queryPodMem, "nodeCPU": queryNodeCPU, "nodeMem": queryNodeMem,
	})
	var snap usageSnap
	if res["podCPU"].err == nil {
		snap.podCPU = byLabels(res["podCPU"].samples, "namespace", "pod")
	}
	if res["podMem"].err == nil {
		snap.podMem = byLabels(res["podMem"].samples, "namespace", "pod")
	}
	if res["nodeCPU"].err == nil {
		snap.nodeCPU = byLabel(res["nodeCPU"].samples, "instance")
	}
	if res["nodeMem"].err == nil {
		snap.nodeMem = byLabel(res["nodeMem"].samples, "instance")
	}
	if r.ctx.Err() != nil {
		return
	}
	r.endUsage(snap, firstErr(res), true)
}

// endUsage records a finished poll: the values that answered replace the old ones, the rest stay.
func (r *runtime) endUsage(got usageSnap, err error, merge bool) {
	r.mu.Lock()
	if merge {
		if got.podCPU != nil {
			r.usage.podCPU = got.podCPU
		}
		if got.podMem != nil {
			r.usage.podMem = got.podMem
		}
		if got.nodeCPU != nil {
			r.usage.nodeCPU = got.nodeCPU
		}
		if got.nodeMem != nil {
			r.usage.nodeMem = got.nodeMem
		}
	}
	r.usageOut = outcome{polled: true, err: err, at: r.h.Now()}
	r.mu.Unlock()
	r.noteSource("usage", err)
	r.mark(dirtyCluster)
}

// usageSnapshot is the usage values as the last polls left them.
func (r *runtime) usageSnapshot() usageSnap {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.usage
}

// noteSource logs a failed upstream poll once per change of its category: viewers get the
// category only (design C.6), the detail goes to the log, scrubbed.
func (r *runtime) noteSource(key string, err error) {
	cat := upstream.Category(err)
	if r.changed("source:"+key, cat) && err != nil && cat != upstream.CategoryNotConfigured {
		r.h.Logger().Warn("upstream source failed", "source", key, "category", cat, "error", r.h.ScrubLog(err.Error()))
	}
}

// qres is the answer of one query.
type qres struct {
	samples []svc.Sample
	err     error
}

// queryAll runs the named instant queries concurrently.
func queryAll(ctx context.Context, q svc.Querier, qs map[string]string) map[string]qres {
	out := make(map[string]qres, len(qs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, expr := range qs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := q.Instant(ctx, expr)
			mu.Lock()
			out[name] = qres{s, err}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// firstErr returns the error of the alphabetically first failed query, or nil.
func firstErr(res map[string]qres) error {
	names := make([]string, 0, len(res))
	for n, r := range res {
		if r.err != nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return res[names[0]].err
}

func first(s []svc.Sample) (float64, bool) {
	if len(s) == 0 {
		return 0, false
	}
	return s[0].Value, true
}

// byLabel indexes sample values by one label (later samples of the same label win).
func byLabel(s []svc.Sample, label string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[label]] = x.Value
	}
	return out
}

// byLabels indexes sample values by "<a>/<b>".
func byLabels(s []svc.Sample, a, b string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[a]+"/"+x.Labels[b]] = x.Value
	}
	return out
}

// pollStorage runs the storage poll (queryStorage, storage.go) and records how it ended.
func (r *runtime) pollStorage() {
	q := r.querier()
	if q == nil {
		r.endStorage(storageSnap{}, upstream.ErrDisabled, false)
		return
	}
	ctx, cancel := context.WithTimeout(r.ctx, pollTimeout)
	defer cancel()
	r.mu.RLock()
	prev := r.storage
	r.mu.RUnlock()
	snap, err := queryStorage(ctx, q, r.cfg, prev)
	if r.ctx.Err() != nil {
		return
	}
	r.endStorage(snap, err, true)
}

func (r *runtime) endStorage(snap storageSnap, err error, set bool) {
	r.mu.Lock()
	if set {
		r.storage = snap
	}
	r.storageOut = outcome{polled: true, err: err, at: r.h.Now()}
	r.mu.Unlock()
	r.noteSource("storage", err)
	r.mark(dirtyStorage)
}
