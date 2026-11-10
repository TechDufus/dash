package proxmox

import (
	"context"
	"net/http"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// current is the plugin instance that runs now (a reload makes a new one).
func (e *env) current() *Plugin {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.p
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "proxmox" || m.Name != "Proxmox VE" || m.Version != "1.0.0" || m.Description == "" {
		t.Errorf("manifest = %+v", m)
	}
	if !slices.Equal(m.Uses, []plugin.ID{"kubernetes", "prometheus"}) || len(m.Requires) != 0 {
		t.Errorf("uses %v requires %v: want a soft use of kubernetes and prometheus and no requirement", m.Uses, m.Requires)
	}
	var names []string
	for _, tp := range m.Topics {
		names = append(names, tp.Name)
	}
	if !slices.Equal(names, []string{"vms", "cluster", "storage"}) {
		t.Errorf("topics = %v, want the wire order vms, cluster, storage", names)
	}
	var series []string
	for _, s := range m.Series {
		series = append(series, s.Name)
	}
	if !slices.Equal(series, []string{"vm.cpu", "vm.mem", "vm.netIn", "vm.netOut", "vm.diskRead", "vm.diskWrite"}) {
		t.Errorf("series = %v", series)
	}
	if len(m.Actions) != 0 {
		t.Errorf("actions = %v: the plugin has none", m.Actions)
	}
	// The nav entry is the shim's, so the rail does not move.
	want := model.NavItem{Path: "/vms", Label: "VMs", Title: "Virtual machines", Icon: "Server"}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != want {
		t.Errorf("nav = %+v, want %+v", m.UI.Nav, want)
	}
	if _, ok := m.Config().(*pluginconf.Proxmox); !ok {
		t.Errorf("Config() is %T", m.Config())
	}
}

// A URL alone is not enough: the console and shell links used to appear with a URL and no token,
// and now the plugin is off until it can read Proxmox.
func TestProxmoxConfiguredNeedsURLAndToken(t *testing.T) {
	p := New().(*Plugin)
	full := defaultConfig("https://pve.example.com:8006")
	for _, tc := range []struct {
		name string
		mod  func(*pluginconf.Proxmox)
		want bool
	}{
		{"everything set", func(*pluginconf.Proxmox) {}, true},
		{"no url", func(c *pluginconf.Proxmox) { c.URL = "" }, false},
		{"no token id", func(c *pluginconf.Proxmox) { c.TokenID = "" }, false},
		{"no token secret", func(c *pluginconf.Proxmox) { c.TokenSecret = secret.Value{} }, false},
		{"a url alone", func(c *pluginconf.Proxmox) { c.TokenID, c.TokenSecret = "", secret.Value{} }, false},
	} {
		c := full
		tc.mod(&c)
		if got := p.Configured(&c); got != tc.want {
			t.Errorf("%s: Configured = %v, want %v", tc.name, got, tc.want)
		}
	}
	if p.Configured(&pluginconf.Proxmox{}) || p.Configured("not a section") {
		t.Error("an empty or foreign configuration is configured")
	}
}

func TestEveryRealPluginSaysWhatToSet(t *testing.T) {
	hint := New().(plugin.ConfigHinter).ConfigHint()
	if !strings.HasPrefix(hint, "plugins.proxmox.") {
		t.Errorf("hint = %q, want it to start with plugins.proxmox.", hint)
	}
}

func TestValidateDelegatesToTheSection(t *testing.T) {
	p := New().(*Plugin)
	c := defaultConfig("https://pve.example.com:8006")
	if err := p.Validate(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	c.Series.VMSelector = `id="x"` // no {vmid}
	if err := p.Validate(&c); err == nil {
		t.Error("a selector with no {vmid} passed")
	}
	if err := p.Validate(42); err == nil {
		t.Error("a foreign configuration passed")
	}
}

// Every visible state: stopped and started again, the topics reach an open stream after the row.
func TestVisibilityRuleA(t *testing.T) {
	pve := newFakePVE(t, false)
	off := plugin.InstanceConfig{Config: &pluginconf.Proxmox{}} // nothing set: disabled
	c := defaultConfig(pve.URL)
	on := plugin.InstanceConfig{Config: &c}
	plugintest.AssertTopicsReachOpenStream(t, func() plugin.Plugin { return &Plugin{t: fastTiming()} }, off, on)
}

// The status follows design 5.3: starting until the first VM poll has ended, healthy when every
// source answers, degraded when some do not, error when none does.
func TestStatusStates(t *testing.T) {
	t.Run("healthy and the sources", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		e.state(t, plugin.StateHealthy)
		srcs := e.current().Status().Sources
		if len(srcs) != 1 || srcs[0].Name != "proxmox" || !srcs[0].OK {
			t.Errorf("sources = %+v, want one ok proxmox source", srcs)
		}
	})
	t.Run("degraded when a source fails, with the category", func(t *testing.T) {
		pve := newFakePVE(t, false)
		pve.fail("/cluster/resources?type=node", http.StatusForbidden)
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateDegraded)
		if !strings.Contains(pi.Message, "cluster: auth") || strings.Contains(pi.Message, "vms") {
			t.Errorf("message = %q", pi.Message)
		}
		if srcs := e.current().Status().Sources; len(srcs) != 1 || srcs[0].OK || srcs[0].Error != "auth" {
			t.Errorf("sources = %+v, want proxmox failing with auth", srcs)
		}
	})
	t.Run("error when every source fails", func(t *testing.T) {
		pve := newFakePVE(t, false)
		for _, p := range []string{"/cluster/resources?type=vm", "/cluster/resources?type=node", "/cluster/resources?type=storage"} {
			pve.fail(p, http.StatusBadGateway)
		}
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateError)
		if !strings.Contains(pi.Message, "vms: http_502") {
			t.Errorf("message = %q", pi.Message)
		}
	})
	t.Run("a token without Sys.Audit on / reads the cluster all the same", func(t *testing.T) {
		pve := newFakePVE(t, false)
		pve.fail("/cluster/status", http.StatusForbidden)
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateDegraded)
		if pi.Message != "cluster status: auth" {
			t.Errorf("message = %q", pi.Message)
		}
		srcs := e.current().Status().Sources
		if len(srcs) != 2 || srcs[0].Name != "proxmox" || !srcs[0].OK || srcs[1].Name != "cluster status" || srcs[1].Error != "auth" {
			t.Errorf("sources = %+v, want proxmox ok and the cluster status failing with auth", srcs)
		}
		var c pvemodel.PVEClusterData
		waitFor(t, "the cluster", func() bool { e.payload(t, "proxmox.cluster", &c); return len(c.Nodes) == 2 })
		if c.Name != "" || c.QuorumKnown || c.Quorate || c.Standalone {
			t.Errorf("cluster without its status = %+v, want no name and an unknown quorum", c)
		}
		for _, it := range e.current().Attention(time.Now()) {
			if strings.HasPrefix(it.ID, "pve-cluster:") {
				t.Errorf("an unknown quorum raised %+v", it)
			}
		}
	})
	t.Run("plugins.proxmox.node naming no node of the cluster", func(t *testing.T) {
		e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.Node = "pve-9" }})
		pi := e.state(t, plugin.StateDegraded)
		if pi.Message != "plugins.proxmox.node names pve-9, which is not a node of cluster lab (pve-1, pve-2)" {
			t.Errorf("message = %q", pi.Message)
		}
		if n, ok := e.current().Service().(svc.NodeName).NodeName(); ok {
			t.Errorf("NodeName = %q, want none for a node the cluster does not have", n)
		}
	})
	t.Run("never unavailable while it runs", func(t *testing.T) {
		pve := newFakePVE(t, false)
		pve.fail("/cluster/resources?type=vm", http.StatusInternalServerError)
		e := newEnv(t, envOpts{pve: pve})
		e.state(t, plugin.StateDegraded)
		pve.heal("/cluster/resources?type=vm")
		e.state(t, plugin.StateHealthy)
	})
	t.Run("starting before the first poll", func(t *testing.T) {
		p := New().(*Plugin)
		if st := p.Status(); st.State != plugin.StateStarting {
			t.Errorf("before Start: %s", st.State)
		}
	})
}

