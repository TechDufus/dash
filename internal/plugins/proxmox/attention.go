package proxmox

import (
	"fmt"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
)

// The keys of the plugin's KPIs.
const (
	kpiRunning = "vms.running"
	kpiTotal   = "vms.total"
)

// current is the VMs of the last poll as the overview sees them, or nil before the first poll.
func (r *runtime) current() *model.VMsData {
	r.mu.RLock()
	polled := r.vmsOut.polled
	r.mu.RUnlock()
	if !polled {
		return nil
	}
	d := r.buildVMs(r.nodeIndex())
	return &d
}

// knownCluster is the last good cluster payload, and whether a cluster poll has succeeded yet.
func (r *runtime) knownCluster() (pvemodel.PVEClusterData, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clusterPayloadLocked(), r.cluster.Ref != ""
}

// Attention raises an item for every guest that is not running and is not a template (critical
// for a critical VM, a warning for any other), for every node the cluster reports offline, and for
// a cluster that has lost quorum. Since is when the supervisor first saw the condition. It has no
// opinion (nil) before the first poll of the VMs or of the cluster.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	d := r.current()
	c, known := r.knownCluster()
	if d == nil && !known {
		return nil
	}
	items := []model.AttentionItem{}
	if d != nil {
		items = attentionOf(d.VMs)
	}
	if known {
		items = append(items, clusterAttention(c, proxmoxUI(r.cfg.URL))...)
	}
	return items
}

// clusterAttention is a critical item for every offline node and one for a cluster without quorum.
// A standalone server has no quorum to lose, and an unknown quorum is no lost one. A node in state
// unknown is no item either: the cluster has not lost it, only its status daemon is quiet. The
// ids carry "pve-" so they never meet the
// kubernetes plugin's "node:<name>" items, which the web and the push notifier key alike.
func clusterAttention(c pvemodel.PVEClusterData, links []model.Link) []model.AttentionItem {
	items := []model.AttentionItem{}
	if !c.Standalone && c.QuorumKnown && !c.Quorate {
		items = append(items, model.AttentionItem{
			ID: "pve-cluster:quorum", Severity: model.SeverityCrit, Source: sourceName, Plugin: string(ID),
			Title:  fmt.Sprintf("Proxmox cluster %s has lost quorum", c.Name),
			Detail: "VMs keep running; nothing can be started, stopped or changed until quorum returns",
			Route:  "/vms", Links: links, Ref: c.Ref,
		})
	}
	for _, n := range c.Nodes {
		if n.Status != pvemodel.NodeOffline {
			continue
		}
		items = append(items, model.AttentionItem{
			ID: "pve-node:" + n.Node, Severity: model.SeverityCrit, Source: sourceName, Plugin: string(ID),
			Title: fmt.Sprintf("Proxmox node %s is offline", n.Node), Detail: "The cluster reports it offline.",
			Route: "/vms", Links: links, Ref: n.Ref,
		})
	}
	return items
}

// attentionOf is the items of a list of VMs, without Since.
func attentionOf(vms []model.VM) []model.AttentionItem {
	items := []model.AttentionItem{}
	for _, vm := range vms {
		if vm.Template || (vm.Health != model.HealthWarn && vm.Health != model.HealthCrit) {
			continue
		}
		items = append(items, model.AttentionItem{
			ID: "vm:" + strconv.Itoa(vm.VMID), Severity: model.Severity(vm.Health), Source: sourceName, Plugin: string(ID),
			Title: fmt.Sprintf("VM %s is %s", vm.Name, vm.Status), Detail: fmt.Sprintf("VMID %d", vm.VMID),
			Route: fmt.Sprintf("/vms/%d", vm.VMID), Links: vm.Links, Ref: vm.Ref,
		})
	}
	return items
}

// KPIs are the guests that are running and the guests there are, templates not counted. The
// health is the tile's: a warning while a guest is down.
func (p *Plugin) KPIs() []model.KPI {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	d := r.current()
	if d == nil {
		return nil
	}
	return kpisOf(d.VMs)
}

func kpisOf(vms []model.VM) []model.KPI {
	running, total := 0, 0
	for _, vm := range vms {
		if vm.Template {
			continue
		}
		total++
		if vm.Status == "running" {
			running++
		}
	}
	health := model.HealthOK
	if running < total {
		health = model.HealthWarn
	}
	return []model.KPI{
		{Key: kpiRunning, Plugin: string(ID), Label: "VMs running", Value: float64(running), Health: health},
		{Key: kpiTotal, Plugin: string(ID), Label: "VMs", Value: float64(total), Health: model.HealthOK},
	}
}

// Topology is one host per node of the cluster (critical while offline) and the guests, templates
// left out, each with the node it runs on and whether it is a Kubernetes node.
func (p *Plugin) Topology() model.Topology {
	t := model.Topology{Hosts: []model.TopologyHost{}, VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	r := p.rt.Load()
	if r == nil {
		return t
	}
	if c, known := r.knownCluster(); known {
		t.Hosts = topologyHosts(c)
	}
	if d := r.current(); d != nil {
		t.VMs = topologyOf(d.VMs)
	}
	return t
}

// topologyHosts is a host per node, by name as the cluster lists them.
func topologyHosts(c pvemodel.PVEClusterData) []model.TopologyHost {
	out := make([]model.TopologyHost, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		h := model.TopologyHost{Name: n.Node, Node: n.Node, Health: model.HealthOK}
		switch n.Status {
		case pvemodel.NodeOffline:
			h.Health = model.HealthCrit
		case pvemodel.NodeUnknown:
			h.Health = model.HealthUnknown
		}
		if n.MemTotalBytes > 0 {
			h.MemUsedRatio = float64(n.MemUsedBytes) / float64(n.MemTotalBytes)
		}
		out = append(out, h)
	}
	return out
}

func topologyOf(vms []model.VM) []model.TopologyVM {
	out := []model.TopologyVM{}
	for _, vm := range vms {
		if vm.Template {
			continue
		}
		mem := 0.0
		if vm.MaxMemBytes > 0 {
			mem = float64(vm.MemBytes) / float64(vm.MaxMemBytes)
		}
		out = append(out, model.TopologyVM{
			VMID: vm.VMID, Name: vm.Name, Node: vm.Node, Health: vm.Health, CPURatio: vm.CPURatio, MemRatio: mem,
			IsK8sNode: vm.K8sNode != nil, Running: vm.Status == "running",
		})
	}
	return out
}
