package proxmox

import (
	"fmt"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
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

// Attention raises an item for every guest that is not running and is not a template: critical
// for a critical VM, a warning for any other. Since is when the plugin first saw the VM stopped.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	d := r.current()
	if d == nil {
		return nil
	}
	return r.fs.Stamp(now, attentionOf(d.VMs))
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

// Topology is the guests, templates left out, with the cluster node each one is.
func (p *Plugin) Topology() model.Topology {
	t := model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	r := p.rt.Load()
	if r == nil {
		return t
	}
	if d := r.current(); d != nil {
		t.VMs = topologyOf(d.VMs)
	}
	return t
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
			VMID: vm.VMID, Name: vm.Name, Health: vm.Health, CPURatio: vm.CPURatio, MemRatio: mem,
			IsK8sNode: vm.K8sNode != nil, Running: vm.Status == "running",
		})
	}
	return out
}