// A poll that fails keeps the last good payload and marks its source; the next good poll clears it.
func TestAFailedPollKeepsTheLastGoodPayload(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the first payload", func(d model.VMsData) bool { return len(d.VMs) == 4 })

	pve.fail("/cluster/resources?type=vm", http.StatusBadGateway)
	d, srcs := e.vms(t, "a failing source", func(model.VMsData) bool {
		var cur model.VMsData
		s, _ := e.payload(t, "proxmox.vms", &cur)
		return len(s) == 1 && !s[0].OK
	})
	_ = d
	var kept model.VMsData
	srcs, _ = e.payload(t, "proxmox.vms", &kept)
	if len(kept.VMs) != 4 || srcs[0].Name != "proxmox" || srcs[0].Error != "http_502" {
		t.Errorf("after a failed poll: %d VMs, sources %+v; want the last good four and http_502", len(kept.VMs), srcs)
	}
	pve.heal("/cluster/resources?type=vm")
	waitFor(t, "the source to recover", func() bool {
		s, _ := e.payload(t, "proxmox.vms", &kept)
		return len(s) == 1 && s[0].OK
	})
}

// The envelope of each topic names its own source only: proxmox for the guests, the cluster and the
// storages, and the kubernetes source that proxmox.vms carried is gone.
func TestEnvelopeSourcesNameOnlyTheirOwn(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, plugin.StateHealthy)
	for _, topic := range []string{"proxmox.vms", "proxmox.cluster", "proxmox.storage"} {
		var any_ map[string]any
		waitFor(t, topic, func() bool { _, ok := e.payload(t, topic, &any_); return ok })
		srcs, _ := e.payload(t, topic, &any_)
		if len(srcs) != 1 || srcs[0].Name != "proxmox" || !srcs[0].OK {
			t.Errorf("%s sources = %+v, want one ok proxmox source", topic, srcs)
		}
	}
}

