package hardware

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// hostState is what the poller has learned of one host.
type hostState struct {
	// vals holds the values of the last good query of each kind; a query that fails leaves the
	// old values (the last good payload survives a failed poll). It carries no ref, name or
	// setting: those are added when the payload is built, from the settings and services of the
	// moment.
	vals     model.HostData
	polled   bool      // a poll has ended, whether or not it succeeded
	err      string    // the error category of the last poll; "" when every query succeeded
	at       time.Time // when the last poll ended
	last     time.Time // when the last poll started
	possible bool      // whether the half could run at the last look
}

// hostLoop looks at its dependencies every watchEvery, polls the hosts that are due and publishes
// what it has. A poll that fails publishes too: the sources say why.
func (p *Plugin) hostLoop(ctx context.Context, rt *runtime) {
	t := time.NewTicker(p.tm.watch)
	defer t.Stop()
	for {
		p.hostTick(ctx, rt)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// hostTick is one look. A host polls when it can run and has never polled, when the interval has
// passed, or when it could not run at the last look (a dependency that appears later starts the
// work at once instead of waiting out an interval). The hosts that are due poll concurrently and
// the topic is published once they are all back, so the first payload holds every host that can
// be read.
func (p *Plugin) hostTick(ctx context.Context, rt *runtime) {
	ids := rt.hostIDs()
	q := rt.querier()
	now := rt.h.Now()

	var due []hostID
	p.mu.Lock()
	for _, id := range ids {
		s := p.hostAt(id.i)
		possible := q != nil && id.runnable()
		if possible && (s.last.IsZero() || !s.possible || now.Sub(s.last) >= p.tm.host) {
			s.last = now
			due = append(due, id)
		}
		s.possible = possible
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.pollHost(ctx, rt, q, id)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	p.publishHosts(rt, ids, q != nil)
}

// pollHost runs one host's queries and keeps what they return.
func (p *Plugin) pollHost(ctx context.Context, rt *runtime, q svc.Querier, id hostID) {
	pctx, cancel := context.WithTimeout(ctx, p.tm.timeout)
	defer cancel()
	res := queryAll(pctx, q, rt.q.hostQueries(id.nodename, rt.cfg.ZFSScrubMetricPrefix))
	if ctx.Err() != nil {
		return // stopping: whatever came back is not an outcome
	}
	err := firstErr(res)

	p.mu.Lock()
	s := p.hostAt(id.i)
	prev := *s
	s.vals = applyHost(s.vals, res)
	s.polled, s.err, s.at = true, upstream.Category(err), rt.h.Now()
	cat := s.err
	p.mu.Unlock()

	if cat != prev.err || !prev.polled {
		logCategory(rt, "host "+id.name, cat, prev.polled, prev.err, err)
	}
}

// logCategory logs a change of a source's outcome once. The detail names URLs and hosts, so it
// goes to the log, scrubbed, never to the viewers: they get the category.
func logCategory(rt *runtime, half, cat string, hadPolled bool, prevCat string, err error) {
	switch {
	case cat != "":
		rt.h.Logger().Warn("upstream source failed", "source", sourcePrometheus, "half", half, "category", cat, "error", rt.h.ScrubLog(err.Error()))
	case hadPolled && prevCat != "":
		rt.h.Logger().Info("upstream source recovered", "source", sourcePrometheus, "half", half)
	}
}

// publishHosts publishes the hosts topic: every host that has polled, in configuration order,
// with its links. The first publish waits until every host that can be read has finished its
// first poll, so the page never shows a host list that is empty or partial because a poll is
// still on its way; a host that cannot run (no querier, or waiting for its Proxmox node) does not
// hold it up. When no host can run, the list is published empty at once and a host that becomes
// runnable later is published after its poll, so the page is never left waiting for the topic.
// Nothing is published while nothing of the plugin can run and nothing has been: a row that
// cannot run holds no data. The sources are one prometheus source, failing with the category of
// the first host whose last poll failed, and proxmox while a host waits for its node.
func (p *Plugin) publishHosts(rt *runtime, ids []hostID, hasQuerier bool) {
	p.mu.Lock()
	ran, first := p.ran, !p.hostsPublished
	waiting := false // a runnable host has not finished its first poll
	for _, id := range ids {
		if hasQuerier && id.runnable() && !p.hostAt(id.i).polled {
			waiting = true
		}
	}
	p.mu.Unlock()
	if !ran && !rt.availability(ids).any() {
		return
	}
	if first && waiting {
		return
	}
	d := model.HostsData{Hosts: p.hostsData(rt, ids)}
	for i := range d.Hosts {
		d.Hosts[i].Links = hostLinks(rt.h, d.Hosts[i].Node)
	}
	now := rt.h.Now().UTC()
	srcs := []model.SourceStatus{}
	p.mu.Lock()
	for _, id := range ids {
		s := p.hostAt(id.i)
		switch {
		case !hasQuerier:
			srcs = mergeSources(srcs, notConfigured(sourcePrometheus))
		case !id.known():
			srcs = mergeSources(srcs, notConfigured(sourceProxmox))
		case id.dupOf < 0 && s.polled:
			srcs = mergeSources(srcs, model.SourceStatus{Name: sourcePrometheus, OK: s.err == "", Error: s.err, CheckedAt: now})
		}
	}
	p.hostsPublished = true
	p.mu.Unlock()
	p.publish(rt, topicHosts, d, srcs)
}

// hostLinks are the deep links of a host: its hypervisor UI and the shell of its node, then
// Grafana. The providers decide which exist; a plugin never imports another.
func hostLinks(h plugin.Host, node string) []model.Link {
	r := h.Links()
	out := []model.Link{}
	for _, kind := range []string{"ProxmoxUI", "HostShell", "Host"} {
		out = append(out, r.Resolve(plugin.LinkRequest{Kind: kind, Name: node})...)
	}
	return out
}

// shellLinks are the links the attention items of a host carry: the hypervisor UI and the shell
// of the host's node.
func shellLinks(h plugin.Host, node string) []model.Link {
	r := h.Links()
	return append(r.Resolve(plugin.LinkRequest{Kind: "ProxmoxUI"}), r.Resolve(plugin.LinkRequest{Kind: "HostShell", Name: node})...)
}

// hostsData is the payload of every host that has polled, without links: the last good values
// with the settings and the names of the moment, in configuration order. A host whose nodename is
// unknown, or that repeats an earlier host, is left out.
func (p *Plugin) hostsData(rt *runtime, ids []hostID) []model.HostData {
	out := []model.HostData{}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, id := range ids {
		s := p.hostAt(id.i)
		if !id.runnable() || !s.polled {
			continue
		}
		out = append(out, buildHostData(s.vals, rt.cfg, rt.cfg.Hosts[id.i], id.name, id.node))
	}
	return out
}

// buildHostData adds to the polled values what the plugin owns: the name, node and ref, the model,
// the layout settings of the host's entry h, the thresholds and the health they give, and a ref on
// every pool and disk. Lists that were never filled are empty, not null. vals is not changed.
func buildHostData(vals model.HostData, cfg pluginconf.Hardware, h pluginconf.HardwareHost, name, node string) model.HostData {
	d := vals
	d.Name, d.Node, d.Ref, d.Model = name, node, plugin.Ref(ID, "Host", name), h.Model
	d.Bays, d.FanMaxRPM = h.Bays, h.FanMaxRPM
	d.SensorAliases = model.SensorAliases{Inlet: h.SensorAliases.Inlet, Exhaust: h.SensorAliases.Exhaust}
	d.TempWarnC, d.TempCritC = cfg.TempThresholds.Warn, cfg.TempThresholds.Crit
	d.ScrubTracked = cfg.ZFSScrubMetricPrefix != ""
	d.CPUTempHealth = tempHealth(cfg.TempThresholds.Warn, cfg.TempThresholds.Crit, maxOf(d.PackageTempsC))
	if d.PackageTempsC == nil {
		d.PackageTempsC = []float64{}
	}
	if d.Sensors == nil {
		d.Sensors = []model.Sensor{}
	}
	if d.Fans == nil {
		d.Fans = []model.Fan{}
	}
	if d.PSUs == nil {
		d.PSUs = []model.PSU{}
	}
	d.Pools = make([]model.Pool, len(vals.Pools))
	for i, x := range vals.Pools {
		x.Ref = plugin.Ref(ID, "Pool", name, x.Name)
		d.Pools[i] = x
	}
	d.Disks = make([]model.Disk, len(vals.Disks))
	for i, x := range vals.Disks {
		id := x.Serial
		if id == "" {
			id = "bay-" + x.Bay
		}
		x.Ref = plugin.Ref(ID, "Disk", name, id)
		d.Disks[i] = x
	}
	d.Links = []model.Link{}
	d.Health = model.HealthOK
	for _, it := range attentionOf(d, nil) {
		d.Health = worst(d.Health, model.Health(it.Severity))
	}
	return d
}

// applyHost folds the results of one poll into vals: each kind of value is replaced when its
// query succeeded and kept when it failed. vals is not changed.
func applyHost(vals model.HostData, res map[string]qres) model.HostData {
	d := vals
	ok := func(k string) ([]upstream.Sample, bool) { r, has := res[k]; return r.samples, has && r.err == nil }

	if s, good := ok("cpu"); good {
		if v, has := first(s); has {
			d.CPURatio = v
		}
	}
	if t, good := ok("memTotal"); good {
		if a, good2 := ok("memAvail"); good2 {
			if tv, has := first(t); has {
				av, _ := first(a)
				d.MemTotalBytes, d.MemUsedBytes = int64(tv), int64(tv-av)
			}
		}
	}
	if s, good := ok("pkg"); good {
		d.PackageTempsC = []float64{}
		for _, x := range s {
			d.PackageTempsC = append(d.PackageTempsC, x.Value)
		}
		sort.Float64s(d.PackageTempsC)
	}
	if s, good := ok("sensors"); good {
		d.Sensors = []model.Sensor{}
		for _, x := range s {
			d.Sensors = append(d.Sensors, model.Sensor{Name: x.Labels["name"], ValueC: x.Value})
		}
		sort.Slice(d.Sensors, func(i, j int) bool { return d.Sensors[i].Name < d.Sensors[j].Name })
	}
	if s, good := ok("fans"); good {
		d.Fans = []model.Fan{}
		for _, x := range s {
			d.Fans = append(d.Fans, model.Fan{Name: x.Labels["name"], RPM: x.Value})
		}
		sort.Slice(d.Fans, func(i, j int) bool { return d.Fans[i].Name < d.Fans[j].Name })
	}
	if s, good := ok("power"); good {
		d.PowerWatts = sumValues(s)
	}
	if s, good := ok("psu"); good {
		d.PSUs = psus(s)
	}
	if s, good := ok("sel"); good {
		if v, has := first(s); has {
			d.SELFreeBytes = int64(v)
		}
	}
	if s, good := ok("pools"); good {
		d.Pools = pools(s, res["scrubEnd"].samples, res["scrubErr"].samples, res["scrubRun"].samples)
	}
	if s, good := ok("healthy"); good {
		d.Disks = disks(s, res["temp"].samples, res["life"].samples, res["wear"].samples)
	}
	return d
}

func psus(s []upstream.Sample) []model.PSU {
	counts := map[string]int{}
	for _, x := range s {
		counts[x.Labels["name"]]++
	}
	out := []model.PSU{}
	for _, x := range s {
		name := x.Labels["name"]
		if counts[name] > 1 {
			name += " #" + x.Labels["id"]
		}
		h := model.HealthOK
		switch {
		case x.Value >= 2:
			h = model.HealthCrit
		case x.Value >= 1:
			h = model.HealthWarn
		}
		out = append(out, model.PSU{Name: name, State: h})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func pools(state, scrubEnd, scrubErr, scrubRun []upstream.Sample) []model.Pool {
	end, errs, run := byLabel(scrubEnd, "zpool"), byLabel(scrubErr, "zpool"), byLabel(scrubRun, "zpool")
	out := []model.Pool{}
	for _, x := range state {
		name := x.Labels["zpool"]
		p := model.Pool{Name: name, State: x.Labels["state"], ScrubErrors: int(errs[name]), ScrubInProgress: run[name] > 0}
		if e, has := end[name]; has && e > 0 {
			t := time.Unix(int64(e), 0).UTC()
			p.LastScrubEnd = &t
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func disks(healthy, temp, life, wear []upstream.Sample) []model.Disk {
	temps, lives, wears := byLabel(temp, "bay"), byLabel(life, "bay"), byLabel(wear, "bay")
	out := []model.Disk{}
	for _, x := range healthy {
		bay := x.Labels["bay"]
		d := model.Disk{Bay: bay, Serial: x.Labels["serial"], Device: x.Labels["disk"], Healthy: x.Value >= 1}
		if t, has := temps[bay]; has {
			d.TempC = &t
		}
		if l, has := lives[bay]; has {
			d.LifeRemainingPct = &l
		} else if w, has := wears[bay]; has {
			d.LifeRemainingPct = &w
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return atoi(out[i].Bay) < atoi(out[j].Bay) })
	return out
}

// qres is the outcome of one query.
type qres struct {
	samples []upstream.Sample
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

func first(s []upstream.Sample) (float64, bool) {
	if len(s) == 0 {
		return 0, false
	}
	return s[0].Value, true
}

func sumValues(s []upstream.Sample) float64 {
	var t float64
	for _, x := range s {
		t += x.Value
	}
	return t
}

// byLabel indexes sample values by one label (later samples of the same label win).
func byLabel(s []upstream.Sample, label string) map[string]float64 {
	out := make(map[string]float64, len(s))
	for _, x := range s {
		out[x.Labels[label]] = x.Value
	}
	return out
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
