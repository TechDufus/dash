package hardware

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/queries"
)

// twoHosts are a Proxmox node with its nodename (pve-1) and a box outside the cluster with its
// own name (box-2, called Rack 2).
var twoHosts = withHosts(
	pluginconf.HardwareHost{Nodename: node, Model: "Example 2U server", Bays: 16},
	pluginconf.HardwareHost{Name: "Rack 2", Nodename: "box-2", Model: "Example 1U server", Bays: 4},
)

func twoHostRig(t *testing.T) *rig {
	t.Helper()
	return newRig(t, rigOpts{cfg: twoHosts, prefill: func(r *rig) {
		r.node.setNodes(node, "pve-2")
		r.q.fillHost(r.ex, node, "")
		r.q.fillHost(r.ex, "box-2", "")
		r.q.set(r.ex.power("box-2"), sample(100))
		r.q.set(r.ex.packageTemps("box-2"), sample(87, "sensor", "temp1"))
		r.q.set(r.ex.pools("box-2"), sample(1, "zpool", "tank", "state", "DEGRADED"))
	}})
}

// Every host is read and published in configuration order with its own identity: the Proxmox node
// by its nodename, the outside box by its own; names, refs and nodes never mix.
func TestSeveralHostsArePublishedWithTheirIdentity(t *testing.T) {
	r := twoHostRig(t)
	d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostsData) bool { return len(d.Hosts) == 2 })
	a, b := d.Hosts[0], d.Hosts[1]
	if a.Name != node || a.Node != node || a.Ref != "hardware:Host/"+node || a.PowerWatts != 214 || a.Bays != 16 {
		t.Errorf("first host = %+v", a)
	}
	if b.Name != "Rack 2" || b.Node != "box-2" || b.Ref != "hardware:Host/Rack 2" || b.PowerWatts != 100 || b.Bays != 4 || b.Model != "Example 1U server" {
		t.Errorf("second host = %+v", b)
	}
	if b.Pools[0].Ref != "hardware:Pool/Rack 2/tank" || a.Health != model.HealthOK || b.Health != model.HealthCrit {
		t.Errorf("pool ref %q, health %s / %s", b.Pools[0].Ref, a.Health, b.Health)
	}
}

// KPIs sum the power and take the hottest package with the worst health; attention ids, titles and
// routes name their host; topology has a host each with its own health.
func TestSeveralHostsContribute(t *testing.T) {
	r := twoHostRig(t)
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostsData) bool { return len(d.Hosts) == 2 })
	c := contributor(t, r)

	kpis := map[string]model.KPI{}
	for _, k := range c.KPIs.KPIs() {
		kpis[k.Key] = k
	}
	if kpis["power"].Value != 314 || kpis["cpuTemp"].Value != 87 || kpis["cpuTemp"].Health != model.HealthWarn {
		t.Errorf("kpis = %+v, want power 214+100 and the hotter host's 87 °C warning", kpis)
	}
	items := c.Attention.Attention(time.Now())
	if got := ids(items); !slices.Equal(got, []string{"zfs:pool:Rack 2/tank", "host:cpu-temp:Rack 2"}) {
		t.Fatalf("ids = %v", got)
	}
	if items[0].Route != "/hardware/Rack%202" || items[0].Title != "ZFS pool tank on Rack 2 is DEGRADED" || items[1].Title != "CPU package at 87 °C on Rack 2" {
		t.Errorf("items = %+v", items)
	}
	top := c.Topology.Topology()
	if len(top.Hosts) != 2 || top.Hosts[0].Health != model.HealthOK || top.Hosts[1].Health != model.HealthCrit || top.Hosts[1].Node != "box-2" {
		t.Errorf("topology hosts = %+v", top.Hosts)
	}
}

// A failing host poll fails the one prometheus source and the row names that host. While the
// other host polls fine the row is degraded; with every host failing it is an error.
func TestFailingHostIsNamedInTheRow(t *testing.T) {
	r := twoHostRig(t)
	r.waitState(model.PluginHealthy)
	r.q.setErr(r.ex.power("box-2"), errors.New("boom"))
	st := r.waitState(model.PluginDegraded)
	if st.Message != "prometheus: Rack 2: unreachable" {
		t.Errorf("one host failing: message = %q", st.Message)
	}
	r.q.setErr(r.ex.power(node), errors.New("boom"))
	st = r.waitState(model.PluginError)
	if st.Message != "prometheus: "+node+": unreachable; prometheus: Rack 2: unreachable" {
		t.Errorf("every host failing: message = %q", st.Message)
	}
}

