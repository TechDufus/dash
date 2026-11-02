package world

import (
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
)

// The facts of the fake Proxmox node. Host() draws the same ones onto the host payload until the
// Hardware page gets them from the proxmox plugin.
const (
	pveVersion  = "9.0.11"
	pveKernel   = "6.14.11-4-pve"
	pveCPUModel = "2 × Intel Xeon E5-2690 v4 @ 2.60GHz"
)

// PVENode is the proxmox.node payload: the fake node's version, kernel, CPU and uptime.
func (w *World) PVENode() pvemodel.NodeData { return w.pveNode(w.clock()) }

func (w *World) pveNode(now time.Time) pvemodel.NodeData {
	return pvemodel.NodeData{
		Ref: plugin.Ref("proxmox", "Node", HostName), Node: HostName,
		Version: pveVersion, Kernel: pveKernel, CPUModel: pveCPUModel,
		UptimeSeconds: int64(now.Sub(w.start).Seconds()) + 38*86400,
	}
}

// PVEStorages are the storages of the fake node with their usage: the proxmox.storage payload.
func (w *World) PVEStorages() []pvemodel.PVEStorage {
	spec := []struct {
		name       string
		used, size float64
	}{
		{"local", 18, 94},
		{"local-zfs", 612, 1700},
		{"tank", 4100, 10900},
	}
	out := make([]pvemodel.PVEStorage, len(spec))
	for i, s := range spec {
		out[i] = pvemodel.PVEStorage{
			Ref: plugin.Ref("proxmox", "Storage", s.name), Name: s.name, UsedBytes: gib(s.used), SizeBytes: gib(s.size),
		}
	}
	return out
}
