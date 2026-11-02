package cnpg

import (
	"context"
	"fmt"

	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// ageQuery is the PromQL of the backup age: the seconds since the newest backup of each
// cluster's primary. The metric was checked to be a metric name by pluginconf.CNPG.Validate
// before the plugin started, so it cannot add anything to the expression.
func ageQuery(metric string) string {
	return fmt.Sprintf(`time() - max by (namespace,cluster)(%s{role="primary"})`, metric)
}

// pollIfDue starts a backup-age poll when none is running and the last one began tm.poll ago or
// never. It runs in the watch loop, and only while the CRD is there to attach an age to. The poll
// itself runs on its own goroutine, so a slow Prometheus does not hold up the cluster state.
func (p *Plugin) pollIfDue(rt *runtime) {
	now := rt.h.Now()
	if !rt.lastPoll.IsZero() && now.Sub(rt.lastPoll) < p.tm.poll {
		return
	}
	if !p.polling.CompareAndSwap(false, true) {
		return
	}
	rt.lastPoll = now
	p.spawn(rt.h, "cnpg backup age", func() {
		defer p.polling.Store(false)
		p.pollAge(rt)
	})
}

// pollAge reads the backup ages and records how the poll ended. A failed query keeps the ages of
// the last good one; an answer with no samples replaces them (a cluster without a backup has no
// age).
func (p *Plugin) pollAge(rt *runtime) {
	var samples []svc.Sample
	var err error
	if q := rt.querier(); q == nil {
		err = upstream.ErrDisabled
	} else {
		ctx, cancel := context.WithTimeout(rt.ctx, p.tm.timeout)
		samples, err = q.Instant(ctx, rt.query)
		cancel()
	}
	if rt.ctx.Err() != nil {
		return // stopping: whatever came back is not an outcome
	}

	cat := upstream.Category(err)
	p.mu.Lock()
	prevCat, hadPolled := p.cat, p.polled
	if err == nil {
		p.ages = byLabels(samples, "namespace", "cluster")
	}
	p.polled, p.cat, p.at = true, cat, rt.h.Now()
	p.gen++
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

// byLabels indexes sample values by "<a>/<b>".
func byLabels(s []svc.Sample, a, b string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[a]+"/"+x.Labels[b]] = x.Value
	}
	return out
}