// proxmox.cluster carries the cluster, every node with the usage the cluster reports and the facts
// the node reports, and the totals: CPU weighed by threads.
func TestClusterTopicCarriesEveryNode(t *testing.T) {
	e := newEnv(t, envOpts{})
	var c pvemodel.PVEClusterData
	waitFor(t, "the cluster", func() bool {
		e.payload(t, "proxmox.cluster", &c)
		return len(c.Nodes) == 2 && c.Nodes[1].Version != ""
	})
	want := pvemodel.PVEClusterData{
		Ref: "proxmox:Cluster/lab", Name: "lab", QuorumKnown: true, Quorate: true,
		Nodes: []pvemodel.NodeData{
			{
				Ref: "proxmox:Node/pve-1", Node: "pve-1", Status: pvemodel.NodeOnline, CPURatio: 0.5, Threads: 8,
				MemUsedBytes: 4000, MemTotalBytes: 16000, RootUsedBytes: 10, RootTotalBytes: 100, UptimeSeconds: 2422190,
				Version: "9.0.5", Kernel: "6.8.12-4-pve", CPUModel: "Example CPU @ 2.10GHz", CPUSockets: 1, CPUCores: 4,
			},
			{
				Ref: "proxmox:Node/pve-2", Node: "pve-2", Status: pvemodel.NodeOnline, CPURatio: 0.25, Threads: 24,
				MemUsedBytes: 6000, MemTotalBytes: 48000, RootUsedBytes: 20, RootTotalBytes: 200, UptimeSeconds: 7200,
				Version: "9.0.6", Kernel: "6.8.12-5-pve", CPUModel: "Example CPU @ 3.00GHz", CPUSockets: 2, CPUCores: 12,
			},
		},
		// (0.5*8 + 0.25*24) / 32
		Totals: pvemodel.PVEClusterTotals{Nodes: 2, NodesOnline: 2, Threads: 32, CPURatio: 0.3125, MemUsedBytes: 10000, MemTotalBytes: 64000},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("cluster = %+v\nwant      %+v", c, want)
	}
	plugintest.AssertRefs(t, e.sup, ID, c, pvemodel.PVEClusterData{})
	plugintest.AssertRefs(t, e.sup, ID, c, pvemodel.NodeData{})
}

