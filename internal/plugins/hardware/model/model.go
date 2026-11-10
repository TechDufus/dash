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
	// Stats are the machine's node-exporter readings. Nil for a Gatus machine, while a
	// node-exporter machine is down, and until a poll finds its node_uname_info or CPU series.
	Stats *MachineStats `json:"stats,omitempty"`
}

// MachineStats are what a machine's node-exporter reports, Linux and macOS alike. Each figure is
// the last one read: a failed query keeps it, a query that succeeds without the machine clears
// it. A zero total or count means the figure is not reported, so the UI hides it rather than
// drawing an empty bar.
type MachineStats struct {
	// OS, Arch and Kernel are node_uname_info's sysname (Linux, Darwin), machine (x86_64, arm64)
	// and release; empty when not reported.
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Kernel string `json:"kernel"`
	// CPUs counts the logical CPUs, 0 when the CPU is not reported; CPURatio is their use, 0 to 1.
	CPUs     int     `json:"cpus"`
	CPURatio float64 `json:"cpuRatio"`
	// MemUsedBytes is MemTotal - MemAvailable on Linux, and on macOS (which has no MemAvailable)
	// what Activity Monitor calls Memory Used: app memory, wired and compressed. MemTotalBytes 0
	// means memory is not reported.
	MemUsedBytes  int64 `json:"memUsedBytes"`
	MemTotalBytes int64 `json:"memTotalBytes"`
	// DiskUsedBytes and DiskTotalBytes are the root file system's (on macOS the APFS container's).
	// DiskTotalBytes 0 means the disk is not reported.
	DiskUsedBytes  int64   `json:"diskUsedBytes"`
	DiskTotalBytes int64   `json:"diskTotalBytes"`
	Load1          float64 `json:"load1"`
	Load5          float64 `json:"load5"`
	Load15         float64 `json:"load15"`
	// UptimeSeconds 0 means uptime is not reported.
	UptimeSeconds int64 `json:"uptimeSeconds"`
	// TempC is the hottest CPU reading where the machine reports one (hwmon with the shared
	// selectors.hwmonChip, else a CPU thermal zone); nil when it reports none, as macOS does.
	TempC *float64 `json:"tempC,omitempty"`
}

// MachinesData is the hardware.machines topic.
type MachinesData struct {
	// Machines is never null: [] when none is configured. The topic is still published, because
	// an unpublished topic leaves the web UI's read pending.
	Machines []Machine `json:"machines"`
}
