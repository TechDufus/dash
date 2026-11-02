package proxmox

import (
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
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
		want     model.Health
	}{
		{false, "running", false, "ok"}, {true, "running", false, "ok"},
		{false, "stopped", false, "warn"}, {true, "stopped", false, "crit"},
		{false, "stopped", true, "ok"}, {true, "stopped", true, "ok"},
		{true, "paused", false, "crit"},
	} {
		if got := vmHealth(tc.critical, tc.status, tc.template); got != tc.want {
			t.Errorf("vmHealth(%v, %q, %v) = %s, want %s", tc.critical, tc.status, tc.template, got, tc.want)
		}
	}
	if !isCritical(upstream.PVEVM{Tags: []string{"Prod"}}, pluginconf.CriticalVMs{Tags: []string{" prod "}}) {
		t.Error("a tag with spaces around the setting did not match")
	}
	if isCritical(upstream.PVEVM{VMID: 5}, pluginconf.CriticalVMs{}) {
		t.Error("nothing configured, yet critical")
	}
}

// The attention items: a stopped, non-template VM, critical or warning, with the id, source,
// route and ref of the design; Since is when the plugin first saw it and does not move.
func TestAttentionItemsAndSinceIsStable(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.CriticalVMs.IDs = []int{103} }})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	p := e.current()

	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	items := p.Attention(t0)
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
	again := p.Attention(t0.Add(time.Hour))
	for _, x := range again {
		if !x.Since.Equal(t0) {
			t.Errorf("%s: Since moved to %v", x.ID, x.Since)
		}
	}
}

func TestAttentionStartsOverWhenTheVMRecovers(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	p := e.current()
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	p.Attention(t0)

	// 103 starts.
	pve.set("/cluster/resources", `{"data":[{"vmid":103,"name":"scratch","status":"running","maxcpu":1,"maxmem":1,"tags":"lab"}]}`)
	e.vms(t, "103 running", func(d model.VMsData) bool { return len(d.VMs) == 1 && d.VMs[0].Status == "running" })
	if items := p.Attention(t0.Add(time.Minute)); len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
	// It stops again: a new item with its own Since.
	pve.set("/cluster/resources", pveVMs)
	e.vms(t, "103 stopped", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	t1 := t0.Add(2 * time.Minute)
	for _, it := range p.Attention(t1) {
		if it.ID == "vm:103" && !it.Since.Equal(t1) {
			t.Errorf("Since = %v, want %v", it.Since, t1)
		}
	}
}

func TestNoAttentionKPIsOrTopologyBeforeTheFirstPoll(t *testing.T) {
	p := New().(*Plugin)
	if p.Attention(time.Now()) != nil || p.KPIs() != nil {
		t.Error("a plugin that has not started contributes attention or KPIs")
	}
	if tp := p.Topology(); len(tp.VMs) != 0 || tp.Host.Name != "" {
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

	tp := p.Topology()
	if tp.Host.Name != "" || len(tp.Groups) != 0 {
		t.Errorf("topology names a host or groups: %+v", tp)
	}
	if len(tp.VMs) != 3 {
		t.Fatalf("topology VMs = %+v, want the three that are not templates", tp.VMs)
	}
	cp := tp.VMs[0]
	if cp.VMID != 101 || !cp.IsK8sNode || !cp.Running || cp.CPURatio != 0.25 || cp.MemRatio != 0.5 || cp.Health != model.HealthOK {
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
	waitFor(t, "proxmox.node", func() bool { _, ok := e.payload(t, "proxmox.node", &v); return ok })

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
	polls := pve.count("/cluster/resources")
	e.idx.setNode("k8s-cp-1", false)
	e.vms(t, "the node not ready", func(d model.VMsData) bool {
		n := byID(d, 101).K8sNode
		return n != nil && !n.Ready
	})
	if got := pve.count("/cluster/resources"); got != polls {
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
	pve.set("/cluster/resources", `{"data":[{"vmid":7,"name":"seven","status":"running","maxcpu":1,"maxmem":1}]}`)
	d, srcs := e.vms(t, "the new VMs", func(d model.VMsData) bool { return len(d.VMs) == 1 })
	if len(d.Tasks) != 1 || srcs[0].OK {
		t.Errorf("tasks = %+v, sources = %+v: want the old task and a failing source", d.Tasks, srcs)
	}
}

// Never nil: a cluster with no guests and no tasks is two empty arrays.
func TestEmptyListsAreNeverNull(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/resources", `{"data":[]}`)
	pve.set("/cluster/tasks", `{"data":[]}`)
	e := newEnv(t, envOpts{pve: pve})
	waitFor(t, "the payload", func() bool { _, ok := e.hub.Latest("", "proxmox.vms"); return ok })
	ev, _ := e.hub.Latest("", "proxmox.vms")
	if !contains(string(ev.Data), `"vms":[]`, `"tasks":[]`) {
		t.Errorf("payload = %s", ev.Data)
	}
}