// An offline node stays in the list with no usage and the facts it last reported, its own status is
// no longer asked for, and the totals count the online nodes only. A node whose status fails while
// the cluster sees it online keeps its last facts, and the poll does not fail.
func TestClusterWithAnOfflineNode(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	var c pvemodel.PVEClusterData
	waitFor(t, "pve-2's facts", func() bool {
		e.payload(t, "proxmox.cluster", &c)
		return len(c.Nodes) == 2 && c.Nodes[1].Version == "9.0.6"
	})

	before := pve.count("/nodes/pve-2/status")
	pve.fail("/nodes/pve-2/status", 595)
	waitFor(t, "polls after pve-2's status failed", func() bool { return pve.count("/nodes/pve-2/status") > before+1 })
	e.state(t, plugin.StateHealthy)

	// pve-2 has been offline for a while: the cluster forgot its guests' details and its storages'
	// sizes.
	pve.set("/cluster/resources?type=vm", `{"data":[
	  {"id":"qemu/101","type":"qemu","node":"pve-1","vmid":101,"name":"k8s-cp-1","status":"running","maxcpu":4,"maxmem":8589934592},
	  {"id":"qemu/102","type":"qemu","node":"pve-2","vmid":102,"status":"unknown"},
	  {"id":"qemu/103","type":"qemu","node":"pve-2","vmid":103,"status":"unknown"},
	  {"id":"qemu/9000","type":"qemu","node":"pve-1","vmid":9000,"name":"k8s-template","status":"stopped","template":1}]}`)
	pve.set("/cluster/resources?type=storage", `{"data":[
	  {"id":"storage/pve-1/local","storage":"local","node":"pve-1","shared":0,"status":"available","disk":1000,"maxdisk":4000},
	  {"id":"storage/pve-2/local","storage":"local","node":"pve-2","shared":0,"status":"unknown"}]}`)
	pve.set("/cluster/resources?type=node", `{"data":[
	  {"id":"node/pve-1","type":"node","node":"pve-1","status":"online","cpu":0.5,"maxcpu":8,"mem":4000,"maxmem":16000,"disk":10,"maxdisk":100,"uptime":2422190},
	  {"id":"node/pve-2","type":"node","node":"pve-2","status":"offline"}]}`)
	waitFor(t, "pve-2 offline", func() bool {
		e.payload(t, "proxmox.cluster", &c)
		return len(c.Nodes) == 2 && c.Nodes[1].Status == pvemodel.NodeOffline
	})
	off := c.Nodes[1]
	if off.Version != "9.0.6" || off.CPUModel != "Example CPU @ 3.00GHz" || off.CPURatio != 0 || off.Threads != 0 || off.MemUsedBytes != 0 {
		t.Errorf("offline node = %+v, want its last facts and no usage", off)
	}
	if tt := c.Totals; tt != (pvemodel.PVEClusterTotals{Nodes: 2, NodesOnline: 1, Threads: 8, CPURatio: 0.5, MemUsedBytes: 4000, MemTotalBytes: 16000}) {
		t.Errorf("totals = %+v, want pve-1's alone", tt)
	}
	d, _ := e.vms(t, "pve-2's guests unknown", func(d model.VMsData) bool { return byID(d, 102).Health == model.HealthUnknown })
	if vm := byID(d, 102); vm.Name != "db-1" || vm.MaxMemBytes != 4294967296 || vm.Node != "pve-2" {
		t.Errorf("a guest of the offline node = %+v, want its last name and memory", vm)
	}
	for _, it := range e.current().Attention(time.Now()) {
		if strings.HasPrefix(it.ID, "vm:") {
			t.Errorf("a guest of the offline node raised %+v: the node's item covers it", it)
		}
	}
	if w := e.get(t, "/vms/102"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "node pve-2 is offline") {
		t.Errorf("detail of a guest of the offline node: %d %s", w.Code, w.Body)
	}
	if paths := pve.pathsWith("/qemu/102"); len(paths) != 0 {
		t.Errorf("Proxmox was asked about a guest of the offline node: %v", paths)
	}
	var st pvemodel.PVEStorageData
	waitFor(t, "pve-2's storages gone", func() bool { e.payload(t, "proxmox.storage", &st); return len(st.Storages) == 1 })
	if st.Storages[0].Ref != "proxmox:Storage/pve-1/local" {
		t.Errorf("storages = %+v, want pve-1's alone: an offline node's sizes are unknown", st.Storages)
	}

	asked := pve.count("/nodes/pve-2/status")
	n := pve.count("/cluster/status")
	waitFor(t, "two more cluster polls", func() bool { return pve.count("/cluster/status") >= n+2 })
	if got := pve.count("/nodes/pve-2/status"); got > asked+1 {
		t.Errorf("an offline node's status was asked %d more times", got-asked)
	}
}

