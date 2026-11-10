package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// VMs is the proxmox.vms payload of the fake lab: guests on both nodes of the cluster and recent
// tasks. The proxmox twin decorates the guests (ref, critical flag, links); the machines are
// Machines.
func (w *World) VMs() model.VMsData { return w.vms(w.clock()) }

func (w *World) vms(now time.Time) model.VMsData {
	type vmSpec struct {
		id      int
		name    string
		cpus    float64
		memGiB  float64
		running bool
		tags    []string
		k8s     string // the Kubernetes node the guest is
		pve     string // the Proxmox node it runs on
	}
	vmSpecs := []vmSpec{
		{101, "k8s-cp-1", 4, 16, true, []string{"k8s", "control-plane"}, "k8s-cp-1", HostName},
		{102, "k8s-cp-2", 4, 16, true, []string{"k8s", "control-plane"}, "k8s-cp-2", SecondNodeName},
		{103, "k8s-cp-3", 4, 16, true, []string{"k8s", "control-plane"}, "k8s-cp-3", HostName},
		{110, "ci-runner-1", 8, 12, true, []string{"ci"}, "", SecondNodeName},
		{120, "dev-sandbox", 2, 4, true, []string{"scratch"}, "", HostName},
		{9000, "k8s-template", 2, 4, false, []string{"template"}, "", HostName},
	}
	var d model.VMsData
	for _, v := range vmSpecs {
		vm := model.VM{
			VMID: v.id, Node: v.pve, Name: v.name, Status: "stopped", Health: model.HealthOK, Tags: v.tags, CPUs: v.cpus,
			MaxMemBytes: gib(v.memGiB), Template: v.id == 9000, Lock: "",
			Links: []model.Link{}, // the proxmox twin adds the console and Proxmox links
		}
		if v.running {
			vm.Status = "running"
			vm.CPURatio = Around(v.name+"cpu", now, 0.21, 0.09)
			vm.MemBytes = int64(float64(vm.MaxMemBytes) * Around(v.name+"mem", now, 0.62, 0.05))
			vm.UptimeSeconds = int64(now.Sub(w.start).Seconds()) + 12*86400 + int64(v.id)*977
			vm.NetInBytes = 41<<30 + int64(v.id)<<24
			vm.NetOutBytes = 17<<30 + int64(v.id)<<23
		}
		if v.k8s != "" {
			vm.K8sNode = &model.K8sNodeRef{Name: v.k8s, Ready: true}
		}
		d.VMs = append(d.VMs, vm)
	}
	d.Tasks = []model.PVETask{
		{UPID: "UPID:pve-demo:0001:qmsnapshot", Node: HostName, Type: "qmsnapshot", ID: "101", User: "root@pam", Status: "OK", StartTime: ago(now, 26*time.Hour), EndTime: agoP(now, 26*time.Hour-40*time.Second)},
		{UPID: "UPID:pve-demo-2:0002:vzdump", Node: SecondNodeName, Type: "vzdump", ID: "110", User: "root@pam", Status: "OK", StartTime: ago(now, 9*time.Hour), EndTime: agoP(now, 9*time.Hour-6*time.Minute)},
		{UPID: "UPID:pve-demo:0003:qmstop", Node: HostName, Type: "qmstop", ID: "120", User: "root@pam", Status: "OK", StartTime: ago(now, 3*24*time.Hour), EndTime: agoP(now, 3*24*time.Hour-5*time.Second)},
	}
	return d
}
