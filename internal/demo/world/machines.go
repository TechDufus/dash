package world

import (
	"github.com/TechDufus/dash/internal/plugin"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// Machines are the boxes outside the cluster that the fake homelab watches: the hardware.machines
// payload. One is reported by a Gatus check, the others by their node-exporter.
func (w *World) Machines() []hwmodel.Machine {
	spec := []struct {
		name, source string
	}{
		{"backup-nas", "prometheus"},
		{"edge-pi", "gatus"},
		{"router-1", "prometheus"},
	}
	out := make([]hwmodel.Machine, len(spec))
	for i, m := range spec {
		out[i] = hwmodel.Machine{Ref: plugin.Ref("hardware", "Machine", m.name), Name: m.name, Up: true, Source: m.source}
	}
	return out
}
