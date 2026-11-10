package hardware

import (
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// builtHost is a host payload as the plugin builds it, with the default thresholds.
func builtHost(mod func(*model.HostData)) model.HostData {
	vals := model.HostData{
		PackageTempsC: []float64{60}, PowerWatts: 200, MemTotalBytes: 100, MemUsedBytes: 25,
		Pools: []model.Pool{{Name: "tank", State: "ONLINE"}},
		Disks: []model.Disk{{Bay: "3", Serial: "SER3", Device: "sdc", Healthy: true}},
	}
	if mod != nil {
		mod(&vals)
	}
	cfg := pluginconf.DefaultHardware()
	cfg.Hosts = []pluginconf.HardwareHost{{}}
	return buildHostData(vals, cfg.Normalized(), "h1", "h1")
}

func ids(items []model.AttentionItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestHealthyHostRaisesNothing(t *testing.T) {
	if items := attentionOf(builtHost(nil), nil); len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
}

// The rule table: ids, severities, sources, titles, details, refs, the route and the plugin.
func TestAttentionRules(t *testing.T) {
	scrubbed := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	d := builtHost(func(h *model.HostData) {
		h.PackageTempsC = []float64{70, 91}
		h.Pools = []model.Pool{
			{Name: "tank", State: "DEGRADED"},
			{Name: "rpool", State: "ONLINE", ScrubErrors: 3, LastScrubEnd: &scrubbed},
		}
		h.Disks = []model.Disk{{Bay: "3", Serial: "SER3", Device: "sdc", Healthy: false}, {Bay: "4", Healthy: true}}
	})
	link := model.Link{Label: "Proxmox", Href: "https://pve.example.com"}
	items := attentionOf(d, []model.Link{link})
	if got, want := ids(items), []string{"zfs:pool:tank", "zfs:scrub:rpool", "smart:3", "host:cpu-temp"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	byID := map[string]model.AttentionItem{}
	for _, it := range items {
		byID[it.ID] = it
		if it.Plugin != "hardware" || it.Route != "/hardware" || len(it.Links) != 1 || it.Links[0] != link {
			t.Errorf("%s: plugin %q route %q links %v", it.ID, it.Plugin, it.Route, it.Links)
		}
	}
	for id, c := range map[string]struct {
		sev                   model.Severity
		source, title, detail string
		ref                   string
	}{
		"zfs:pool:tank":   {model.SeverityCrit, "zfs", "ZFS pool tank is DEGRADED", "", "hardware:Pool/h1/tank"},
		"zfs:scrub:rpool": {model.SeverityWarn, "zfs", "ZFS pool rpool last scrub found errors", "3 errors", "hardware:Pool/h1/rpool"},
		"smart:3":         {model.SeverityCrit, "smart", "Disk in bay 3 reports SMART failure", "sdc SER3", "hardware:Disk/h1/SER3"},
		"host:cpu-temp":   {model.SeverityCrit, "hardware", "CPU package at 91 °C", "", "hardware:Host/h1"},
	} {
		it := byID[id]
		if it.Severity != c.sev || it.Source != c.source || it.Title != c.title || it.Detail != c.detail || it.Ref != c.ref {
			t.Errorf("%s = %+v, want %+v", id, it, c)
		}
	}
	if !byID["zfs:scrub:rpool"].Since.Equal(scrubbed) {
		t.Errorf("the scrub item's since = %v, want the scrub's end", byID["zfs:scrub:rpool"].Since)
	}
	if !byID["zfs:pool:tank"].Since.IsZero() {
		t.Error("an item with no natural time has a since before stamping")
	}
	items[0].Links[0].Label = "changed"
	if items[1].Links[0].Label != "Proxmox" {
		t.Error("the items share one links slice")
	}
}

// A pool that is online with no errors and a healthy disk are not items; a warm CPU is a warning.
func TestOnlinePoolIsCaseInsensitiveAndWarmCPUWarns(t *testing.T) {
	d := builtHost(func(h *model.HostData) {
		h.Pools = []model.Pool{{Name: "tank", State: "Online"}}
		h.PackageTempsC = []float64{86}
	})
	items := attentionOf(d, nil)
	if got := ids(items); !slices.Equal(got, []string{"host:cpu-temp"}) || items[0].Severity != model.SeverityWarn {
		t.Errorf("items = %+v", items)
	}
}

// No CPU reading, no CPU item.
func TestNoPackageTempsNoCPUItem(t *testing.T) {
	d := builtHost(func(h *model.HostData) { h.PackageTempsC = nil })
	if items := attentionOf(d, nil); len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
}

// Since of an item with no time of its own is the first time it was seen, and it is stable
// across calls; an item that recovers and fails again starts over.
func TestAttentionSinceIsStableAcrossCalls(t *testing.T) {
	r := hostRig(t, nil)
	r.q.set(r.ex.packageTemps(node), sample(95, "sensor", "temp1"))
	r.q.set(r.ex.smartHealthy(node), sample(0, "bay", "2", "serial", "SER2", "disk", "sdb"))
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool {
		return len(d.PackageTempsC) == 1 && len(d.Disks) == 1 && !d.Disks[0].Healthy
	})
	c := contributor(t, r)

	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	first := c.Attention.Attention(t0)
	again := c.Attention.Attention(t0.Add(3 * time.Hour))
	if len(first) != 2 || len(again) != 2 {
		t.Fatalf("items: %v then %v", ids(first), ids(again))
	}
	for i := range first {
		if !first[i].Since.Equal(t0) || !again[i].Since.Equal(t0) {
			t.Errorf("%s: since %v then %v, want both %v", first[i].ID, first[i].Since, again[i].Since, t0)
		}
	}

	r.q.set(r.ex.packageTemps(node), sample(60, "sensor", "temp1"))
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return len(d.PackageTempsC) == 1 && d.PackageTempsC[0] == 60 })
	t1 := t0.Add(5 * time.Hour)
	if got := ids(c.Attention.Attention(t1)); !slices.Equal(got, []string{"smart:2"}) {
		t.Fatalf("after the CPU recovered: %v", got)
	}
	r.q.set(r.ex.packageTemps(node), sample(96, "sensor", "temp1"))
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return len(d.PackageTempsC) == 1 && d.PackageTempsC[0] == 96 })
	t2 := t0.Add(7 * time.Hour)
	for _, it := range c.Attention.Attention(t2) {
		want := t0
		if it.ID == "host:cpu-temp" {
			want = t2
		}
		if !it.Since.Equal(want) {
			t.Errorf("%s: since %v, want %v (the CPU item starts over)", it.ID, it.Since, want)
		}
	}
}

