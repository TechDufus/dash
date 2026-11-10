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
	up map[string]bool // by machine name; the last good answer of a node-exporter machine
	// stats are the node-exporter machines' last stats, by machine name; nil until a poll finds
	// any of a machine's series.
	stats    map[string]*hwmodel.MachineStats
	polled   bool      // a node-exporter poll has ended, whether or not it succeeded
	err      string    // the error category of the last poll; "" when it succeeded
	last     time.Time // when the last poll started
	possible bool      // whether the querier was there at the last look

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
	av := rt.availability(rt.hostIDs())
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

// pollMachines asks each node-exporter machine's up series, and the stats of all of them with
// one query per figure.
func (p *Plugin) pollMachines(ctx context.Context, rt *runtime, q svc.Querier) {
	var nodenames []string
	for _, m := range rt.cfg.Machines {
		if isPrometheus(m) && !slices.Contains(nodenames, m.NodeExporterNodename) {
			nodenames = append(nodenames, m.NodeExporterNodename)
		}
	}
	qs := rt.q.machineStats(nodenames)
	for i, m := range rt.cfg.Machines {
		if isPrometheus(m) {
			qs[upKey(i)] = rt.q.machineUp(m.NodeExporterNodename)
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
		p.mach.up, p.mach.stats = map[string]bool{}, map[string]*hwmodel.MachineStats{}
	}
	for i, m := range rt.cfg.Machines {
		if !isPrometheus(m) {
			continue
		}
		if r, ok := res[upKey(i)]; ok && r.err == nil { // a failed query keeps the last value
			v, has := first(r.samples)
			p.mach.up[m.Name] = has && v >= 1
		}
		p.mach.stats[m.Name] = applyStats(p.mach.stats[m.Name], res, m.NodeExporterNodename)
	}
	p.mach.polled, p.mach.err = true, upstream.Category(err)
	cat := p.mach.err
	p.mu.Unlock()

	if cat != prev.err || !prev.polled {
		logCategory(rt, "machines", cat, prev.polled, prev.err, err)
	}
}

// sampleOf is the sample of one nodename among a query's samples.
func sampleOf(s []upstream.Sample, nodename string) (upstream.Sample, bool) {
	for _, x := range s {
		if x.Labels["nodename"] == nodename {
			return x, true
		}
	}
	return upstream.Sample{}, false
}

// upKey is the result key of machine i's up query, apart from the stats' keys.
func upKey(i int) string { return "up/" + strconv.Itoa(i) }

// applyStats folds one poll into a machine's stats: each figure is replaced when its query
// succeeded (zero, or a nil temperature, when the batch lacks the machine) and kept when it
// failed. The result is nil while neither node_uname_info nor the CPU count has the machine, so a
// machine whose node-exporter reports nothing has no stats rather than a row of zeros; a failed
// query of either keeps what the last poll decided. old is not changed.
func applyStats(old *hwmodel.MachineStats, res map[string]qres, nodename string) *hwmodel.MachineStats {
	var st hwmodel.MachineStats
	if old != nil {
		st = *old
	}
	present := false
	read := func(key string, set func(upstream.Sample, bool)) {
		r, asked := res[key]
		if !asked || r.err != nil {
			if key == "uname" || key == "cpus" {
				present = present || old != nil
			}
			return
		}
		s, has := sampleOf(r.samples, nodename)
		set(s, has)
		if key == "uname" || key == "cpus" {
			present = present || has
		}
	}
	read("uname", func(s upstream.Sample, _ bool) {
		st.OS, st.Arch, st.Kernel = s.Labels["sysname"], s.Labels["machine"], s.Labels["release"]
	})
	read("cpus", func(s upstream.Sample, _ bool) { st.CPUs = int(s.Value) })
	read("cpu", func(s upstream.Sample, _ bool) { st.CPURatio = s.Value })
	read("memTotal", func(s upstream.Sample, _ bool) { st.MemTotalBytes = int64(s.Value) })
	read("memUsed", func(s upstream.Sample, _ bool) { st.MemUsedBytes = int64(s.Value) })
	read("diskTotal", func(s upstream.Sample, _ bool) { st.DiskTotalBytes = int64(s.Value) })
	// Used is size minus what is free to unprivileged users, as df counts it.
	read("diskAvail", func(s upstream.Sample, has bool) {
		st.DiskUsedBytes = 0
		if has && st.DiskTotalBytes > 0 {
			st.DiskUsedBytes = max(0, st.DiskTotalBytes-int64(s.Value))
		}
	})
	read("load1", func(s upstream.Sample, _ bool) { st.Load1 = s.Value })
	read("load5", func(s upstream.Sample, _ bool) { st.Load5 = s.Value })
	read("load15", func(s upstream.Sample, _ bool) { st.Load15 = s.Value })
	read("uptime", func(s upstream.Sample, _ bool) { st.UptimeSeconds = int64(s.Value) })
	read("temp", func(s upstream.Sample, has bool) {
		st.TempC = nil
		if has {
			st.TempC = &s.Value
		}
	})
	if !present {
		return nil
	}
	return &st
}

// machinesData is the topic's payload and its sources. A machine whose source cannot be asked
// (no querier, no Gatus) is down, and its source says not_configured. hasQuerier is whether the
// prometheus plugin runs; ep is the Gatus service, nil when it does not.
func (p *Plugin) machinesData(rt *runtime, hasQuerier bool, ep svc.EndpointHealth) (hwmodel.MachinesData, []model.SourceStatus) {
	p.mu.Lock()
	up := maps.Clone(p.mach.up) // reading a nil map is fine
	// applyStats replaces a machine's stats on every poll and never changes them, so the
	// pointers can be shared with the payload.
	stats := maps.Clone(p.mach.stats)
	polled, cat := p.mach.polled, p.mach.err
	p.mu.Unlock()

	d := hwmodel.MachinesData{Machines: []hwmodel.Machine{}}
	srcs := []model.SourceStatus{}
	now := rt.h.Now().UTC()
	for _, m := range rt.cfg.Machines {
		x := hwmodel.Machine{Ref: plugin.Ref(ID, "Machine", m.Name), Name: m.Name}
		if isPrometheus(m) {
			x.Source, x.Up = sourcePrometheus, up[m.Name]
			if x.Up {
				x.Stats = stats[m.Name] // a machine that is down shows no stale figures
			}
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