// A standalone server is a cluster of one: no name, quorate, its ref the node's, and svc.NodeName
// names its node without plugins.proxmox.node.
func TestStandaloneServerIsAClusterOfOne(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.set("/cluster/status", `{"data":[{"type":"node","id":"node/pve-1","name":"pve-1","nodeid":0,"online":1,"local":1}]}`)
	pve.set("/cluster/resources?type=node", `{"data":[{"id":"node/pve-1","node":"pve-1","status":"online","cpu":0.5,"maxcpu":8,"mem":4000,"maxmem":16000}]}`)
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.Node = "" }})
	var c pvemodel.PVEClusterData
	waitFor(t, "the server", func() bool { e.payload(t, "proxmox.cluster", &c); return len(c.Nodes) == 1 })
	if c.Ref != "proxmox:Cluster/pve-1" || c.Name != "" || !c.Standalone || !c.QuorumKnown || !c.Quorate {
		t.Errorf("standalone = %+v", c)
	}
	s := e.current().Service()
	if n, ok := s.(svc.NodeName).NodeName(); !ok || n != "pve-1" {
		t.Errorf("NodeName = %q, %v; want the only node", n, ok)
	}
	if nodes, ok := s.(svc.ClusterNodes).Nodes(); !ok || !slices.Equal(nodes, []string{"pve-1"}) {
		t.Errorf("Nodes = %v, %v", nodes, ok)
	}
}

func TestVMsTopic(t *testing.T) {
	e := newEnv(t, envOpts{})
	d, _ := e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	ids := []int{}
	for _, vm := range d.VMs {
		ids = append(ids, vm.VMID)
	}
	if !slices.Equal(ids, []int{101, 102, 103, 9000}) {
		t.Errorf("ids = %v, want them in order", ids)
	}
	vm := d.VMs[0]
	if vm.Ref != "proxmox:VM/101" || vm.Node != "pve-1" || vm.Name != "k8s-cp-1" || vm.Status != "running" || !slices.Equal(vm.Tags, []string{"k8s", "control-plane"}) {
		t.Errorf("vm = %+v", vm)
	}
	if vm.K8sNode == nil || vm.K8sNode.Name != "k8s-cp-1" || !vm.K8sNode.Ready {
		t.Errorf("k8sNode = %+v, want the index's node", vm.K8sNode)
	}
	if d.VMs[1].K8sNode != nil || d.VMs[1].Node != "pve-2" {
		t.Errorf("db-1 is no Kubernetes node and runs on pve-2: %+v", d.VMs[1])
	}
	if len(d.Tasks) != 1 || d.Tasks[0].Type != "qmstart" || d.Tasks[0].Node != "pve-1" {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	plugintest.AssertRefs(t, e.sup, ID, d, model.VM{})
}

// The API source lists every node's storages that report a size; the shared one, which every node
// lists, comes first, once and without a node, and the totals count it once.
func TestStorageTopicFromTheAPI(t *testing.T) {
	e := newEnv(t, envOpts{})
	var s pvemodel.PVEStorageData
	waitFor(t, "storages", func() bool { e.payload(t, "proxmox.storage", &s); return len(s.Storages) > 0 })
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/nas", Name: "nas", Shared: true, UsedBytes: 300, SizeBytes: 900},
		{Ref: "proxmox:Storage/pve-1/local", Name: "local", Node: "pve-1", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/pve-1/tank", Name: "tank", Node: "pve-1", UsedBytes: 50, SizeBytes: 100},
		{Ref: "proxmox:Storage/pve-2/local", Name: "local", Node: "pve-2", UsedBytes: 2000, SizeBytes: 4000},
	}
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v\nwant      %+v", s.Storages, want)
	}
	if s.UsedBytes != 3350 || s.SizeBytes != 9000 {
		t.Errorf("totals used %d size %d, want 3350 and 9000 (the shared storage once)", s.UsedBytes, s.SizeBytes)
	}
	plugintest.AssertRefs(t, e.sup, ID, s, pvemodel.PVEStorage{})
}

