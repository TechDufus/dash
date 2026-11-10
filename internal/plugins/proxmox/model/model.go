// Package model holds the payload types only the proxmox plugin publishes. It is an import leaf:
// it may import nothing from dash (TestPluginModelPackagesAreImportLeaves), because
// internal/model.TopicTypes lists every topic's payload type and so imports this package.
// A payload that needs a core type (Link, Health, SourceStatus) stays in internal/model.
package model

// PVEClusterData is the proxmox.cluster topic: the Proxmox VE cluster the plugin reads through
// whichever node its URL reaches, every node with its usage and facts, and the totals over the
// online nodes. A standalone server is a cluster of one. It is not called ClusterData because that
// name is taken in internal/model, and the OpenAPI generator qualifies a name two packages share.
type PVEClusterData struct {
	// Ref is proxmox:Cluster/<name>; a standalone server's is proxmox:Cluster/<node>.
	Ref string `json:"ref"`
	// Name is the cluster's name; empty for a standalone server.
	Name string `json:"name"`
	// Standalone is true for a server in no cluster: its only node is the server itself.
	Standalone bool `json:"standalone"`
	// QuorumKnown is false while dash cannot read the cluster's quorum: the token lacks Sys.Audit
	// on /, or the cluster has not answered yet. Quorate means nothing then. Always true for a
	// standalone server.
	QuorumKnown bool `json:"quorumKnown"`
	// Quorate is false while the cluster has lost quorum: its guests keep running, but nothing can
	// be started, stopped or changed until quorum returns. Always true for a standalone server.
	Quorate bool `json:"quorate"`
	// Nodes are every node of the cluster, sorted by name; never null.
	Nodes []NodeData `json:"nodes"`
	// Totals add up the online nodes.
	Totals PVEClusterTotals `json:"totals"`
}

// NodeStatus is a node's state in the cluster.
type NodeStatus string

const (
	// NodeOnline is a node the cluster sees and whose status daemon reports.
	NodeOnline NodeStatus = "online"
	// NodeOffline is a node the cluster has lost.
	NodeOffline NodeStatus = "offline"
	// NodeUnknown is a member whose status daemon has not reported for a while: the cluster
	// knows nothing of its usage, so it is neither counted as online nor as down.
	NodeUnknown NodeStatus = "unknown"
)

// NodeData is one node of the cluster: its usage, as the cluster reports it, and its facts, as the
// node itself reports them.
type NodeData struct {
	Ref  string `json:"ref"` // proxmox:Node/<node>
	Node string `json:"node"`
	// Status is the node's state in the cluster. A node that is not online reports no usage and
	// keeps the facts it last reported.
	Status NodeStatus `json:"status"`
	// CPURatio is the node's CPU use, 0 to 1 of all its threads; Threads counts its logical CPUs.
	CPURatio      float64 `json:"cpuRatio"`
	Threads       int     `json:"threads"`
	MemUsedBytes  int64   `json:"memUsedBytes"`
	MemTotalBytes int64   `json:"memTotalBytes"`
	// RootUsedBytes and RootTotalBytes are the node's root file system.
	RootUsedBytes  int64 `json:"rootUsedBytes"`
	RootTotalBytes int64 `json:"rootTotalBytes"`
	UptimeSeconds  int64 `json:"uptimeSeconds"`
	// The facts below come from the node itself and stay empty until it has answered once.
	Version  string `json:"version"` // pve-manager version, "9.2.2"
	Kernel   string `json:"kernel"`
	CPUModel string `json:"cpuModel"`
	// CPUSockets and CPUCores are the node's CPU sockets and its physical cores over all of them.
	CPUSockets int `json:"cpuSockets"`
	CPUCores   int `json:"cpuCores"`
}

// PVEClusterTotals add up the online nodes of the cluster.
type PVEClusterTotals struct {
	Nodes int `json:"nodes"`
	// NodesOnline counts the nodes whose status is online.
	NodesOnline int `json:"nodesOnline"`
	Threads     int `json:"threads"`
	// CPURatio is the CPU use over all those threads, 0 to 1: each node weighs by its threads.
	CPURatio      float64 `json:"cpuRatio"`
	MemUsedBytes  int64   `json:"memUsedBytes"`
	MemTotalBytes int64   `json:"memTotalBytes"`
}

// PVEStorage is one Proxmox storage with its usage.
type PVEStorage struct {
	// Ref is proxmox:Storage/<node>/<name>; a shared storage, listed once for the cluster, is
	// proxmox:Storage/<name>.
	Ref  string `json:"ref"`
	Name string `json:"name"`
	// Node is the node the storage belongs to; empty for a shared storage.
	Node string `json:"node"`
	// Shared marks a storage every node reaches (NFS, Ceph...): it is listed and counted once.
	Shared    bool  `json:"shared"`
	UsedBytes int64 `json:"usedBytes"`
	SizeBytes int64 `json:"sizeBytes"`
}

// PVEStorageData is the proxmox.storage topic. It is not called StorageData because that name
// is taken in internal/model, and the OpenAPI generator qualifies a name two packages share.
type PVEStorageData struct {
	// Storages are the shared storages first, then every node's own, by node and name; never null.
	Storages []PVEStorage `json:"storages"`
	// UsedBytes and SizeBytes add up the storages, each shared one once.
	UsedBytes int64 `json:"usedBytes"`
	SizeBytes int64 `json:"sizeBytes"`
}
