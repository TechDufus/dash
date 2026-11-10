package world

import (
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
)

// The fake Proxmox cluster: two nodes, HostName and SecondNodeName, each a hardware host.
const (
	// ClusterName is the name of the fake Proxmox cluster.
	ClusterName = "demo"
	// SecondNodeName is the cluster's second node, a smaller one-socket box.
	SecondNodeName = "pve-demo-2"
)

// pveNodeSpec is what a fake node is: its facts and the resting values of its usage.
type pveNodeSpec struct {
	name, version, kernel, cpuModel string
	sockets, cores, threads         int
	memGiB, rootGiB, rootUsedGiB    float64
	cpuKey, memKey                  string
	cpu, mem                        float64 // resting ratios
	upDays                          int64
}

// pveNodes are the fake cluster's nodes, by name. Their CPU and memory drift with the same keys
// as their hardware hosts' payloads (hostSpecs), so the two pages agree.
var pveNodes = []pveNodeSpec{
	{
		name: HostName, version: "9.0.11", kernel: "6.14.11-4-pve", cpuModel: "Intel Xeon E5-2690 v4 @ 2.60GHz",
		sockets: 2, cores: 28, threads: 56, memGiB: 256, rootGiB: 94, rootUsedGiB: 18,
		cpuKey: "hostcpu", memKey: "hostmem", cpu: 0.19, mem: 0.41, upDays: 38,
	},
	{
		name: SecondNodeName, version: "9.0.11", kernel: "6.14.11-4-pve", cpuModel: "Intel Xeon E-2236 CPU @ 3.40GHz",
		sockets: 1, cores: 6, threads: 12, memGiB: 64, rootGiB: 94, rootUsedGiB: 11,
		cpuKey: "node2cpu", memKey: "node2mem", cpu: 0.27, mem: 0.48, upDays: 21,
	},
}

// PVECluster is the proxmox.cluster payload: the fake cluster with both nodes online, their usage
// and facts, and the totals.
func (w *World) PVECluster() pvemodel.PVEClusterData { return w.pveCluster(w.clock()) }

func (w *World) pveCluster(now time.Time) pvemodel.PVEClusterData {
	d := pvemodel.PVEClusterData{
		Ref: plugin.Ref("proxmox", "Cluster", ClusterName), Name: ClusterName, QuorumKnown: true, Quorate: true,
		Nodes: make([]pvemodel.NodeData, 0, len(pveNodes)),
	}
	var busy float64
	for _, s := range pveNodes {
		memTotal := gib(s.memGiB)
		n := pvemodel.NodeData{
			Ref: plugin.Ref("proxmox", "Node", s.name), Node: s.name, Status: pvemodel.NodeOnline,
			CPURatio: Around(s.cpuKey, now, s.cpu, 0.07), Threads: s.threads,
			MemUsedBytes: int64(float64(memTotal) * Around(s.memKey, now, s.mem, 0.02)), MemTotalBytes: memTotal,
			RootUsedBytes: gib(s.rootUsedGiB), RootTotalBytes: gib(s.rootGiB),
			UptimeSeconds: int64(now.Sub(w.start).Seconds()) + s.upDays*86400,
			Version:       s.version, Kernel: s.kernel, CPUModel: s.cpuModel, CPUSockets: s.sockets, CPUCores: s.cores,
		}
		d.Nodes = append(d.Nodes, n)
		d.Totals.Nodes++
		d.Totals.NodesOnline++
		d.Totals.Threads += n.Threads
		d.Totals.MemUsedBytes += n.MemUsedBytes
		d.Totals.MemTotalBytes += n.MemTotalBytes
		busy += n.CPURatio * float64(n.Threads)
	}
	d.Totals.CPURatio = busy / float64(d.Totals.Threads)
	return d
}

// PVEStorages are the storages of the fake cluster with their usage, the shared one first and then
// each node's own: the proxmox.storage list.
func (w *World) PVEStorages() []pvemodel.PVEStorage {
	spec := []struct {
		node, name string
		used, size float64
	}{
		{"", "backup-nfs", 2300, 7400},
		{HostName, "local", 18, 94},
		{HostName, "local-zfs", 612, 1700},
		{HostName, "tank", 4100, 10900},
		{SecondNodeName, "local", 11, 94},
		{SecondNodeName, "local-lvm", 140, 860},
	}
	out := make([]pvemodel.PVEStorage, len(spec))
	for i, s := range spec {
		ref := plugin.Ref("proxmox", "Storage", s.node, s.name)
		if s.node == "" {
			ref = plugin.Ref("proxmox", "Storage", s.name)
		}
		out[i] = pvemodel.PVEStorage{
			Ref: ref, Name: s.name, Node: s.node, Shared: s.node == "", UsedBytes: gib(s.used), SizeBytes: gib(s.size),
		}
	}
	return out
}
