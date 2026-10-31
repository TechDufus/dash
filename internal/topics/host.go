package topics

import (
	"context"
	"sort"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/upstream"
)

type hostSnap struct{ data model.HostData }

func (m *Manager) pollHost(ctx context.Context) {
	n, scrub := m.d.Cfg.HostNodename, m.d.Cfg.Conventions.ZFSScrubMetricPrefix
	m.mu.RLock()
	d := m.host.data // last good values survive a failed query
	m.mu.RUnlock()
	d.Name = m.d.Cfg.ProxmoxNode

	if m.d.PVE.Enabled() {
		st, err := m.d.PVE.NodeStatus(ctx)
		m.setSource("host:proxmox", err)
		if err == nil {
			d.UptimeSeconds, d.PVEVersion, d.Kernel, d.CPUModel = st.UptimeSeconds, st.PVEVersion, st.Kernel, st.CPUModel
		}
	} else {
		m.setSource("host:proxmox", upstream.ErrDisabled)
	}

	res := m.queryAll(ctx, map[string]string{
		"cpu":      queries.HostCPU(n),
		"memTotal": queries.HostMemTotal(n),
		"memAvail": queries.HostMemAvailable(n),
		"pkg":      queries.HostPackageTemps(n),
		"sensors":  queries.HostSensors(n),
		"fans":     queries.HostFans(n),
		"power":    queries.HostPower(n),
		"psu":      queries.HostPSUs(n),
		"sel":      queries.HostSELFree(n),
		"pools":    queries.HostPools(n),
		"scrubEnd": queries.HostScrubEnd(scrub, n),
		"scrubErr": queries.HostScrubErrors(scrub, n),
		"scrubRun": queries.HostScrubRunning(scrub, n),
		"healthy":  queries.HostSMARTHealthy(n),
		"temp":     queries.HostSMARTTemp(n),
		"life":     queries.HostSMARTLifeRemain(n),
		"wear":     queries.HostSMARTWearout(n),
	})
	m.setSource("host:victoriametrics", firstErr(res))
	ok := func(k string) ([]upstream.Sample, bool) { return res[k].samples, res[k].err == nil }

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

	m.mu.Lock()
	m.host.data = d
	m.mu.Unlock()
	m.mark(model.TopicHost)
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
			p.LastScrubEnd = new(time.Unix(int64(e), 0).UTC())
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
			d.TempC = new(t)
		}
		if l, has := lives[bay]; has {
			d.LifeRemainingPct = new(l)
		} else if w, has := wears[bay]; has {
			d.LifeRemainingPct = new(w)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return atoi(out[i].Bay) < atoi(out[j].Bay) })
	return out
}

func (m *Manager) buildHost() model.HostData {
	m.mu.RLock()
	d := m.host.data
	m.mu.RUnlock()
	d.Name = m.d.Cfg.ProxmoxNode
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
	if d.Pools == nil {
		d.Pools = []model.Pool{}
	}
	if d.Disks == nil {
		d.Disks = []model.Disk{}
	}
	d.Links = append(append(m.d.Links.ProxmoxUI(), m.d.Links.HostShell()...), m.d.Links.Grafana()...)
	return d
}
