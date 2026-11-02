package hardware

import (
	"fmt"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// The keys of the plugin's KPIs.
const (
	kpiPower   = "power"
	kpiCPUTemp = "cpuTemp"
)

// routeHardware is where every item of the plugin points.
const routeHardware = "/hardware"

// The sources of the attention items, as the Attention list has always shown them.
const (
	sourceZFS      = "zfs"
	sourceSMART    = "smart"
	sourceHardware = "hardware"
)

// current is the host's payload as it is now, without its links, and false until the host has
// polled and its nodename is known.
func (p *Plugin) current() (model.HostData, *runtime, bool) {
	rt := p.rt.Load()
	if rt == nil {
		return model.HostData{}, nil, false
	}
	p.mu.Lock()
	polled := p.host.polled
	p.mu.Unlock()
	if !polled {
		return model.HostData{}, rt, false
	}
	d, ok := p.hostData(rt)
	return d, rt, ok
}

// Attention raises an item for a ZFS pool that is not online or whose last scrub found errors, a
// disk that fails SMART and a CPU package at or above the warning temperature. Items without a
// time of their own are stamped with the moment they were first seen.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	d, rt, ok := p.current()
	if !ok {
		return nil
	}
	return p.firstSeen.Stamp(now, attentionOf(d, shellLinks(rt.h)))
}

// attentionOf is the rule table over a host payload, in the order pools, disks, CPU. links is
// copied on every item. Items that have no time of their own have a zero Since.
func attentionOf(d model.HostData, links []model.Link) []model.AttentionItem {
	items := []model.AttentionItem{}
	add := func(id string, sev model.Health, source, title, detail string, since time.Time, ref string) {
		if sev != model.HealthWarn && sev != model.HealthCrit {
			return
		}
		items = append(items, model.AttentionItem{
			ID: id, Severity: model.Severity(sev), Source: source, Plugin: string(ID), Title: title, Detail: detail,
			Since: since, Route: routeHardware, Links: append([]model.Link{}, links...), Ref: ref,
		})
	}
	for _, p := range d.Pools {
		if !strings.EqualFold(p.State, "online") {
			add("zfs:pool:"+p.Name, model.HealthCrit, sourceZFS, fmt.Sprintf("ZFS pool %s is %s", p.Name, p.State), "", time.Time{}, p.Ref)
		}
		if p.ScrubErrors > 0 {
			var since time.Time
			if p.LastScrubEnd != nil {
				since = *p.LastScrubEnd
			}
			add("zfs:scrub:"+p.Name, model.HealthWarn, sourceZFS, fmt.Sprintf("ZFS pool %s last scrub found errors", p.Name),
				fmt.Sprintf("%d errors", p.ScrubErrors), since, p.Ref)
		}
	}
	for _, x := range d.Disks {
		if !x.Healthy {
			add("smart:"+x.Bay, model.HealthCrit, sourceSMART, fmt.Sprintf("Disk in bay %s reports SMART failure", x.Bay),
				x.Device+" "+x.Serial, time.Time{}, x.Ref)
		}
	}
	if len(d.PackageTempsC) > 0 {
		t := maxOf(d.PackageTempsC)
		add("host:cpu-temp", tempHealth(d.TempWarnC, d.TempCritC, t), sourceHardware,
			fmt.Sprintf("CPU package at %.0f °C", t), "", time.Time{}, d.Ref)
	}
	return items
}

// KPIs are the host's power draw and its hottest CPU package. The temperature's health comes
// from the plugin's thresholds. There are none before the host has polled.
func (p *Plugin) KPIs() []model.KPI {
	d, _, ok := p.current()
	if !ok {
		return nil
	}
	return kpisOf(d)
}

func kpisOf(d model.HostData) []model.KPI {
	return []model.KPI{
		{Key: kpiPower, Plugin: string(ID), Label: "Power", Unit: "W", Value: d.PowerWatts, Health: model.HealthOK},
		{Key: kpiCPUTemp, Plugin: string(ID), Label: "CPU temp", Unit: "°C", Value: maxOf(d.PackageTempsC), Health: d.CPUTempHealth},
	}
}

// Topology is the host, its health the worst of the plugin's own items (every one of them points
// at /hardware). It is empty before the host has polled.
func (p *Plugin) Topology() model.Topology {
	d, _, ok := p.current()
	if !ok {
		return model.Topology{}
	}
	return topologyOf(d, attentionOf(d, nil))
}

func topologyOf(d model.HostData, items []model.AttentionItem) model.Topology {
	health := model.HealthOK
	for _, it := range items {
		if it.Route == routeHardware {
			health = worst(health, model.Health(it.Severity))
		}
	}
	mem := 0.0
	if d.MemTotalBytes > 0 {
		mem = float64(d.MemUsedBytes) / float64(d.MemTotalBytes)
	}
	return model.Topology{
		Host: model.TopologyHost{
			Name: d.Name, Health: health, PowerWatts: d.PowerWatts, CPUTempC: maxOf(d.PackageTempsC),
			MemUsedRatio: mem, CPUTempHealth: d.CPUTempHealth,
		},
		VMs:    []model.TopologyVM{},
		Groups: []model.TopologyGroup{},
	}
}
