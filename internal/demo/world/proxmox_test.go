package world

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
)

// The Proxmox parts are what the proxmox twin publishes: the cluster with its nodes and its
// storages, with refs of the plugin that publishes them and values that make sense together.
func TestPVEPartsAreWhatTheTwinPublishes(t *testing.T) {
	w := testWorld()

	c := w.PVECluster()
	if c.Name != ClusterName || c.Standalone || !c.QuorumKnown || !c.Quorate || len(c.Nodes) != 2 ||
		c.Nodes[0].Node != HostName || c.Nodes[1].Node != SecondNodeName {
		t.Fatalf("PVECluster = %+v", c)
	}
	if p, kind, id, ok := plugin.ParseRef(c.Ref); !ok || p != "proxmox" || kind != "Cluster" || id != ClusterName {
		t.Errorf("cluster ref %q", c.Ref)
	}
	threads := 0
	var used, total int64
	for _, n := range c.Nodes {
		if n.Status != pvemodel.NodeOnline || n.Version == "" || n.Kernel == "" || n.CPUModel == "" || n.UptimeSeconds <= 0 ||
			n.Threads <= 0 || n.CPUCores <= 0 || n.CPUSockets <= 0 || n.CPURatio <= 0 || n.CPURatio >= 1 ||
			n.MemUsedBytes <= 0 || n.MemTotalBytes <= n.MemUsedBytes || n.RootTotalBytes <= n.RootUsedBytes {
			t.Errorf("node %+v", n)
		}
		if p, kind, id, ok := plugin.ParseRef(n.Ref); !ok || p != "proxmox" || kind != "Node" || id != n.Node {
			t.Errorf("node ref %q", n.Ref)
		}
		threads += n.Threads
		used += n.MemUsedBytes
		total += n.MemTotalBytes
	}
	if tt := c.Totals; tt.Nodes != 2 || tt.NodesOnline != 2 || tt.Threads != threads || tt.MemUsedBytes != used || tt.MemTotalBytes != total ||
		tt.CPURatio <= 0 || tt.CPURatio >= 1 {
		t.Errorf("totals %+v", tt)
	}

	ps := w.PVEStorages()
	seen := map[string]bool{}
	shared, perNode := 0, map[string]int{}
	for i, s := range ps {
		if s.Name == "" || seen[s.Ref] || s.UsedBytes <= 0 || s.SizeBytes <= s.UsedBytes {
			t.Errorf("storage %+v: want a unique ref and 0 < used < size", s)
		}
		seen[s.Ref] = true
		want := s.Node + "/" + s.Name
		if s.Shared {
			shared++
			want = s.Name
			if s.Node != "" || i >= shared {
				t.Errorf("shared storage %+v: want no node and a place before every node's own", s)
			}
		} else {
			perNode[s.Node]++
		}
		if p, kind, id, ok := plugin.ParseRef(s.Ref); !ok || p != "proxmox" || kind != "Storage" || id != want {
			t.Errorf("storage ref %q for %+v", s.Ref, s)
		}
	}
	if shared != 1 || perNode[HostName] == 0 || perNode[SecondNodeName] == 0 {
		t.Errorf("storages: %d shared, per node %v; want one shared and some on each node", shared, perNode)
	}

	// The guests run on both nodes, and each names a node of the cluster.
	on := map[string]int{}
	for _, vm := range w.VMs().VMs {
		on[vm.Node]++
	}
	if len(on) != 2 || on[HostName] == 0 || on[SecondNodeName] == 0 {
		t.Errorf("guests per node %v", on)
	}
}

// The values depend on the clock only: two builds at the same instant are equal, and the uptime
// moves with it.
func TestPVEClusterFollowsTheClock(t *testing.T) {
	now := testNow
	w := New(func() time.Time { return now }, testStart, fakeLinks{})
	a, b := w.PVECluster(), w.PVECluster()
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(w.PVEStorages(), w.PVEStorages()) {
		t.Fatal("two builds at the same instant differ")
	}
	now = now.Add(time.Hour)
	if got := w.PVECluster().Nodes[0].UptimeSeconds - a.Nodes[0].UptimeSeconds; got != 3600 {
		t.Errorf("uptime moved by %d s in an hour", got)
	}
}

// The fake cluster names no real one.
func TestPVEPartsNameNothingReal(t *testing.T) {
	w := testWorld()
	c := w.PVECluster()
	text := c.Name
	for _, n := range c.Nodes {
		text += n.Node + n.CPUModel + n.Kernel
	}
	for _, s := range w.PVEStorages() {
		text += s.Name
	}
	for _, lab := range []string{"pve" + "-730", "E5" + "-2683", "fast" + "-zfs", "homelab", "talos"} {
		if strings.Contains(text, lab) {
			t.Errorf("the Proxmox fixtures mention %q", lab)
		}
	}
}
