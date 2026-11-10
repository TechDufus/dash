package proxmox

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/upstream"
)

func byID(d model.VMsData, id int) model.VM {
	for _, vm := range d.VMs {
		if vm.VMID == id {
			return vm
		}
	}
	return model.VM{}
}

// A stopped VM matching criticalVMs by id or by tag (without regard to case) is critical, any
// other stopped VM warns, templates and running VMs are fine, and the flag is on the VM.
func TestCriticalVMsByIDAndTag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      pluginconf.CriticalVMs
		critical map[int]bool
		health   map[int]model.Health
	}{
		{"nothing is critical by default", pluginconf.CriticalVMs{},
			map[int]bool{}, map[int]model.Health{101: "ok", 102: "warn", 103: "warn", 9000: "ok"}},
		{"by id", pluginconf.CriticalVMs{IDs: []int{103}},
			map[int]bool{103: true}, map[int]model.Health{101: "ok", 102: "warn", 103: "crit", 9000: "ok"}},
		{"by tag, compared without regard to case", pluginconf.CriticalVMs{Tags: []string{"critical"}},
			map[int]bool{102: true}, map[int]model.Health{101: "ok", 102: "crit", 103: "warn", 9000: "ok"}},
		{"a running critical VM is fine, and a critical template too", pluginconf.CriticalVMs{IDs: []int{101, 9000}},
			map[int]bool{101: true, 9000: true}, map[int]model.Health{101: "ok", 102: "warn", 103: "warn", 9000: "ok"}},
		{"an id and a tag together", pluginconf.CriticalVMs{IDs: []int{103}, Tags: []string{"Control-Plane"}},
			map[int]bool{101: true, 103: true}, map[int]model.Health{101: "ok", 102: "warn", 103: "crit", 9000: "ok"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.CriticalVMs = tc.cfg }})
			d, _ := e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
			for _, vm := range d.VMs {
				if vm.Critical != tc.critical[vm.VMID] {
					t.Errorf("vm %d critical = %v, want %v", vm.VMID, vm.Critical, tc.critical[vm.VMID])
				}
				if vm.Health != tc.health[vm.VMID] {
					t.Errorf("vm %d health = %s, want %s", vm.VMID, vm.Health, tc.health[vm.VMID])
				}
			}
		})
	}
}

func TestVMHealthTable(t *testing.T) {
	for _, tc := range []struct {
		critical bool
		status   string
		template bool
		offline  bool
		want     model.Health
	}{
		{false, "running", false, false, "ok"}, {true, "running", false, false, "ok"},
		{false, "stopped", false, false, "warn"}, {true, "stopped", false, false, "crit"},
		{false, "stopped", true, false, "ok"}, {true, "stopped", true, false, "ok"},
		{true, "paused", false, false, "crit"},
		// A guest of an offline node is unknown, whatever it last was; a template stays fine.
		{true, "unknown", false, true, "unknown"}, {false, "running", false, true, "unknown"}, {false, "unknown", true, true, "ok"},
	} {
		if got := vmHealth(tc.critical, tc.status, tc.template, tc.offline); got != tc.want {
			t.Errorf("vmHealth(%v, %q, %v, %v) = %s, want %s", tc.critical, tc.status, tc.template, tc.offline, got, tc.want)
		}
	}
	if !isCritical(upstream.PVEVM{Tags: []string{"Prod"}}, pluginconf.CriticalVMs{Tags: []string{" prod "}}) {
		t.Error("a tag with spaces around the setting did not match")
	}
	if isCritical(upstream.PVEVM{VMID: 5}, pluginconf.CriticalVMs{}) {
		t.Error("nothing configured, yet critical")
	}
}

// attentionAt is the plugin's attention as the overview sees it: read through the supervisor,
// which stamps the items that have no time of their own.
func (e *env) attentionAt(t *testing.T, now time.Time) []model.AttentionItem {
	t.Helper()
	for _, c := range e.sup.Contributors() {
		if c.ID == ID && c.Attention != nil {
			return c.Attention.Attention(now)
		}
	}
	t.Fatal("proxmox is not among the overview contributors")
	return nil
}

