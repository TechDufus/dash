package proxmox

import (
	"context"
	"maps"
	"sync"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// clusterInfo is what /cluster/status says about the cluster: its name, whether it is a standalone
// server, and its quorum. known is false until an answer has said it.
type clusterInfo struct {
	name       string
	standalone bool
	quorate    bool
	known      bool
}

// pollCluster reads every node with its usage (the node list and their online state), the facts of
// every online node, and the cluster's name and quorum, and publishes proxmox.cluster.
//
// Only the node list decides the poll: a failed one publishes the last good payload (none, on the
// first poll) with the failing source. /cluster/status needs Sys.Audit on /, which a token can
// lack: until it has answered once, the quorum is unknown and raises no item. Its failure keeps
// the last known name and quorum, so a lost quorum stays raised with its first-seen time through a
// timeout, and shows as the "cluster status" source. An answer without the cluster entry from a
// server that is not
// standalone (the cluster file system restarting) keeps the last known name and quorum too. A node
// whose own status fails keeps its last facts and does not fail the poll: a node that went down
// between the calls answers HTTP 595 through the node dash reaches.
func (r *runtime) pollCluster(ctx context.Context) {
	usage, err := r.client.NodeUsage(ctx)
	var (
		st    upstream.PVEClusterStatus
		stErr error
		facts map[string]upstream.PVEHost
	)
	if err == nil {
		st, stErr = r.client.ClusterStatus(ctx)
		facts = r.nodeFacts(ctx, nodesWith(usage, pvemodel.NodeOnline))
	}
	republish := false
	if ctx.Err() != nil {
		return
	}

	r.mu.Lock()
	r.clusterOut = outcome{polled: true, cat: upstream.Category(err), at: r.h.Now()}
	if err == nil {
		r.statusOut = outcome{polled: true, cat: upstream.Category(stErr), at: r.h.Now()}
		if stErr == nil && !st.Transient {
			r.info = clusterInfo{name: st.Name, standalone: st.Standalone, quorate: st.Quorate, known: true}
		}
		info := r.info
		if !info.known {
			// A single node is a standalone server whether or not /cluster/status says so, and
			// it has no quorum to lose; a cluster's quorum stays unknown.
			info.standalone, info.quorate = len(usage) == 1, len(usage) == 1
		}
		kept := make(map[string]upstream.PVEHost, len(usage))
		for _, u := range usage {
			if f, ok := facts[u.Node]; ok {
				kept[u.Node] = f
			} else if f, ok := r.facts[u.Node]; ok {
				kept[u.Node] = f
			}
		}
		r.facts = kept
		before := notOnline(r.cluster.Nodes)
		r.cluster = clusterData(info, usage, kept)
		// Guest health follows the node state: republish the VMs now rather than at their next
		// poll when a node went down or came back.
		republish = !maps.Equal(before, notOnline(r.cluster.Nodes))
	}
	data, out := r.clusterPayloadLocked(), r.clusterOut
	r.mu.Unlock()
	r.note("cluster", err)
	if err == nil {
		r.note("cluster status", stErr)
	}
	r.h.Publish(topicCluster, data, []model.SourceStatus{src(sourceName, out)})
	if republish {
		r.publishVMs()
	}
}

// clusterPayloadLocked is a copy of the last good cluster payload, never with a null node list.
// The caller holds r.mu.
func (r *runtime) clusterPayloadLocked() pvemodel.PVEClusterData {
	d := r.cluster
	d.Nodes = append([]pvemodel.NodeData{}, r.cluster.Nodes...)
	return d
}

// nodeFacts asks every node in nodes for its own status, together: one slow node does not hold up
// the others beyond the request timeout. A node that fails is left out.
func (r *runtime) nodeFacts(ctx context.Context, nodes []string) map[string]upstream.PVEHost {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = make(map[string]upstream.PVEHost, len(nodes))
	)
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := r.client.NodeStatus(ctx, n)
			r.note("node "+n, err)
			if err != nil {
				return
			}
			mu.Lock()
			out[n] = h
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// nodeStatus is the node's state in the cluster; a state the cluster does not name reads as
// unknown, never as online.
func nodeStatus(u upstream.PVENodeUsage) pvemodel.NodeStatus {
	switch s := pvemodel.NodeStatus(u.Status); s {
	case pvemodel.NodeOnline, pvemodel.NodeOffline:
		return s
	}
	return pvemodel.NodeUnknown
}

// nodesWith are the nodes in the given state: only an online node can be asked for its own status.
func nodesWith(usage []upstream.PVENodeUsage, s pvemodel.NodeStatus) []string {
	var out []string
	for _, u := range usage {
		if nodeStatus(u) == s {
			out = append(out, u.Node)
		}
	}
	return out
}

// clusterData assembles the proxmox.cluster payload: every node with its usage and its facts, by
// name as the usage lists them, and the totals over the online nodes. The CPU ratio of the totals
// weighs each node by its threads, so a busy small node does not read as a busy cluster.
func clusterData(info clusterInfo, usage []upstream.PVENodeUsage, facts map[string]upstream.PVEHost) pvemodel.PVEClusterData {
	d := pvemodel.PVEClusterData{
		Name: info.name, Standalone: info.standalone, QuorumKnown: info.known || info.standalone,
		Quorate: info.quorate, Nodes: []pvemodel.NodeData{},
	}
	var busy float64
	for _, u := range usage {
		n := pvemodel.NodeData{Ref: plugin.Ref(ID, "Node", u.Node), Node: u.Node, Status: nodeStatus(u)}
		if n.Status == pvemodel.NodeOnline {
			n.CPURatio, n.Threads = u.CPURatio, u.Threads
			n.MemUsedBytes, n.MemTotalBytes = u.Mem, u.MaxMem
			n.RootUsedBytes, n.RootTotalBytes = u.Disk, u.MaxDisk
			n.UptimeSeconds = u.Uptime
		}
		if f, ok := facts[u.Node]; ok {
			n.Version, n.Kernel, n.CPUModel = f.PVEVersion, f.Kernel, f.CPUModel
			n.CPUSockets, n.CPUCores = f.Sockets, f.Cores
		}
		d.Totals.Nodes++
		if n.Status == pvemodel.NodeOnline {
			d.Totals.NodesOnline++
			d.Totals.Threads += n.Threads
			d.Totals.MemUsedBytes += n.MemUsedBytes
			d.Totals.MemTotalBytes += n.MemTotalBytes
			busy += n.CPURatio * float64(n.Threads)
		}
		d.Nodes = append(d.Nodes, n)
	}
	if d.Totals.Threads > 0 {
		d.Totals.CPURatio = busy / float64(d.Totals.Threads)
	}
	switch {
	case !info.standalone && info.name != "":
		d.Ref = plugin.Ref(ID, "Cluster", info.name)
	case len(d.Nodes) > 0:
		d.Ref = plugin.Ref(ID, "Cluster", d.Nodes[0].Node)
	}
	return d
}

// offlineNodes are the nodes the last good cluster poll found not online.
func (r *runtime) offlineNodes() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return notOnline(r.cluster.Nodes)
}

// notOnline are the nodes whose status is not online: their guests cannot be trusted to run.
func notOnline(nodes []pvemodel.NodeData) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		if n.Status != pvemodel.NodeOnline {
			out[n.Node] = true
		}
	}
	return out
}
