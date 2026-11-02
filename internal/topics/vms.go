package topics

import (
	"context"

	"github.com/TechDufus/dash/internal/model"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/queries"
)

// machinesSnap is the last reading of the machines dash watches that are neither VMs nor cluster
// nodes. The VMs themselves belong to the proxmox plugin.
type machinesSnap struct {
	macUp bool
}

// rpiGatusEndpoint is the Gatus check that tells whether the rpi-control Pi is reachable.
const rpiGatusEndpoint = "rpi-control SSH"

// pollVMs is the machines poller (named "vms" for the poller table): the node-exporter machine's
// reachability. It runs while the hardware plugin does, because the machines are published
// through its shim until P3.7 moves them into the plugin.
func (m *Manager) pollVMs(ctx context.Context) {
	m.mu.RLock()
	snap := m.machines
	m.mu.RUnlock()

	res := m.queryAll(ctx, map[string]string{"macUp": queries.MachineUp("mac-mini")})
	m.setSource("machines:prometheus", firstErr(res))
	if res["macUp"].err == nil {
		v, has := first(res["macUp"].samples)
		snap.macUp = has && v >= 1
	}

	m.mu.Lock()
	m.machines = snap
	m.mu.Unlock()
	m.mark(model.TopicHardwareMachines)
}

// buildMachines is the hardware.machines payload: never a null list.
func (m *Manager) buildMachines() hwmodel.MachinesData {
	m.mu.RLock()
	snap := m.machines
	m.mu.RUnlock()

	// A check that exists but has no results counts as down; one that does not exist is down too.
	rpi, _ := m.endpoints().ByName(rpiGatusEndpoint)
	return hwmodel.MachinesData{Machines: []hwmodel.Machine{
		{Ref: "hardware:Machine/mac-mini", Name: "mac-mini", Up: snap.macUp, Source: "prometheus"},
		{Ref: "hardware:Machine/rpi-control", Name: "rpi-control", Up: rpi, Source: "gatus"},
	}}
}