// The attention items: a stopped, non-template VM, critical or warning, with the id, source,
// route and ref of the design; Since is when the supervisor first saw it and does not move.
func TestAttentionItemsAndSinceIsStable(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.CriticalVMs.IDs = []int{103} }})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })

	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	items := e.attentionAt(t, t0)
	got := map[string]model.AttentionItem{}
	for _, it := range items {
		got[it.ID] = it
	}
	if len(items) != 2 || got["vm:102"].ID == "" || got["vm:103"].ID == "" {
		t.Fatalf("items = %+v, want vm:102 (warn) and vm:103 (crit) and neither the running VM nor the template", items)
	}
	it := got["vm:103"]
	if it.Severity != model.SeverityCrit || got["vm:102"].Severity != model.SeverityWarn {
		t.Errorf("severities: %s, %s", it.Severity, got["vm:102"].Severity)
	}
	if it.Source != "proxmox" || it.Plugin != "proxmox" || it.Route != "/vms/103" || it.Ref != "proxmox:VM/103" ||
		it.Title != "VM scratch is stopped" || it.Detail != "VMID 103" {
		t.Errorf("item = %+v", it)
	}
	if len(it.Links) != 2 || it.Links[0].Label != "Console" || it.Links[1].Label != "Proxmox UI" {
		t.Errorf("links = %+v", it.Links)
	}
	if !it.Since.Equal(t0) {
		t.Errorf("Since = %v, want the first sighting %v", it.Since, t0)
	}
	again := e.attentionAt(t, t0.Add(time.Hour))
	for _, x := range again {
		if !x.Since.Equal(t0) {
			t.Errorf("%s: Since moved to %v", x.ID, x.Since)
		}
	}
}

// A node the cluster reports offline is a critical item, and so is a cluster that has lost quorum;
// both point at the VMs page and the object, and go away when the cluster recovers.
func TestClusterAttention(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/resources?type=vm", `{"data":[]}`)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the empty list", anyVMs)
	pve.set("/cluster/status", `{"data":[{"type":"cluster","name":"lab","quorate":0},
	  {"type":"node","name":"pve-1","nodeid":1,"online":1},{"type":"node","name":"pve-2","nodeid":2,"online":0}]}`)
	pve.set("/cluster/resources?type=node", `{"data":[{"node":"pve-1","status":"online","maxcpu":8,"maxmem":16000},{"node":"pve-2","status":"offline"}]}`)
	var items []model.AttentionItem
	waitFor(t, "the cluster items", func() bool { items = e.attentionAt(t, time.Now()); return len(items) == 2 })
	q, n := items[0], items[1]
	if q.ID != "pve-cluster:quorum" || q.Severity != model.SeverityCrit || q.Ref != "proxmox:Cluster/lab" || q.Route != "/vms" ||
		q.Title != "Proxmox cluster lab has lost quorum" || !strings.Contains(q.Detail, "until quorum returns") || q.Source != "proxmox" {
		t.Errorf("quorum item = %+v", q)
	}
	if n.ID != "pve-node:pve-2" || n.Severity != model.SeverityCrit || n.Ref != "proxmox:Node/pve-2" || n.Route != "/vms" ||
		n.Title != "Proxmox node pve-2 is offline" || n.Plugin != "proxmox" {
		t.Errorf("node item = %+v", n)
	}
	var hosts []model.TopologyHost
	waitFor(t, "the offline host", func() bool {
		hosts = e.current().Topology().Hosts
		return len(hosts) == 2 && hosts[1].Health == model.HealthCrit
	})

	// An answer without the cluster entry (the cluster file system restarting) keeps the last
	// known name and quorum.
	pve.set("/cluster/status", `{"data":[{"type":"node","name":"pve-1","nodeid":1,"online":1}]}`)
	n0 := pve.count("/cluster/status")
	waitFor(t, "two transient answers", func() bool { return pve.count("/cluster/status") >= n0+2 })
	if items := e.attentionAt(t, time.Now()); len(items) != 2 || items[0].ID != "pve-cluster:quorum" {
		t.Errorf("after a transient answer: %+v, want the quorum item kept", items)
	}

	pve.set("/cluster/status", pveCluster)
	pve.set("/cluster/resources?type=node", pveUsage)
	waitFor(t, "the items to clear", func() bool { return len(e.attentionAt(t, time.Now())) == 0 })

	// A standalone server has no quorum to lose, and an unknown quorum is no lost one.
	if got := clusterAttention(pvemodel.PVEClusterData{Standalone: true, QuorumKnown: true, Nodes: []pvemodel.NodeData{{Node: "pve-1", Status: pvemodel.NodeOnline}}}, nil); len(got) != 0 {
		t.Errorf("standalone items = %+v", got)
	}
	if got := clusterAttention(pvemodel.PVEClusterData{Name: "lab"}, nil); len(got) != 0 {
		t.Errorf("unknown quorum items = %+v", got)
	}
}