// A shared storage takes its usage from an entry whose node is online and reaches it, then from
// the largest, then from the first node by name, whatever the order of the answer.
func TestSharedStorageUsageIsChosenDeterministically(t *testing.T) {
	entries := func() []storageEntry {
		return []storageEntry{
			{name: "nas", node: "pve-1", shared: true, usedBytes: 1, sizeBytes: 900},
			{name: "nas", node: "pve-3", shared: true, trusted: true, usedBytes: 3, sizeBytes: 800},
			{name: "nas", node: "pve-2", shared: true, trusted: true, usedBytes: 2, sizeBytes: 800},
			{name: "ceph", node: "pve-2", shared: true, trusted: true, usedBytes: 5, sizeBytes: 500},
			{name: "ceph", node: "pve-1", shared: true, trusted: true, usedBytes: 6, sizeBytes: 600},
		}
	}
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/ceph", Name: "ceph", Shared: true, UsedBytes: 6, SizeBytes: 600},
		{Ref: "proxmox:Storage/nas", Name: "nas", Shared: true, UsedBytes: 2, SizeBytes: 800},
	}
	in := entries()
	if got := storageList(in); !slices.Equal(got, want) {
		t.Errorf("storages = %+v\nwant      %+v", got, want)
	}
	slices.Reverse(in)
	if got := storageList(in); !slices.Equal(got, want) {
		t.Errorf("reversed: %+v", got)
	}
}

// The Prometheus source assembles every node's storages from the exporter's series: the name and
// node from pve_storage_info (else from the id), usage by id, a storage that pve_storage_shared
// marks listed once and first, and the rest by node and name.
func TestStorageSourceAPIAndPrometheus(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg: func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		// Loaded before the plugin starts: a poll that ran in the middle of loading it published
		// the storages without their names, and the wait below saw them.
		prefill: func(e *env) {
			e.prom.set(exprStorageUsed,
				sample(1000, "id", "storage/pve-1/local"), sample(50, "id", "storage/pve-1/tank"), sample(7, "id", "storage/pve-1/nameless"),
				sample(2000, "id", "storage/pve-2/local"), sample(300, "id", "storage/pve-1/nas"), sample(300, "id", "storage/pve-2/nas"))
			e.prom.set(exprStorageSize,
				sample(4000, "id", "storage/pve-1/local"), sample(100, "id", "storage/pve-1/tank"), sample(70, "id", "storage/pve-1/nameless"),
				sample(8000, "id", "storage/pve-2/local"), sample(900, "id", "storage/pve-1/nas"), sample(900, "id", "storage/pve-2/nas"))
			e.prom.set(exprStorageInfo,
				sample(1, "id", "storage/pve-1/local", "storage", "local", "node", "pve-1"), sample(1, "id", "storage/pve-1/tank", "storage", "tank", "node", "pve-1"),
				sample(1, "id", "storage/pve-2/local", "storage", "local", "node", "pve-2"),
				sample(1, "id", "storage/pve-1/nas", "storage", "nas", "node", "pve-1"), sample(1, "id", "storage/pve-2/nas", "storage", "nas", "node", "pve-2"))
			e.prom.set(exprStorageShared,
				sample(0, "id", "storage/pve-1/local"), sample(1, "id", "storage/pve-1/nas"), sample(1, "id", "storage/pve-2/nas"))
		},
	})
	var s pvemodel.PVEStorageData
	var srcs []model.SourceStatus
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/nas", Name: "nas", Shared: true, UsedBytes: 300, SizeBytes: 900},
		{Ref: "proxmox:Storage/pve-1/local", Name: "local", Node: "pve-1", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/pve-1/nameless", Name: "nameless", Node: "pve-1", UsedBytes: 7, SizeBytes: 70},
		{Ref: "proxmox:Storage/pve-1/tank", Name: "tank", Node: "pve-1", UsedBytes: 50, SizeBytes: 100},
		{Ref: "proxmox:Storage/pve-2/local", Name: "local", Node: "pve-2", UsedBytes: 2000, SizeBytes: 8000},
	}
	waitFor(t, "storages from prometheus", func() bool {
		srcs, _ = e.payload(t, "proxmox.storage", &s)
		return slices.Equal(s.Storages, want)
	})
	if s.UsedBytes != 3357 || s.SizeBytes != 13070 {
		t.Errorf("totals used %d size %d, want 3357 and 13070", s.UsedBytes, s.SizeBytes)
	}
	if len(srcs) != 1 || srcs[0].Name != "prometheus" || !srcs[0].OK {
		t.Errorf("sources = %+v, want one ok prometheus source", srcs)
	}
	if e.pve.count("/cluster/resources?type=storage") != 0 {
		t.Error("the prometheus source asked the Proxmox API for the storages")
	}
	// A failing query leaves the other values and its own old ones; the source fails.
	e.prom.setErr(exprStorageSize, upstreamErr("down"))
	waitFor(t, "the prometheus source to fail", func() bool {
		srcs, _ = e.payload(t, "proxmox.storage", &s)
		return len(srcs) == 1 && !srcs[0].OK
	})
	if !slices.Equal(s.Storages, want) {
		t.Errorf("after a failed query: %+v, want the last good list", s.Storages)
	}
	if srcs[0].Error != "unreachable" {
		t.Errorf("error = %q, want a category", srcs[0].Error)
	}
}

