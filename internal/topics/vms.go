package topics

import (
	"context"
	"sort"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/upstream"
)

type vmsSnap struct {
	vms   []upstream.PVEVM
	tasks []model.PVETask
	macUp bool
}

// rpiGatusEndpoint is the Gatus check that tells whether the rpi-control Pi is reachable.
const rpiGatusEndpoint = "rpi-control SSH"

func (m *Manager) pollVMs(ctx context.Context) {
	m.mu.RLock()
	snap := m.vms
	m.mu.RUnlock()

	if m.d.PVE.Enabled() {
		vms, err := m.d.PVE.VMs(ctx)
		if err == nil {
			snap.vms = vms
			var terr error
			snap.tasks, terr = m.d.PVE.Tasks(ctx)
			if terr != nil {
				err = terr
			}
		}
		m.setSource("vms:proxmox", err)
	} else {
		m.setSource("vms:proxmox", upstream.ErrDisabled)
	}

	res := m.queryAll(ctx, map[string]string{"macUp": queries.MachineUp("mac-mini")})
	m.setSource("vms:victoriametrics", firstErr(res))
	if res["macUp"].err == nil {
		v, has := first(res["macUp"].samples)
		snap.macUp = has && v >= 1
	}

	m.mu.Lock()
	m.vms = snap
	m.mu.Unlock()
	m.mark(model.TopicVMs)
}

// k8sNodes indexes the Kubernetes nodes by name with their Ready condition.
func (m *Manager) k8sNodes() map[string]bool {
	out := map[string]bool{}
	for _, n := range m.d.Cluster.ListNodes() {
		out[n.Name] = nodeReady(n.Status.Conditions)
	}
	return out
}

func (m *Manager) buildVMs() model.VMsData {
	m.mu.RLock()
	snap := m.vms
	status := m.status
	m.mu.RUnlock()
	nodes := m.k8sNodes()

	out := model.VMsData{VMs: []model.VM{}, Machines: []model.Machine{}, Tasks: snap.tasks}
	if out.Tasks == nil {
		out.Tasks = []model.PVETask{}
	}
	for _, v := range snap.vms {
		vm := model.VM{
			VMID: v.VMID, Name: v.Name, Status: v.Status, Health: VMHealth(v.VMID, v.Status, v.Template),
			Tags: v.Tags, CPUs: v.CPUs, CPURatio: v.CPURatio, MemBytes: v.MemBytes, MaxMemBytes: v.MaxMemBytes,
			UptimeSeconds: v.UptimeSeconds, NetInBytes: v.NetInBytes, NetOutBytes: v.NetOutBytes,
			Template: v.Template, Lock: v.Lock,
			Links: append(m.d.Links.VMConsole(v.VMID, v.Name), m.d.Links.ProxmoxUI()...),
		}
		if vm.Tags == nil {
			vm.Tags = []string{}
		}
		if ready, ok := nodes[v.Name]; ok {
			vm.K8sNode = &model.K8sNodeRef{Name: v.Name, Ready: ready}
			vm.Links = append(vm.Links, m.d.Links.Headlamp("Node", "", v.Name)...)
		}
		out.VMs = append(out.VMs, vm)
	}
	sort.Slice(out.VMs, func(i, j int) bool { return out.VMs[i].VMID < out.VMs[j].VMID })

	rpi := false
	for _, e := range status {
		if strings.EqualFold(e.Name, rpiGatusEndpoint) {
			rpi = e.Healthy
		}
	}
	out.Machines = []model.Machine{
		{Name: "mac-mini", Up: snap.macUp, Source: "victoriametrics"},
		{Name: "rpi-control", Up: rpi, Source: "gatus"},
	}
	return out
}

// VMDetail assembles a VM's configuration, guest-agent IPs and snapshots from Proxmox.
func (m *Manager) VMDetail(ctx context.Context, vmid int) (model.VMDetail, error) {
	m.mu.RLock()
	built := m.built.vms
	m.mu.RUnlock()
	if built == nil {
		built = &model.VMsData{}
		if d := m.buildVMs(); d.VMs != nil {
			built = &d
		}
	}
	var vm *model.VM
	for i := range built.VMs {
		if built.VMs[i].VMID == vmid {
			vm = &built.VMs[i]
		}
	}
	if vm == nil {
		return model.VMDetail{}, ErrNotFound
	}
	cfg, err := m.d.PVE.VMConfig(ctx, vmid)
	if err != nil {
		return model.VMDetail{}, err
	}
	snaps, err := m.d.PVE.Snapshots(ctx, vmid)
	if err != nil {
		return model.VMDetail{}, err
	}
	d := model.VMDetail{VM: *vm, Config: cfg, IPs: m.d.PVE.VMIPs(ctx, vmid), Snapshots: snaps}
	if d.IPs == nil {
		d.IPs = []model.IfaceAddr{}
	}
	if d.Snapshots == nil {
		d.Snapshots = []model.Snapshot{}
	}
	if d.Config.Disks == nil {
		d.Config.Disks = []model.KeySpec{}
	}
	if d.Config.NICs == nil {
		d.Config.NICs = []model.KeySpec{}
	}
	return d, nil
}
