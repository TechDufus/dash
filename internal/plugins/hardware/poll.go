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

// hostState is what the host poller has learned.
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

// hostLoop looks at its dependencies every watchEvery, polls when it is due and publishes what it
// has. A poll that fails publishes too: the sources say why.
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

// hostTick is one look. The half polls when it can run and has never polled, when the interval
// has passed, or when it could not run at the last look (a dependency that appears later starts
// the work at once instead of waiting out an interval).
func (p *Plugin) hostTick(ctx context.Context, rt *runtime) {
	n, ok := rt.nodename()
	q := rt.querier()
	possible := q != nil && ok
	now := rt.h.Now()

	p.mu.Lock()
	due := possible && (p.host.last.IsZero() || !p.host.possible || now.Sub(p.host.last) >= p.tm.host)
	p.host.possible = possible
	if due {
		p.host.last = now
	}
	p.mu.Unlock()

	if due {
		p.pollHost(ctx, rt, q, n)
	}
	if ctx.Err() != nil {
		return
	}
	p.publishHost(rt)
}

// pollHost runs the host's queries and keeps what they return.
func (p *Plugin) pollHost(ctx context.Context, rt *runtime, q svc.Querier, nodename string) {
	pctx, cancel := context.WithTimeout(ctx, p.tm.timeout)
	defer cancel()
	res := queryAll(pctx, q, rt.q.hostQueries(nodename, rt.cfg.ZFSScrubMetricPrefix))
	if ctx.Err() != nil {
		return // stopping: whatever came back is not an outcome
	}
	err := firstErr(res)

	p.mu.Lock()
	prev := p.host
	p.host.vals = applyHost(p.host.vals, res)
	p.host.polled, p.host.err, p.host.at = true, upstream.Category(err), rt.h.Now()
	cat := p.host.err
	p.mu.Unlock()

	if cat != prev.err || !prev.polled {
		logCategory(rt, "host", cat, prev.polled, prev.err, err)
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

// publishHost publishes the host topic once the half has polled.
func (p *Plugin) publishHost(rt *runtime) {
	p.mu.Lock()
	polled := p.host.polled
	cat := p.host.err
	p.mu.Unlock()
	if !polled {
		return
	}
	d, ok := p.hostData(rt)
	if !ok {
		return
	}
	d.Links = hostLinks(rt.h)
	p.publish(rt, topicHosts, d, []model.SourceStatus{{Name: sourcePrometheus, OK: cat == "", Error: cat, CheckedAt: rt.h.Now().UTC()}})
}

// hostLinks are the deep links of the host: its hypervisor UI and shell, then Grafana. The
// providers decide which exist; a plugin never imports another.
func hostLinks(h plugin.Host) []model.Link {
	r := h.Links()
	out := []model.Link{}
	for _, kind := range []string{"ProxmoxUI", "HostShell", "Host"} {
		out = append(out, r.Resolve(plugin.LinkRequest{Kind: kind})...)
	}
	return out
}

// shellLinks are the links the attention items carry: the hypervisor UI and shell.
func shellLinks(h plugin.Host) []model.Link {
	r := h.Links()
	return append(r.Resolve(plugin.LinkRequest{Kind: "ProxmoxUI"}), r.Resolve(plugin.LinkRequest{Kind: "HostShell"})...)
}

// hostData is the topic's payload without its links: the last good values with the settings and
// the names of the moment. ok is false while the host's nodename is unknown.
func (p *Plugin) hostData(rt *runtime) (model.HostData, bool) {
	n, ok := rt.nodename()
	if !ok || !rt.hasHost() {
		return model.HostData{}, false
	}
	p.mu.Lock()
	vals := p.host.vals
	p.mu.Unlock()
	return buildHostData(vals, rt.cfg, rt.hostName(n)), true
}

// buildHostData adds to the polled values what the plugin owns: the name and ref, the model,
// the layout settings, the thresholds and the health they give, and a ref on every pool and
// disk. Lists that were never filled are empty, not null. vals is not changed.
func buildHostData(vals model.HostData, cfg pluginconf.Hardware, name string) model.HostData {
	d := vals
	h := cfg.Hosts[0]
	d.Name, d.Ref, d.Model = name, plugin.Ref(ID, "Host", name), h.Model
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
