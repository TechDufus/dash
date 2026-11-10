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

// routeHardware is the Hardware page; each host's items point at its own page below it
// (hostRoute).
const routeHardware = "/hardware"

// The sources of the attention items, as the Attention list has always shown them.
const (
	sourceZFS      = "zfs"
	sourceSMART    = "smart"
	sourceHardware = "hardware"
)

// current is every host's payload as it is now, without links: the hosts that have polled and
// whose nodename is known, in configuration order.
func (p *Plugin) current() ([]model.HostData, *runtime) {
	rt := p.rt.Load()
	if rt == nil {
		return nil, nil
	}
	return p.hostsData(rt, rt.hostIDs()), rt
}

// Attention raises, per host, an item for a ZFS pool that is not online or whose last scrub
// found errors, a disk that fails SMART and a CPU package at or above the warning temperature.
// Items without a time of their own are stamped by the supervisor with the moment they were first
// seen. It has no opinion (nil) before the first host has polled.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	hosts, rt := p.current()
	if len(hosts) == 0 {
		return nil
	}
	items := []model.AttentionItem{}
	for _, d := range hosts {
		items = append(items, attentionOf(d, shellLinks(rt.h, d.Node))...)
	}
	return items
}

// attentionOf is the rule table over one host's payload, in the order pools, disks, CPU. The ids
// carry the host's name, so two hosts' pools, bays and CPUs never share an item or a first-seen
// time, and every item points at the host's page. links is copied on every item. Items that have
// no time of their own have a zero Since.
func attentionOf(d model.HostData, links []model.Link) []model.AttentionItem {
	items := []model.AttentionItem{}
	route := hostRoute(d.Name)
	add := func(id string, sev model.Health, source, title, detail string, since time.Time, ref string) {
		if sev != model.HealthWarn && sev != model.HealthCrit {
			return
		}
		items = append(items, model.AttentionItem{
			ID: id, Severity: model.Severity(sev), Source: source, Plugin: string(ID), Title: title, Detail: detail,
			Since: since, Route: route, Links: append([]model.Link{}, links...), Ref: ref,
		})
	}
	for _, p := range d.Pools {
		if !strings.EqualFold(p.State, "online") {
			add("zfs:pool:"+d.Name+"/"+p.Name, model.HealthCrit, sourceZFS, fmt.Sprintf("ZFS pool %s on %s is %s", p.Name, d.Name, p.State), "", time.Time{}, p.Ref)
		}
		if p.ScrubErrors > 0 {
			var since time.Time
			if p.LastScrubEnd != nil {
				since = *p.LastScrubEnd
			}
			add("zfs:scrub:"+d.Name+"/"+p.Name, model.HealthWarn, sourceZFS, fmt.Sprintf("ZFS pool %s on %s: last scrub found errors", p.Name, d.Name),
				fmt.Sprintf("%d errors", p.ScrubErrors), since, p.Ref)
		}
	}
	for _, x := range d.Disks {
		if !x.Healthy {
			add("smart:"+d.Name+"/"+x.Bay, model.HealthCrit, sourceSMART, fmt.Sprintf("Disk in bay %s of %s reports SMART failure", x.Bay, d.Name),
				x.Device+" "+x.Serial, time.Time{}, x.Ref)
		}
	}
	if len(d.PackageTempsC) > 0 {
		t := maxOf(d.PackageTempsC)
		add("host:cpu-temp:"+d.Name, tempHealth(d.TempWarnC, d.TempCritC, t), sourceHardware,
			fmt.Sprintf("CPU package at %.0f °C on %s", t, d.Name), "", time.Time{}, d.Ref)
	}
	return items
}

// KPIs are the hosts' power draw, summed, and their hottest CPU package. The temperature's health
// comes from the plugin's thresholds. There are none before a host has polled.
func (p *Plugin) KPIs() []model.KPI {
	hosts, _ := p.current()
	return kpisOf(hosts)
}

// kpisOf sums the power of every host and takes the hottest package of all of them with the
// worst CPU temperature health, so one host gives its own values. nil without a host.
func kpisOf(hosts []model.HostData) []model.KPI {
	if len(hosts) == 0 {
		return nil
	}
	var power, temp float64
	health := model.HealthOK
	for i, d := range hosts {
		power += d.PowerWatts
		if t := maxOf(d.PackageTempsC); i == 0 || t > temp {
			temp = t
		}
		health = worst(health, d.CPUTempHealth)
	}
	return []model.KPI{
		{Key: kpiPower, Plugin: string(ID), Label: "Power", Unit: "W", Value: power, Health: model.HealthOK},
		{Key: kpiCPUTemp, Plugin: string(ID), Label: "CPU temp", Unit: "°C", Value: temp, Health: health},
	}
}

// Topology is one host per configured host that has polled, each with its own health (the worst
// of its own items). It is empty before a host has polled.
func (p *Plugin) Topology() model.Topology {
	hosts, _ := p.current()
	if len(hosts) == 0 {
		return model.Topology{}
	}
	return topologyOf(hosts)
}

func topologyOf(hosts []model.HostData) model.Topology {
	t := model.Topology{Hosts: []model.TopologyHost{}, VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	for _, d := range hosts {
		mem := 0.0
		if d.MemTotalBytes > 0 {
			mem = float64(d.MemUsedBytes) / float64(d.MemTotalBytes)
		}
		t.Hosts = append(t.Hosts, model.TopologyHost{
			Name: d.Name, Node: d.Node, Health: d.Health, PowerWatts: d.PowerWatts, CPUTempC: maxOf(d.PackageTempsC),
			MemUsedRatio: mem, CPUTempHealth: d.CPUTempHealth,
		})
	}
	return t
}
