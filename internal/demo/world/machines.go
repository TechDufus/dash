package world

import (
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// machineSpec is a fake machine: its source and, for a node-exporter one, what its node-exporter
// reports at rest.
type machineSpec struct {
	name, source     string
	os, arch, kernel string
	cpus             int
	cpu, memGiB, mem float64 // resting CPU ratio, memory size and used ratio
	diskGiB, disk    float64 // root file system size and used ratio
	load             float64 // resting 1-minute load
	upDays           int64
	tempC            float64 // resting CPU temperature; 0 reports none, as macOS does
}

// machineSpecs are the boxes outside the cluster: a Linux NAS with a hwmon temperature, a board
// reported by a Gatus check (up or down only), and a macOS workstation without temperatures.
var machineSpecs = []machineSpec{
	{
		name: "backup-nas", source: "prometheus", os: "Linux", arch: "x86_64", kernel: "6.12.48-1-lts",
		cpus: 8, cpu: 0.12, memGiB: 32, mem: 0.38, diskGiB: 220, disk: 0.31, load: 0.9, upDays: 64, tempC: 44,
	},
	{name: "edge-pi", source: "gatus"},
	{
		name: "studio-1", source: "prometheus", os: "Darwin", arch: "arm64", kernel: "25.0.0",
		cpus: 10, cpu: 0.08, memGiB: 16, mem: 0.62, diskGiB: 460, disk: 0.44, load: 1.6, upDays: 12,
	},
}

// Machines are the boxes outside the cluster that the fake homelab watches: the hardware.machines
// payload. One is reported by a Gatus check and has no stats, the others by their node-exporter.
func (w *World) Machines() []hwmodel.Machine {
	now := w.clock()
	out := make([]hwmodel.Machine, len(machineSpecs))
	for i, m := range machineSpecs {
		out[i] = hwmodel.Machine{Ref: plugin.Ref("hardware", "Machine", m.name), Name: m.name, Up: true, Source: m.source}
		if m.source == "prometheus" {
			out[i].Stats = m.stats(now, w.start)
		}
	}
	return out
}

func (m machineSpec) stats(now, start time.Time) *hwmodel.MachineStats {
	k := func(s string) string { return m.name + "/" + s }
	mem, disk := gib(m.memGiB), gib(m.diskGiB)
	load := Around(k("load"), now, m.load, m.load/3)
	st := &hwmodel.MachineStats{
		OS: m.os, Arch: m.arch, Kernel: m.kernel, CPUs: m.cpus,
		CPURatio:     Around(k("cpu"), now, m.cpu, 0.05),
		MemUsedBytes: int64(float64(mem) * Around(k("mem"), now, m.mem, 0.03)), MemTotalBytes: mem,
		DiskUsedBytes: int64(float64(disk) * m.disk), DiskTotalBytes: disk,
		Load1: load, Load5: load * 0.9, Load15: load * 0.8,
		UptimeSeconds: int64(now.Sub(start).Seconds()) + m.upDays*86400,
	}
	if m.tempC > 0 {
		st.TempC = new(Around(k("temp"), now, m.tempC, 3))
	}
	return st
}