// The first publish holds every host that can be read, and a host waiting for its Proxmox node
// does not hold it up; with no host that can be read the list is published empty at once.
func TestFirstHostsPublishWaitsForEveryRunnableHost(t *testing.T) {
	p, rt, h := stubRuntime(t, withHosts(pluginconf.HardwareHost{Nodename: node}, pluginconf.HardwareHost{Nodename: "box-2"}))
	q := newFakeQuerier()
	q.fillHost(rt.q, node, "")
	q.fillHost(rt.q, "box-2", "")
	h.setService("prometheus", q)
	ids := rt.hostIDs()
	p.mu.Lock()
	p.hostAt(0).polled = true // the first host is back, the second still polling
	p.mu.Unlock()
	p.publishHosts(rt, ids, true)
	if n := len(h.published("hosts")); n != 0 {
		t.Fatalf("published %d times with a host still on its first poll", n)
	}
	p.hostTick(context.Background(), rt)
	if pubs := h.published("hosts"); len(pubs) != 1 || len(pubs[0].data.(model.HostsData).Hosts) != 2 {
		t.Errorf("first publish = %+v, want both hosts", pubs)
	}

	// One runnable host and one waiting for its node: published after the runnable one's poll.
	p2, rt2, h2 := stubRuntime(t, withHosts(pluginconf.HardwareHost{Nodename: node}, pluginconf.HardwareHost{}))
	h2.setService("prometheus", q) // no proxmox: the second host's node is unknown
	p2.mu.Lock()
	p2.hostAt(0).last = h2.Now() // its first poll "started" and has not ended
	p2.hostAt(0).possible = true
	p2.mu.Unlock()
	p2.hostTick(context.Background(), rt2)
	if n := len(h2.published("hosts")); n != 0 {
		t.Fatalf("published %d times before the runnable host's first poll ended", n)
	}
	h2.advance(time.Minute)
	p2.hostTick(context.Background(), rt2)
	if pubs := h2.published("hosts"); len(pubs) != 1 || len(pubs[0].data.(model.HostsData).Hosts) != 1 {
		t.Errorf("publish with a host waiting for its node = %+v, want the readable one", pubs)
	}

	// No host can be read (the only one waits for its node) next to a machine: an empty list at
	// once, so the page does not wait for the topic.
	p3, rt3, h3 := stubRuntime(t, func(c *pluginconf.Hardware) {
		c.Hosts = []pluginconf.HardwareHost{{}}
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
	})
	h3.setService("prometheus", q)
	p3.hostTick(context.Background(), rt3)
	pubs := h3.published("hosts")
	if len(pubs) != 1 || pubs[0].data.(model.HostsData).Hosts == nil || len(pubs[0].data.(model.HostsData).Hosts) != 0 ||
		!hasSource(pubs[0].srcs, "proxmox", false, "not_configured") {
		t.Errorf("publish with no runnable host = %+v, want an empty list naming the missing node", pubs)
	}
}

// While the proxmox plugin restarts and names no node, the host without a nodename keeps the one
// it last had: it stays in the payload, with the same name.
func TestHostWithoutNodenameSurvivesAProxmoxRestart(t *testing.T) {
	_, rt, h := stubRuntime(t, withHosts(pluginconf.HardwareHost{}))
	n := &fakeNode{name: node, ok: true}
	h.setService("proxmox", n)
	if ids := rt.hostIDs(); ids[0].nodename != node || ids[0].name != node {
		t.Fatalf("ids = %+v", ids)
	}
	n.set("", false)
	if ids := rt.hostIDs(); !ids[0].runnable() || ids[0].nodename != node || ids[0].name != node || ids[0].node != node {
		t.Errorf("ids during the restart = %+v, want the last nodename kept", ids)
	}
	n.set("pve-9", true)
	if ids := rt.hostIDs(); ids[0].nodename != "pve-9" {
		t.Errorf("ids after the restart = %+v, want the node the plugin names now", ids)
	}
}

// Two hosts that resolve to one nodename at runtime (a host without a nodename whose Proxmox node
// another host names) never merge: the later one is not read and the row says which.
func TestRuntimeDuplicateHostIsNotRead(t *testing.T) {
	r := newRig(t, rigOpts{cfg: withHosts(pluginconf.HardwareHost{Nodename: node}, pluginconf.HardwareHost{}), prefill: func(r *rig) {
		r.q.fillHost(r.ex, node, "")
	}})
	d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostsData) bool { return len(d.Hosts) > 0 })
	if len(d.Hosts) != 1 {
		t.Errorf("hosts = %+v, want the first only", d.Hosts)
	}
	st := r.waitState(model.PluginDegraded)
	if !strings.Contains(st.Message, "hosts entry 1 ("+node+") resolves to the nodename or name of entry 0 and is not read") {
		t.Errorf("message = %q", st.Message)
	}
}