// An exporter without pve_storage_shared (an empty answer) or without pve_storage_info lists every
// storage as its node's own, the node and the name taken from the id; an id of another shape is its
// own name, and no ref repeats.
func TestStoragePrometheusWithoutSharedOrInfo(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg: func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		prefill: func(e *env) {
			e.prom.set(exprStorageUsed, sample(1000, "id", "storage/pve-1/local"), sample(2000, "id", "storage/pve-2/local"), sample(5, "id", "odd"))
			e.prom.set(exprStorageSize, sample(4000, "id", "storage/pve-1/local"), sample(8000, "id", "storage/pve-2/local"), sample(9, "id", "odd"))
		},
	})
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/odd", Name: "odd", UsedBytes: 5, SizeBytes: 9},
		{Ref: "proxmox:Storage/pve-1/local", Name: "local", Node: "pve-1", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/pve-2/local", Name: "local", Node: "pve-2", UsedBytes: 2000, SizeBytes: 8000},
	}
	var s pvemodel.PVEStorageData
	waitFor(t, "every node's storages", func() bool { e.payload(t, "proxmox.storage", &s); return len(s.Storages) > 0 })
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v\nwant      %+v", s.Storages, want)
	}
}

func TestStoragePrometheusWithoutPrometheusIsNotConfigured(t *testing.T) {
	e := newEnv(t, envOpts{noProm: true, cfg: func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus }})
	waitFor(t, "the source to be reported", func() bool {
		var s pvemodel.PVEStorageData
		srcs, ok := e.payload(t, "proxmox.storage", &s)
		return ok && len(srcs) == 1 && srcs[0].Error == "not_configured" && srcs[0].Name == "prometheus" && s.Storages != nil
	})
	if pi := e.state(t, plugin.StateDegraded); !strings.Contains(pi.Message, "storage: not_configured") {
		t.Errorf("message = %q", pi.Message)
	}
}

// A Prometheus whose service appears after the first storage poll (a credential filled late) is
// polled at once, not at the next interval (an hour here), so the source stops reading
// not_configured.
func TestStoragePollsAtOnceWhenPrometheusAppears(t *testing.T) {
	e := newEnv(t, envOpts{
		lateProm: true,
		cfg:      func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		timing:   func(tm *timing) { tm.storage = time.Hour },
		prefill: func(e *env) {
			e.prom.set(exprStorageUsed, sample(1000, "id", "storage/pve-1/local"))
			e.prom.set(exprStorageSize, sample(4000, "id", "storage/pve-1/local"))
			e.prom.set(exprStorageInfo, sample(1, "id", "storage/pve-1/local", "storage", "local"))
		},
	})
	waitFor(t, "the source to say not_configured", func() bool {
		var s pvemodel.PVEStorageData
		srcs, ok := e.payload(t, "proxmox.storage", &s)
		return ok && len(srcs) == 1 && srcs[0].Error == "not_configured"
	})

	e.promReleased.Store(true)
	waitFor(t, "the storages from the new querier", func() bool {
		var s pvemodel.PVEStorageData
		srcs, _ := e.payload(t, "proxmox.storage", &s)
		return len(srcs) == 1 && srcs[0].OK && len(s.Storages) == 1
	})
}