// A host that has polled and is healthy has nothing to raise, which the contributor says with a
// non-nil empty list: nil is "no opinion yet".
func TestAttentionOfAHealthyHostIsNonNilEmpty(t *testing.T) {
	r := hostRig(t, nil)
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts > 0 })
	c := contributor(t, r)
	plugintest.AssertAttentionNonNil(t, c)
	if items := c.Attention.Attention(time.Now()); len(items) != 0 {
		t.Errorf("items = %+v, want none", ids(items))
	}
}

// The items carry the links the providers give for the Proxmox UI and shell.
func TestAttentionLinksComeFromTheLinkProviders(t *testing.T) {
	p, rt, h := stubRuntime(t, withHost(pluginconf.HardwareHost{Nodename: node}))
	q := newFakeQuerier()
	q.fillHost(rt.q, node, "")
	q.set(rt.q.packageTemps(node), sample(99, "sensor", "temp1"))
	h.setService("prometheus", q)
	p.hostTick(t.Context(), rt)
	got := p.Attention(time.Now())
	if len(got) != 1 || got[0].Links == nil {
		t.Fatalf("items = %+v, want one with a non-null links list", got)
	}
}

// KPIs and topology exist only once the host has polled.
func TestContributionsWaitForTheFirstPoll(t *testing.T) {
	p, rt, h := stubRuntime(t, withHost(pluginconf.HardwareHost{Nodename: node}))
	if p.KPIs() != nil || p.Attention(time.Now()) != nil || len(p.Topology().Hosts) != 0 {
		t.Error("the plugin contributes before it has polled")
	}
	q := newFakeQuerier()
	q.fillHost(rt.q, node, "")
	h.setService("prometheus", q)
	p.hostTick(t.Context(), rt)

	kpis := p.KPIs()
	if len(kpis) != 2 || kpis[0].Key != "power" || kpis[0].Value != 214 || kpis[0].Unit != "W" || kpis[1].Key != "cpuTemp" || kpis[1].Value != 62 || kpis[1].Unit != "°C" {
		t.Errorf("kpis = %+v", kpis)
	}
	for _, k := range kpis {
		if k.Plugin != "hardware" || k.Label == "" {
			t.Errorf("kpi %+v", k)
		}
	}
	top := p.Topology()
	if h := hostOf(top); len(top.Hosts) != 1 || h.Name != node || h.Node != node || h.PowerWatts != 214 || h.CPUTempC != 62 || h.MemUsedRatio != 0.25 || h.Health != model.HealthOK {
		t.Errorf("topology hosts = %+v", top.Hosts)
	}
	if top.VMs == nil || top.Groups == nil {
		t.Error("the topology lists are null")
	}
}

// hostOf is the one host of a topology; the zero host when there is not exactly one.
func hostOf(t model.Topology) model.TopologyHost {
	if len(t.Hosts) != 1 {
		return model.TopologyHost{}
	}
	return t.Hosts[0]
}

// The topology host's health is the worst of the plugin's own items.
func TestTopologyHealthIsTheWorstItem(t *testing.T) {
	d := builtHost(func(h *model.HostData) { h.PackageTempsC = []float64{86} }) // warn
	if got := hostOf(topologyOf(d, attentionOf(d, nil))).Health; got != model.HealthWarn {
		t.Errorf("warm: %s", got)
	}
	d = builtHost(func(h *model.HostData) {
		h.PackageTempsC = []float64{86}
		h.Pools = []model.Pool{{Name: "tank", State: "FAULTED"}}
	})
	if got := hostOf(topologyOf(d, attentionOf(d, nil))).Health; got != model.HealthCrit {
		t.Errorf("faulted pool: %s", got)
	}
	if got := hostOf(topologyOf(builtHost(nil), nil)).Health; got != model.HealthOK {
		t.Errorf("healthy: %s", got)
	}
}

// The plugin implements the three overview sources.
func TestPluginIsAContributor(t *testing.T) {
	r := hostRig(t, nil)
	c := contributor(t, r)
	if c.Attention == nil || c.KPIs == nil || c.Topology == nil || c.Sources == nil {
		t.Errorf("contributor = %+v", c)
	}
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts > 0 })
	eventually(t, "the sources to name prometheus", func() bool { return hasSource(c.Sources(), "prometheus", true, "") })
}