// A nodename that is a node's DNS name is that node (exact names first); while the cluster's
// nodes are unknown a host keeps the node it last resolved.
func TestHostNodeFromTheClusterNodes(t *testing.T) {
	_, rt, h := stubRuntime(t, withHosts(pluginconf.HardwareHost{Nodename: "pve-1.example.com"}, pluginconf.HardwareHost{Nodename: "pve-2"}))
	n := &fakeNode{}
	n.setNodes("pve-1", "PVE-2")
	h.setService("proxmox", n)
	ids := rt.hostIDs()
	if ids[0].node != "pve-1" || ids[0].name != "pve-1.example.com" || ids[1].node != "PVE-2" {
		t.Errorf("ids = %+v", ids)
	}
	n.setNodes() // a restarted proxmox plugin that has not read the cluster yet
	if ids := rt.hostIDs(); ids[0].node != "pve-1" || ids[1].node != "PVE-2" {
		t.Errorf("ids while the nodes are unknown = %+v, want the last resolved", ids)
	}
}

// The node argument picks a host by node, then nodename, then name, ignoring case; an unknown
// one is not found. Without it one host is that host; several hosts sum the power, take the
// hottest package and draw the other series one line per host.
func TestSeriesOfSeveralHosts(t *testing.T) {
	x := defaultExprs()
	hosts := []hostID{
		{i: 0, nodename: "pve-1.example.com", node: "pve-1", name: "Rack 1", dupOf: -1},
		{i: 1, nodename: "box-2", node: "box-2", name: "box-2", dupOf: -1},
	}
	for arg, want := range map[string]string{"pve-1": "pve-1.example.com", "PVE-1.example.com": "pve-1.example.com", "rack 1": "pve-1.example.com", "box-2": "box-2"} {
		got, err := seriesOf(x, hosts, 2, "host.fans", plugin.SeriesArgs{Instance: arg})
		if err != nil || got != x.fans(want) {
			t.Errorf("node=%s: %q (%v), want %s's", arg, got, err, want)
		}
	}
	if _, err := seriesOf(x, hosts, 2, "host.cpu", plugin.SeriesArgs{Instance: "pve-9"}); !errors.Is(err, plugin.ErrNotFound) {
		t.Errorf("an unknown host: %v, want not found", err)
	}
	re := `nodename=~"^(pve-1\\.example\\.com|box-2)$"`
	for name, want := range map[string]string{
		"host.power":   `sum(ipmi_power_watts{` + re + `})`,
		"host.cpuTemp": `max(node_hwmon_temp_celsius{` + re + `,chip=~"platform_coretemp_.*",sensor="temp1"})`,
		"host.cpu":     `1 - avg by (nodename) (rate(node_cpu_seconds_total{` + re + `,mode="idle"}[2m]))`,
		"host.fans":    `ipmi_fan_speed_rpm{` + re + `}`,
	} {
		if got, err := seriesOf(x, hosts, 2, name, plugin.SeriesArgs{}); err != nil || got != want {
			t.Errorf("%s without a node = %q (%v), want %q", name, got, err, want)
		}
	}
	if got, err := seriesOf(x, hosts[:1], 1, "host.cpu", plugin.SeriesArgs{}); err != nil || got != x.cpu("pve-1.example.com") {
		t.Errorf("one host without a node = %q (%v)", got, err)
	}
	if _, err := seriesOf(x, nil, 2, "host.power", plugin.SeriesArgs{}); !errors.Is(err, queries.ErrBadSeries) {
		t.Errorf("no host known: %v", err)
	}
}

// A host's context names its series by its node, and a disk or pool relates its own host only.
func TestHostContextIsPerHost(t *testing.T) {
	r := twoHostRig(t)
	d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostsData) bool { return len(d.Hosts) == 2 })
	objs, ok := r.sup.Objects(ID)
	if !ok {
		t.Fatal("the plugin serves no objects")
	}
	part, err := objs.Context(context.Background(), d.Hosts[1].Ref, model.ContextOptions{})
	if err != nil || len(part.Series) == 0 || part.Series[0].Args["node"] != "box-2" || !slices.Contains(part.Related, d.Hosts[1].Pools[0].Ref) {
		t.Errorf("host context = %+v (%v)", part, err)
	}
	pool, err := objs.Context(context.Background(), d.Hosts[1].Pools[0].Ref, model.ContextOptions{})
	if err != nil || !slices.Equal(pool.Related, []string{d.Hosts[1].Ref}) {
		t.Errorf("pool context related = %v (%v), want its own host only", pool.Related, err)
	}
}
