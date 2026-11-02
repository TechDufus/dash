package hardware

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// machineState is what the machines half has learned.
type machineState struct {
	up       map[string]bool // by machine name; the last good answer of a node-exporter machine
	polled   bool            // a node-exporter poll has ended, whether or not it succeeded
	err      string          // the error category of the last poll; "" when it succeeded
	last     time.Time       // when the last poll started
	possible bool            // whether the querier was there at the last look

	// What the last publish stood for: the Gatus service it read and its revision. The half
	// publishes again when either differs, and after every poll.
	published bool
	pubSvc    svc.EndpointHealth
	pubRev    uint64
}

// machinesLoop looks at its dependencies every watchEvery: it polls the node-exporter machines
// when that is due and republishes when the node-exporter answers or Gatus changed. Gatus is
// not polled by this plugin, so its machines follow the service's Revision within the tick.
func (p *Plugin) machinesLoop(ctx context.Context, rt *runtime) {
	t := time.NewTicker(p.tm.watch)
	defer t.Stop()
	for {
		p.machinesTick(ctx, rt)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (p *Plugin) machinesTick(ctx context.Context, rt *runtime) {
	av := rt.availability()
	p.mu.Lock()
	ran := p.ran
	p.mu.Unlock()
	if !ran && !av.any() {
		return // nothing can run: publish nothing, the row says why
	}

	hasProm := slices.ContainsFunc(rt.cfg.Machines, isPrometheus)
	q := rt.querier()
	polledNow := false
	if hasProm && q != nil {
		now := rt.h.Now()
		p.mu.Lock()
		due := p.mach.last.IsZero() || !p.mach.possible || now.Sub(p.mach.last) >= p.tm.machines
		p.mach.possible = true
		if due {
			p.mach.last = now
		}
		p.mu.Unlock()
		if due {
			p.pollMachines(ctx, rt, q)
			polledNow = true
		}
	} else {
		p.mu.Lock()
		p.mach.possible = false
		p.mu.Unlock()
	}
	if ctx.Err() != nil {
		return
	}

	// The first publish waits for the first node-exporter poll, so a machine does not show as
	// down for the second it takes to ask. Without a querier the machines say so at once.
	p.mu.Lock()
	ready := !hasProm || q == nil || p.mach.polled
	p.mu.Unlock()
	if !ready {
		return
	}
	ep := rt.endpoints()
	var rev uint64
	if ep != nil {
		rev = ep.Revision()
	}
	p.mu.Lock()
	changed := !p.mach.published || polledNow || rev != p.mach.pubRev || !sameService(ep, p.mach.pubSvc)
	if changed {
		p.mach.published, p.mach.pubSvc, p.mach.pubRev = true, ep, rev
	}
	p.mu.Unlock()
	if changed {
		d, srcs := p.machinesData(rt, q != nil, ep)
		p.publish(rt, topicMachines, d, srcs)
	}
}

// pollMachines asks the node-exporter machines' up series.
func (p *Plugin) pollMachines(ctx context.Context, rt *runtime, q svc.Querier) {
	qs := map[string]string{}
	for i, m := range rt.cfg.Machines {
		if isPrometheus(m) {
			qs[strconv.Itoa(i)] = rt.q.machineUp(m.NodeExporterNodename)
		}
	}
	pctx, cancel := context.WithTimeout(ctx, p.tm.timeout)
	defer cancel()
	res := queryAll(pctx, q, qs)
	if ctx.Err() != nil {
		return // stopping
	}
	err := firstErr(res)

	p.mu.Lock()
	prev := p.mach
	if p.mach.up == nil {
		p.mach.up = map[string]bool{}
	}
	for i, m := range rt.cfg.Machines {
		if r, ok := res[strconv.Itoa(i)]; ok && r.err == nil { // a failed query keeps the last value
			v, has := first(r.samples)
			p.mach.up[m.Name] = has && v >= 1
		}
	}
	p.mach.polled, p.mach.err = true, upstream.Category(err)
	cat := p.mach.err
	p.mu.Unlock()

	if cat != prev.err || !prev.polled {
		logCategory(rt, "machines", cat, prev.polled, prev.err, err)
	}
}

// machinesData is the topic's payload and its sources. A machine whose source cannot be asked
// (no querier, no Gatus) is down, and its source says not_configured. hasQuerier is whether the
// prometheus plugin runs; ep is the Gatus service, nil when it does not.
func (p *Plugin) machinesData(rt *runtime, hasQuerier bool, ep svc.EndpointHealth) (hwmodel.MachinesData, []model.SourceStatus) {
	p.mu.Lock()
	up := maps.Clone(p.mach.up) // reading a nil map is fine
	polled, cat := p.mach.polled, p.mach.err
	p.mu.Unlock()

	d := hwmodel.MachinesData{Machines: []hwmodel.Machine{}}
	srcs := []model.SourceStatus{}
	now := rt.h.Now().UTC()
	for _, m := range rt.cfg.Machines {
		x := hwmodel.Machine{Ref: plugin.Ref(ID, "Machine", m.Name), Name: m.Name}
		if isPrometheus(m) {
			x.Source, x.Up = sourcePrometheus, up[m.Name]
		} else {
			x.Source = sourceGatus
			if ep != nil {
				// A check that exists but has no results counts as down; one that does not exist is
				// down too.
				x.Up, _ = ep.ByName(m.GatusEndpoint)
			}
		}
		d.Machines = append(d.Machines, x)
	}
	if slices.ContainsFunc(rt.cfg.Machines, isPrometheus) {
		switch {
		case !hasQuerier:
			srcs = mergeSources(srcs, notConfigured(sourcePrometheus))
		case polled:
			srcs = mergeSources(srcs, model.SourceStatus{Name: sourcePrometheus, OK: cat == "", Error: cat, CheckedAt: now})
		}
	}
	if slices.ContainsFunc(rt.cfg.Machines, isGatus) {
		if ep == nil {
			srcs = mergeSources(srcs, notConfigured(sourceGatus))
		} else {
			for _, s := range ep.SourceStatuses() {
				srcs = mergeSources(srcs, s)
			}
		}
	}
	return d, srcs
}

// sameService reports whether a and b are the same service instance. A service that cannot be
// compared is never the same, so a change of it is never missed.
func sameService(a, b svc.EndpointHealth) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb || !ta.Comparable() {
		return false
	}
	return a == b
}
