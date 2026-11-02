package rustfs

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// The names of the four queries of a poll.
const (
	qBucketUsed     = "bucketUsed"
	qBucketQuota    = "bucketQuota"
	qCapacityUsed   = "capacityUsed"
	qCapacityUsable = "capacityUsable"
)

// exprs are the PromQL expressions of a poll, shaped by the configured metric names. The names
// were checked to be metric names by pluginconf.RustFS.Validate before the plugin started, so
// they cannot add anything to an expression.
type exprs map[string]string

func newExprs(m pluginconf.RustFSMetrics) exprs {
	return exprs{
		qBucketUsed:     fmt.Sprintf("max by (bucket)(%s)", m.BucketUsed),
		qBucketQuota:    fmt.Sprintf("max by (bucket)(%s)", m.BucketQuota),
		qCapacityUsed:   fmt.Sprintf("max(%s)", m.CapacityUsed),
		qCapacityUsable: fmt.Sprintf("max(%s)", m.CapacityUsable),
	}
}

// snapshot is the last good values of the queries. The maps are replaced as a whole when their
// query answers and never written afterwards, so a copy of the struct may be read while the next
// poll builds its successor.
type snapshot struct {
	bucketUsed, bucketQuota map[string]float64 // by bucket
	capUsed, capUsable      float64
}

// qres is the outcome of one query.
type qres struct {
	samples []svc.Sample
	err     error
}

// pollLoop polls at once and then every tm.poll, and ends with ctx.
func (p *Plugin) pollLoop(ctx context.Context, rt *runtime) {
	t := time.NewTicker(p.tm.poll)
	defer t.Stop()
	for {
		p.poll(ctx, rt)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// poll runs the four queries, keeps the values of those that answered and publishes. A poll that
// fails publishes too: the source says why, and the values of the last good poll stand.
func (p *Plugin) poll(ctx context.Context, rt *runtime) {
	var res map[string]qres
	var err error
	if q := rt.querier(); q == nil {
		err = upstream.ErrDisabled
	} else {
		pctx, cancel := context.WithTimeout(ctx, p.tm.timeout)
		res = queryAll(pctx, q, rt.q)
		cancel()
		err = firstErr(res)
	}
	if ctx.Err() != nil {
		return // stopping: whatever came back is not an outcome
	}

	p.mu.Lock()
	prevCat, hadPolled := p.cat, p.polled
	p.snap = applyPoll(p.snap, res)
	snap := p.snap
	p.mu.Unlock()

	now := rt.h.Now()
	cat := upstream.Category(err)
	rt.h.Publish(topicUsage, usageOf(snap), []model.SourceStatus{{Name: sourcePrometheus, OK: cat == "", Error: cat, CheckedAt: now.UTC()}})

	// The row turns from starting only now, after the payload is out.
	p.mu.Lock()
	p.polled, p.cat, p.at = true, cat, now
	p.mu.Unlock()

	if cat != prevCat || !hadPolled {
		logCategory(rt, cat, hadPolled, prevCat, err)
	}
}

// logCategory logs a change of the source's outcome once. The detail names URLs and hosts, so it
// goes to the log, scrubbed, never to the viewers: they get the category.
func logCategory(rt *runtime, cat string, hadPolled bool, prevCat string, err error) {
	switch {
	case cat != "":
		if cat != upstream.CategoryNotConfigured {
			rt.h.Logger().Warn("upstream source failed", "source", sourcePrometheus, "category", cat, "error", rt.h.ScrubLog(err.Error()))
		}
	case hadPolled && prevCat != "":
		rt.h.Logger().Info("upstream source recovered", "source", sourcePrometheus)
	}
}

// applyPoll folds the results of one poll into snap: each value is replaced when its query
// answered, an answer with no samples included, and kept when it failed. snap is not changed.
func applyPoll(snap snapshot, res map[string]qres) snapshot {
	ok := func(name string) ([]svc.Sample, bool) { r, has := res[name]; return r.samples, has && r.err == nil }
	if s, good := ok(qBucketUsed); good {
		snap.bucketUsed = byLabel(s, "bucket")
	}
	if s, good := ok(qBucketQuota); good {
		snap.bucketQuota = byLabel(s, "bucket")
	}
	if s, good := ok(qCapacityUsed); good {
		snap.capUsed, _ = first(s)
	}
	if s, good := ok(qCapacityUsable); good {
		snap.capUsable, _ = first(s)
	}
	return snap
}

// queryAll runs the named instant queries concurrently.
func queryAll(ctx context.Context, q svc.Querier, qs exprs) map[string]qres {
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