// A failed /cluster/status call says nothing about the quorum: a lost quorum stays raised with its
// first-seen time, so the push notifier does not send it again when the call recovers.
func TestAFailedClusterStatusKeepsALostQuorum(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/status", `{"data":[{"type":"cluster","name":"lab","quorate":0},
	  {"type":"node","name":"pve-1","nodeid":1,"online":1},{"type":"node","name":"pve-2","nodeid":2,"online":1}]}`)
	e := newEnv(t, envOpts{pve: pve})
	quorum := func() (model.AttentionItem, bool) {
		for _, it := range e.attentionAt(t, time.Now()) {
			if it.ID == "pve-cluster:quorum" {
				return it, true
			}
		}
		return model.AttentionItem{}, false
	}
	waitFor(t, "the quorum item", func() bool { _, ok := quorum(); return ok })
	first, _ := quorum()

	pve.fail("/cluster/status", http.StatusInternalServerError)
	n := pve.count("/cluster/status")
	waitFor(t, "two failed answers", func() bool { return pve.count("/cluster/status") >= n+2 })
	it, ok := quorum()
	if !ok || !it.Since.Equal(first.Since) {
		t.Errorf("after a failed call: %+v, %v; want the item kept since %v", it, ok, first.Since)
	}
	var c pvemodel.PVEClusterData
	e.payload(t, "proxmox.cluster", &c)
	if !c.QuorumKnown || c.Quorate || c.Name != "lab" {
		t.Errorf("cluster = %+v, want the last known name and lost quorum", c)
	}
}

// A node in state unknown has no usage, counts in no totals, is not asked for its facts, and is no
// offline item; any state the cluster does not name reads the same.
func TestAnUnknownNodeIsNeitherOnlineNorOffline(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/resources?type=node", `{"data":[
	  {"node":"pve-1","status":"online","cpu":0.5,"maxcpu":8,"mem":4000,"maxmem":16000},
	  {"node":"pve-2","status":"unknown","cpu":0.25,"maxcpu":24,"mem":6000,"maxmem":48000}]}`)
	e := newEnv(t, envOpts{pve: pve})
	var c pvemodel.PVEClusterData
	waitFor(t, "the cluster", func() bool { e.payload(t, "proxmox.cluster", &c); return len(c.Nodes) == 2 })
	if u := c.Nodes[1]; u.Status != pvemodel.NodeUnknown || u.Threads != 0 || u.MemTotalBytes != 0 {
		t.Errorf("unknown node = %+v, want no usage", u)
	}
	if tt := c.Totals; tt.Nodes != 2 || tt.NodesOnline != 1 || tt.Threads != 8 {
		t.Errorf("totals = %+v, want pve-1's alone", tt)
	}
	for _, it := range e.attentionAt(t, time.Now()) {
		if strings.HasPrefix(it.ID, "pve-node:") {
			t.Errorf("an unknown node raised %+v", it)
		}
	}
	if n := pve.count("/nodes/pve-2/status"); n != 0 {
		t.Errorf("an unknown node's status was asked %d times", n)
	}
	pve.set("/cluster/resources?type=node", `{"data":[{"node":"pve-1","status":"online","maxcpu":8},{"node":"pve-2","status":""}]}`)
	waitFor(t, "an empty state", func() bool {
		e.payload(t, "proxmox.cluster", &c)
		return len(c.Nodes) == 2 && c.Nodes[0].Threads == 8 && c.Nodes[0].MemTotalBytes == 0
	})
	if c.Nodes[1].Status != pvemodel.NodeUnknown {
		t.Errorf("empty state = %q, want unknown", c.Nodes[1].Status)
	}
}

// The guests' health follows a node going down at the cluster poll, not at the next guest poll.
func TestANodeGoingDownRepublishesTheGuests(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve, timing: func(tm *timing) { tm.vms = time.Hour }})
	e.vms(t, "pve-2's stopped guest a warning", func(d model.VMsData) bool {
		return len(d.VMs) == 4 && byID(d, 103).Health == model.HealthWarn
	})
	pve.set("/cluster/resources?type=node", `{"data":[{"node":"pve-1","status":"online","maxcpu":8},{"node":"pve-2","status":"offline"}]}`)
	e.vms(t, "pve-2's guest unknown", func(d model.VMsData) bool { return byID(d, 103).Health == model.HealthUnknown })
}

func TestAttentionStartsOverWhenTheVMRecovers(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })

	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	e.attentionAt(t, t0)

	// 103 starts.
	pve.set("/cluster/resources?type=vm", `{"data":[{"vmid":103,"name":"scratch","status":"running","maxcpu":1,"maxmem":1,"tags":"lab"}]}`)
	e.vms(t, "103 running", func(d model.VMsData) bool { return len(d.VMs) == 1 && d.VMs[0].Status == "running" })
	if items := e.attentionAt(t, t0.Add(time.Minute)); len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
	// It stops again: a new item with its own Since.
	pve.set("/cluster/resources?type=vm", pveVMs)
	e.vms(t, "103 stopped", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	t1 := t0.Add(2 * time.Minute)
	for _, it := range e.attentionAt(t, t1) {
		if it.ID == "vm:103" && !it.Since.Equal(t1) {
			t.Errorf("Since = %v, want %v", it.Since, t1)
		}
	}
}

// A host with every VM running has nothing to raise, which the contributor says with a non-nil
// empty list: nil is "no opinion yet" and would keep the sightings of VMs that recovered.
func TestAttentionOfRunningVMsIsNonNilEmpty(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/resources?type=vm", `{"data":[{"vmid":101,"name":"cp","status":"running","maxcpu":1,"maxmem":1,"tags":"lab"}]}`)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the running guest", func(d model.VMsData) bool { return len(d.VMs) == 1 && d.VMs[0].Status == "running" })
	for _, c := range e.sup.Contributors() {
		if c.ID == ID {
			plugintest.AssertAttentionNonNil(t, c)
			if items := c.Attention.Attention(time.Now()); len(items) != 0 {
				t.Errorf("items = %+v, want none", items)
			}
			return
		}
	}
	t.Fatal("proxmox is not among the overview contributors")
}

func TestNoAttentionKPIsOrTopologyBeforeTheFirstPoll(t *testing.T) {
	p := New().(*Plugin)
	if p.Attention(time.Now()) != nil || p.KPIs() != nil {
		t.Error("a plugin that has not started contributes attention or KPIs")
	}
	if tp := p.Topology(); len(tp.VMs) != 0 || len(tp.Hosts) != 0 {
		t.Errorf("topology = %+v", tp)
	}
}

// KPIs: vms.running and vms.total (templates not counted), the tile's health on the first.
func TestKPIsAndTopology(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	p := e.current()
	ks := p.KPIs()
	want := []model.KPI{
		{Key: "vms.running", Plugin: "proxmox", Label: "VMs running", Value: 1, Health: model.HealthWarn},
		{Key: "vms.total", Plugin: "proxmox", Label: "VMs", Value: 3, Health: model.HealthOK},
	}
	if !slices.Equal(ks, want) {
		t.Errorf("KPIs = %+v, want %+v", ks, want)
	}
	// All running: the tile is fine.
	all := kpisOf([]model.VM{{Status: "running"}, {Status: "stopped", Template: true}})
	if all[0].Health != model.HealthOK || all[0].Value != 1 || all[1].Value != 1 {
		t.Errorf("KPIs with everything running = %+v", all)
	}

	waitFor(t, "the cluster", func() bool { return len(p.Topology().Hosts) == 2 })
	tp := p.Topology()
	wantHosts := []model.TopologyHost{
		{Name: "pve-1", Node: "pve-1", Health: model.HealthOK, MemUsedRatio: 0.25},
		{Name: "pve-2", Node: "pve-2", Health: model.HealthOK, MemUsedRatio: 0.125},
	}
	if !slices.Equal(tp.Hosts, wantHosts) || len(tp.Groups) != 0 {
		t.Errorf("topology hosts %+v, groups %+v; want a host per node and no groups", tp.Hosts, tp.Groups)
	}
	if len(tp.VMs) != 3 {
		t.Fatalf("topology VMs = %+v, want the three that are not templates", tp.VMs)
	}
	cp := tp.VMs[0]
	if cp.VMID != 101 || cp.Node != "pve-1" || tp.VMs[1].Node != "pve-2" || !cp.IsK8sNode || !cp.Running || cp.CPURatio != 0.25 || cp.MemRatio != 0.5 || cp.Health != model.HealthOK {
		t.Errorf("topology vm = %+v", cp)
	}
	if tp.VMs[1].IsK8sNode || tp.VMs[1].Running || tp.VMs[1].Health != model.HealthWarn {
		t.Errorf("topology vm = %+v", tp.VMs[1])
	}

	// The supervisor lists the plugin as a contributor of all three, with its sources.
	var found bool
	for _, c := range e.sup.Contributors() {
		if c.ID == ID {
			found = c.Attention != nil && c.KPIs != nil && c.Topology != nil && c.Sources != nil
		}
	}
	if !found {
		t.Error("proxmox is not a contributor of attention, KPIs and topology")
	}
}

// Without the kubernetes plugin the VMs publish at once, with no node and no chip for it.
func TestVMsPublishWithoutTheKubernetesPlugin(t *testing.T) {
	e := newEnv(t, envOpts{noKube: true})
	d, srcs := e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	for _, vm := range d.VMs {
		if vm.K8sNode != nil {
			t.Errorf("vm %d has a node with no kubernetes plugin: %+v", vm.VMID, vm.K8sNode)
		}
	}
	if len(srcs) != 1 || srcs[0].Name != "proxmox" {
		t.Errorf("sources = %+v", srcs)
	}
}

// The first publish of the VMs waits for the node index to sync, and Synced can turn true with no
// node event: a Revision poll alone would leave the gate shut for good.
func TestFirstVMsPublishFollowsSyncedWithoutARevisionChange(t *testing.T) {
	e := newEnv(t, envOpts{unsynced: true})
	e.state(t, plugin.StateHealthy) // the poll has ended: the row does not wait for the index
	time.Sleep(150 * time.Millisecond)
	var v map[string]any
	if _, ok := e.payload(t, "proxmox.vms", &v); ok {
		t.Fatal("proxmox.vms was published before the node index synced")
	}
	// The other topics do not wait.
	waitFor(t, "proxmox.cluster", func() bool { _, ok := e.payload(t, "proxmox.cluster", &v); return ok })

	rev := e.idx.Revision()
	e.idx.setSynced(true) // no node event: the revision stays
	if e.idx.Revision() != rev {
		t.Fatal("the fake changed its revision")
	}
	d, _ := e.vms(t, "the guests after Synced turned true", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	if n := byID(d, 101).K8sNode; n == nil || n.Name != "k8s-cp-1" {
		t.Errorf("k8sNode = %+v, want the node once synced", n)
	}
}

// A node turning NotReady reaches the VM within the watcher's interval, without a new poll.
func TestNodeIndexRevisionRepublishesTheVMs(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve, timing: func(tm *timing) { tm.vms = time.Hour }})
	e.vms(t, "the first publish", func(d model.VMsData) bool {
		n := byID(d, 101).K8sNode
		return n != nil && n.Ready
	})
	polls := pve.count("/cluster/resources?type=vm")
	e.idx.setNode("k8s-cp-1", false)
	e.vms(t, "the node not ready", func(d model.VMsData) bool {
		n := byID(d, 101).K8sNode
		return n != nil && !n.Ready
	})
	if got := pve.count("/cluster/resources?type=vm"); got != polls {
		t.Errorf("the republish polled Proxmox again (%d -> %d requests)", polls, got)
	}
}

// The Headlamp node link of a VM that is a cluster node comes from whichever link provider runs.
func TestAVMThatIsANodeGetsTheNodeLinks(t *testing.T) {
	e := newEnv(t, envOpts{links: func(req plugin.LinkRequest) []model.Link {
		if req.Kind != "Node" {
			return nil
		}
		return []model.Link{{Label: "Headlamp", Href: "https://headlamp.example.com/nodes/" + req.Name, Kind: model.LinkKindHeadlamp}}
	}})
	d, _ := e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	labels := func(vm model.VM) []string {
		var out []string
		for _, l := range vm.Links {
			out = append(out, l.Label)
		}
		return out
	}
	if got := labels(byID(d, 101)); !slices.Equal(got, []string{"Console", "Proxmox UI", "Headlamp"}) {
		t.Errorf("k8s-cp-1 links = %v", got)
	}
	if got := labels(byID(d, 102)); !slices.Equal(got, []string{"Console", "Proxmox UI"}) {
		t.Errorf("db-1 links = %v", got)
	}
}

// A failed task list keeps the tasks of the previous poll, while the VMs are the new ones.
func TestAFailedTaskListKeepsThePreviousTasks(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the first publish", func(d model.VMsData) bool { return len(d.Tasks) == 1 })
	pve.fail("/cluster/tasks", 500)
	pve.set("/cluster/resources?type=vm", `{"data":[{"vmid":7,"name":"seven","status":"running","maxcpu":1,"maxmem":1}]}`)
	d, srcs := e.vms(t, "the new VMs", func(d model.VMsData) bool { return len(d.VMs) == 1 })
	if len(d.Tasks) != 1 || srcs[0].OK {
		t.Errorf("tasks = %+v, sources = %+v: want the old task and a failing source", d.Tasks, srcs)
	}
}

// Never nil: a cluster with no guests and no tasks is two empty arrays.
func TestEmptyListsAreNeverNull(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/resources?type=vm", `{"data":[]}`)
	pve.set("/cluster/tasks", `{"data":[]}`)
	e := newEnv(t, envOpts{pve: pve})
	waitFor(t, "the payload", func() bool { _, ok := e.hub.Latest("", "proxmox.vms"); return ok })
	ev, _ := e.hub.Latest("", "proxmox.vms")
	if !contains(string(ev.Data), `"vms":[]`, `"tasks":[]`) {
		t.Errorf("payload = %s", ev.Data)
	}
}
