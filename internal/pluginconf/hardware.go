package pluginconf

import "regexp"

var metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Hardware is plugins.hardware: the physical host behind the cluster.
type Hardware struct {
	PluginBase
	// Hosts has at most one entry until the hardware plugin supports several (G3).
	Hosts []HardwareHost `json:"hosts" doc:"The machine behind the cluster, as [{nodename, model}]. One entry is supported for now."`
	// ZFSScrubMetricPrefix names the scrub series: <prefix>_last_end_timestamp_seconds,
	// <prefix>_last_errors and <prefix>_in_progress.
	ZFSScrubMetricPrefix string `json:"zfsScrubMetricPrefix" doc:"Prefix of the ZFS scrub series: <prefix>_last_end_timestamp_seconds, <prefix>_last_errors and <prefix>_in_progress. Must be a valid metric name. Empty queries no scrub data and the pool cards show none."`
}

// HardwareHost is a machine whose node-exporter and IPMI series dash reads.
type HardwareHost struct {
	// Nodename is the nodename label of its series; empty falls back to the Proxmox node.
	Nodename string `json:"nodename" doc:"The nodename label of the host's node-exporter and IPMI series. Empty uses plugins.proxmox.node, or the node discovered from the Proxmox API."`
	// Model is shown next to the host's name, e.g. "Dell R730".
	Model string `json:"model" doc:"Shown next to the host's name, e.g. Dell R730."`
}

// Validate checks the host count and that the scrub prefix is a metric name.
func (h Hardware) Validate() error {
	var p problems
	if len(h.Hosts) > 1 {
		p.add("hosts", "has %d entries; one host is supported until the hardware plugin handles several", len(h.Hosts))
	}
	if pre := h.ZFSScrubMetricPrefix; pre != "" && !metricNameRE.MatchString(pre) { // empty hides the scrub panel
		p.add("zfsScrubMetricPrefix", "%q is not a metric name", pre)
	}
	return p.err()
}