// The configured node is known from Start, before any poll has answered.
func TestNodeNameIsOKFromStartWhenConfigured(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/cluster/status", http.StatusInternalServerError)
	e := newEnv(t, envOpts{pve: pve})
	n, ok := e.current().Service().(svc.NodeName).NodeName()
	if !ok || n != "pve-1" {
		t.Fatalf("NodeName = %q, %v right after Start", n, ok)
	}
	// The service answers from the supervisor, too.
	v, found := e.sup.Service(ID)
	if !found {
		t.Fatal("the supervisor has no service for the running plugin")
	}
	if n, ok := v.(svc.NodeName).NodeName(); !ok || n != "pve-1" {
		t.Errorf("Supervisor.Service: %q %v", n, ok)
	}
	if nodes, ok := v.(svc.ClusterNodes).Nodes(); ok {
		t.Errorf("Nodes = %v before a cluster poll succeeded", nodes)
	}
}

// A cluster of several nodes without plugins.proxmox.node runs (it reads every node), but names no
// host node; ClusterNodes says which nodes there are.
func TestSeveralNodesWithoutTheSettingNameNoHostNode(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.Node = "" }})
	e.state(t, plugin.StateHealthy)
	s := e.current().Service()
	waitFor(t, "the cluster's nodes", func() bool { _, ok := s.(svc.ClusterNodes).Nodes(); return ok })
	if nodes, _ := s.(svc.ClusterNodes).Nodes(); !slices.Equal(nodes, []string{"pve-1", "pve-2"}) {
		t.Errorf("Nodes = %v", nodes)
	}
	if n, ok := s.(svc.NodeName).NodeName(); ok {
		t.Errorf("NodeName = %q with two nodes and no setting", n)
	}
}

// Before Start the plugin has no service.
func TestNoServiceBeforeStart(t *testing.T) {
	if s := New().(*Plugin).Service(); s != nil {
		t.Errorf("Service before Start = %v", s)
	}
}

// A rotated token reaches the client at once, without a restart.
func TestReloadedSecretReachesTheProxmoxClient(t *testing.T) {
	pve := newFakePVE(t, false)
	tok := secret.New("first-secret-value", "")
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.TokenSecret = tok }})
	e.state(t, plugin.StateHealthy)
	for _, a := range pve.authHeaders() {
		if a != "PVEAPIToken="+testTokenID+"=first-secret-value" {
			t.Fatalf("Authorization = %q", a)
		}
	}
	tok.Update("rotated-secret-value")
	waitFor(t, "the rotated secret on the client", func() bool {
		hs := pve.authHeaders()
		return len(hs) > 0 && hs[len(hs)-1] == "PVEAPIToken="+testTokenID+"=rotated-secret-value"
	})
}

// runtimeGoroutines counts the goroutines whose stack has a frame of the plugin's runtime: the
// pollers, the node index watcher, and anything they started.
func runtimeGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "internal/plugins/proxmox.(*runtime)") {
			count++
		}
	}
	return count
}

// Stop ends every goroutine: they are gone shortly after it, no request reaches the fake
// afterwards, and Stop returns in time.
func TestStopEndsEveryGoroutine(t *testing.T) {
	before := runtimeGoroutines()
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.state(t, plugin.StateHealthy)
	p := e.current()
	if running := runtimeGoroutines(); running < before+4 {
		t.Fatalf("%d runtime goroutines running, from %d: the count does not see the pollers and the watcher", running, before)
	}

	start := time.Now()
	e.sup.Shutdown(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v", d)
	}
	if err := p.Stop(context.Background()); err != nil { // a second Stop is a no-op
		t.Errorf("second Stop: %v", err)
	}
	// The goroutines have returned (a goroutine leaves a moment after its WaitGroup is done).
	deadline := time.Now().Add(2 * time.Second)
	for runtimeGoroutines() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtimeGoroutines(); after > before {
		t.Errorf("%d runtime goroutines still run after Stop, from %d before Start", after, before)
	}
	requests := pve.total()
	time.Sleep(150 * time.Millisecond) // many poll intervals at the test timing
	if after := pve.total(); after != requests {
		t.Errorf("%d requests reached the fake after Stop", after-requests)
	}
}
