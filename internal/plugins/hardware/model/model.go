// Package model holds the payload types only the hardware plugin publishes. It is an import
// leaf: it may import nothing from dash (TestPluginModelPackagesAreImportLeaves), because
// internal/model.TopicTypes lists every topic's payload type and so imports this package.
// A payload that needs a core type (Link, Health, SourceStatus) stays in internal/model.
package model

// Machine is a machine dash watches that is neither a VM nor a cluster node: a box whose
// node-exporter is scraped, or whose reachability a Gatus check reports.
type Machine struct {
	Ref    string `json:"ref"` // hardware:Machine/<name>
	Name   string `json:"name"`
	Up     bool   `json:"up"`
	Source string `json:"source"` // "prometheus" or "gatus"
}

// MachinesData is the hardware.machines topic.
type MachinesData struct {
	// Machines is never null: [] when none is configured. The topic is still published, because
	// an unpublished topic leaves the web UI's read pending.
	Machines []Machine `json:"machines"`
}
