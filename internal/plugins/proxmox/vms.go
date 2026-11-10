package proxmox

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// pollVMs reads the guests and the recent tasks and publishes them. After a failure the last good
// lists go out again with the failing source, as the Manager did; a failed task list keeps the
// tasks of the previous poll while the VMs are the new ones.
func (r *runtime) pollVMs(ctx context.Context) {
	vms, err := r.client.VMs(ctx)
	var tasks []model.PVETask
	if err == nil {
		tasks, err = r.client.Tasks(ctx)
	}
	if ctx.Err() != nil {
		return // stopping: whatever came back is not an outcome
	}

	r.mu.Lock()
	r.vmsOut = outcome{polled: true, cat: upstream.Category(err), at: r.h.Now()}
	if vms != nil {
		r.vms = r.rememberLocked(vms)
	}
	if err == nil {
		r.tasks = tasks
	}
	r.mu.Unlock()
	r.note("vms", err)
	r.publishVMs()
}

// vmMemo is what a guest showed while the cluster still knew it.
type vmMemo struct {
	name        string
	template    bool
	cpus        float64
	maxMemBytes int64
	tags        []string
}

// rememberLocked fills in what the cluster forgot about the guests of a node that has been offline
// for a while (their name, template flag, CPUs, memory size and tags) from what they last showed,
// and remembers the rest. Guests that left the list are forgotten. The caller holds r.mu.
func (r *runtime) rememberLocked(vms []upstream.PVEVM) []upstream.PVEVM {
	memo := make(map[int]vmMemo, len(vms))
	for i, v := range vms {
		m, seen := r.memo[v.VMID]
		if v.Name == "" && seen {
			v.Name, v.Template = m.name, m.template
			if v.CPUs == 0 {
				v.CPUs = m.cpus
			}
			if v.MaxMemBytes == 0 {
				v.MaxMemBytes = m.maxMemBytes
			}
			if len(v.Tags) == 0 {
				v.Tags = m.tags
			}
			vms[i] = v
			memo[v.VMID] = m
			continue
		}
		if v.Name != "" {
			memo[v.VMID] = vmMemo{name: v.Name, template: v.Template, cpus: v.CPUs, maxMemBytes: v.MaxMemBytes, tags: v.Tags}
		}
	}
	r.memo = memo
	return vms
}

// publishVMs builds the payload from the last poll and the node index as it is now, and publishes
// it. The first publish waits for the node index to sync when the kubernetes plugin runs: until
// then no VM can be matched to its node, and a page that showed every VM as no node would flash.
func (r *runtime) publishVMs() {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.mu.RLock()
	out := r.vmsOut
	r.mu.RUnlock()
	if !out.polled {
		return
	}
	idx := r.nodeIndex()
	if idx != nil && !idx.Synced() {
		return
	}
	r.h.Publish(topicVMs, r.buildVMs(idx), []model.SourceStatus{src(sourceName, out)})
}

// buildVMs is the proxmox.vms payload for the last poll: the guests by id, each with the node it
// runs on and that node's console, never nil.
func (r *runtime) buildVMs(idx svc.NodeIndex) model.VMsData {
	if r.build != nil {
		return r.build()
	}
	r.mu.RLock()
	list, tasks := slices.Clone(r.vms), slices.Clone(r.tasks)
	r.mu.RUnlock()
	offline := r.offlineNodes()
	links := r.h.Links()

	out := model.VMsData{VMs: make([]model.VM, 0, len(list)), Tasks: tasks}
	if out.Tasks == nil {
		out.Tasks = []model.PVETask{}
	}
	for _, v := range list {
		critical := isCritical(v, r.cfg.CriticalVMs)
		vm := model.VM{
			Ref: plugin.Ref(ID, "VM", strconv.Itoa(v.VMID)), VMID: v.VMID, Node: v.Node, Name: v.Name, Status: v.Status,
			Health: vmHealth(critical, v.Status, v.Template, offline[v.Node]), Critical: critical,
			Tags: v.Tags, CPUs: v.CPUs, CPURatio: v.CPURatio, MemBytes: v.MemBytes, MaxMemBytes: v.MaxMemBytes,
			UptimeSeconds: v.UptimeSeconds, NetInBytes: v.NetInBytes, NetOutBytes: v.NetOutBytes,
			Template: v.Template, Lock: v.Lock,
			Links: slices.Concat(vmLinks(r.cfg.URL, v.Node, v.VMID, v.Name), proxmoxUI(r.cfg.URL)),
		}
		if vm.Tags == nil {
			vm.Tags = []string{}
		}
		if idx != nil {
			if name, ready, ok := idx.K8sNode(v.Name); ok {
				vm.K8sNode = &model.K8sNodeRef{Name: name, Ready: ready}
				vm.Links = append(vm.Links, links.Resolve(plugin.LinkRequest{Kind: "Node", Name: v.Name})...)
			}
		}
		out.VMs = append(out.VMs, vm)
	}
	sort.Slice(out.VMs, func(i, j int) bool { return out.VMs[i].VMID < out.VMs[j].VMID })
	return out
}

// vmNode is the node the last poll found the guest vmid on; empty for a guest it did not find.
func (r *runtime) vmNode(vmid int) string {
	if r.build != nil {
		for _, vm := range r.build().VMs {
			if vm.VMID == vmid {
				return vm.Node
			}
		}
		return ""
	}
	v, _ := r.guest(vmid)
	return v.Node
}

// guest is the guest vmid as the last poll found it.
func (r *runtime) guest(vmid int) (upstream.PVEVM, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.vms {
		if v.VMID == vmid {
			return v, true
		}
	}
	return upstream.PVEVM{}, false
}

// isCritical reports whether a guest is listed by id or carries a listed tag (compared without
// regard to case).
func isCritical(v upstream.PVEVM, c pluginconf.CriticalVMs) bool {
	if slices.Contains(c.IDs, v.VMID) {
		return true
	}
	for _, t := range v.Tags {
		for _, want := range c.Tags {
			if strings.EqualFold(t, strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

// vmHealth: a template and a running guest are fine; a guest of an offline node is unknown (the
// node's own item covers it); a stopped one is critical when it is a critical VM and a warning
// otherwise.
func vmHealth(critical bool, status string, template, nodeOffline bool) model.Health {
	switch {
	case template:
		return model.HealthOK
	case nodeOffline:
		return model.HealthUnknown
	case status == "running":
		return model.HealthOK
	case critical:
		return model.HealthCrit
	}
	return model.HealthWarn
}
