package pluginconf

import (
	"regexp"
	"strings"
)

var metricNameRE = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Limits of the hardware section's numbers; Validate enforces them.
const (
	maxBays      = 128
	maxFanMaxRPM = 100000
	// MaxMachineName is the longest machine name, in bytes.
	MaxMachineName = 64
)

// Hardware is plugins.hardware: the physical host behind the cluster, and the machines next to it.
type Hardware struct {
	PluginBase
	// Hosts has at most one entry until the hardware plugin supports several (G3).
	Hosts []HardwareHost `json:"hosts" doc:"The machine behind the cluster, as [{name, nodename, model, bays, fanMaxRPM, sensorAliases}]; every key is optional, and [{}] reads the Proxmox node, which needs the proxmox plugin to run (its URL, token ID and token secret set) unless nodename is set. One entry is supported for now. A JSON list in the environment."`
	// Machines are the boxes outside the cluster whose reachability dash shows.
	Machines []HardwareMachine `json:"machines" doc:"Machines outside the cluster to show as up or down, as [{name, nodeExporterNodename}] (up while their node-exporter is scraped) or [{name, gatusEndpoint}] (up while that Gatus check passes). A JSON list in the environment."`
	// Selectors are the label values the queries select the host and the machines by.
	Selectors HardwareSelectors `json:"selectors" doc:"The label values dash's queries select by."`
	// TempThresholds are the CPU package temperatures that turn the host warning and critical.
	TempThresholds TempThresholds `json:"tempThresholds" doc:"CPU package temperature, in °C, at which the host turns warning and critical (whole degrees)."`
	// ZFSScrubMetricPrefix names the scrub series: <prefix>_last_end_timestamp_seconds,
	// <prefix>_last_errors and <prefix>_in_progress.
	ZFSScrubMetricPrefix string `json:"zfsScrubMetricPrefix" doc:"Prefix of the ZFS scrub series: <prefix>_last_end_timestamp_seconds, <prefix>_last_errors and <prefix>_in_progress. Must be a valid metric name. Empty queries no scrub data and the pool cards show none."`
}

// HardwareHost is a machine whose node-exporter and IPMI series dash reads.
type HardwareHost struct {
	// Name is shown as the host's name; empty uses the Proxmox node's name.
	Name string `json:"name" doc:"The host's name in the web UI. Empty uses the Proxmox node's name, then the nodename."`
	// Nodename is the nodename label of its series; empty falls back to the node of the running
	// proxmox plugin.
	Nodename string `json:"nodename" doc:"The nodename label of the host's node-exporter and IPMI series. Empty uses the node of the running proxmox plugin (plugins.proxmox.node, or the node it discovers), so the plugin needs its URL, token ID and token secret; without it the Hardware row stays unavailable (waiting for the Proxmox node name) until nodename is set."`
	// Model is shown next to the host's name, e.g. "Dell R730".
	Model string `json:"model" doc:"Shown next to the host's name, e.g. Dell R730."`
	// Bays is the number of drive bays the Disks grid draws; 0 numbers the slots from 1 (from 0
	// when a disk sits in slot 0) up to the highest one that has a disk, at most 128.
	Bays int `json:"bays" doc:"Drive bays the Disks grid draws, 0 to 128. 0 numbers the slots from 1 (from 0 when a disk sits in slot 0) up to the highest one that holds a disk, at most 128; above that, or when the disks' bay labels are not distinct numbers, the page lists the disks sorted."`
	// FanMaxRPM is the top of the fan gauges; 0 scales them to the fastest fan observed.
	FanMaxRPM int `json:"fanMaxRPM" doc:"Top of the fan gauges, in RPM, 0 to 100000. 0 scales them to the fastest fan seen. A gauge never ends below the speed a fan reports."`
	// SensorAliases pick the sensors the Inlet and Exhaust tiles show.
	SensorAliases HardwareSensorAliases `json:"sensorAliases" doc:"Which temperature sensors the Inlet and Exhaust tiles read."`
}

// HardwareSensorAliases name the temperature sensors of the Inlet and Exhaust tiles: a sensor
// matches when its name contains the alias, ignoring case.
type HardwareSensorAliases struct {
	Inlet   string `json:"inlet" doc:"Text the inlet sensor's name contains, ignoring case. Empty means inlet."`
	Exhaust string `json:"exhaust" doc:"Text the exhaust sensor's name contains, ignoring case. Empty means exhaust."`
}

// HardwareMachine is a machine outside the cluster; exactly one way of telling that it is up.
type HardwareMachine struct {
	Name                 string `json:"name" doc:"The machine's name in the web UI, unique ignoring case, at most 64 bytes."`
	NodeExporterNodename string `json:"nodeExporterNodename" doc:"The nodename label of the machine's node-exporter: it is up while the node-exporter is scraped. Exactly one of nodeExporterNodename and gatusEndpoint."`
	GatusEndpoint        string `json:"gatusEndpoint" doc:"The name of the Gatus check that tells whether the machine is reachable (compared without regard to case). A check that does not exist counts as down. Exactly one of nodeExporterNodename and gatusEndpoint."`
}

// HardwareSelectors are the label values the hardware queries select by.
type HardwareSelectors struct {
	NodeExporterJob string `json:"nodeExporterJob" doc:"The job label of the node-exporter targets, which the machines' up queries select."`
	HwmonChip       string `json:"hwmonChip" doc:"RE2 regular expression for the chip label of the CPU package temperature (node_hwmon_temp_celsius), e.g. platform_coretemp_.* for Intel."`
}

// TempThresholds are whole degrees Celsius: the environment has no column for fractions.
type TempThresholds struct {
	Warn int `json:"warn" doc:"The CPU package temperature, in °C, from which the host is warning. Greater than zero and below crit."`
	Crit int `json:"crit" doc:"The CPU package temperature, in °C, from which the host is critical."`
}

// DefaultHardware is the section with nothing set: the conventions of a node-exporter and IPMI
// setup. Nothing about a particular lab has a default.
func DefaultHardware() Hardware {
	return Hardware{
		Selectors:      HardwareSelectors{NodeExporterJob: "node-exporter", HwmonChip: "platform_coretemp_.*"},
		TempThresholds: TempThresholds{Warn: 85, Crit: 90},
	}
}

// Normalized returns h with the whitespace of every name trimmed and the sensor aliases'
// defaults filled in, so that the plugin reads one form. h itself and its slices are not changed.
func (h Hardware) Normalized() Hardware {
	h.Hosts = append([]HardwareHost(nil), h.Hosts...)
	for i := range h.Hosts {
		x := &h.Hosts[i]
		x.Name, x.Nodename, x.Model = strings.TrimSpace(x.Name), strings.TrimSpace(x.Nodename), strings.TrimSpace(x.Model)
		if x.SensorAliases.Inlet = strings.TrimSpace(x.SensorAliases.Inlet); x.SensorAliases.Inlet == "" {
			x.SensorAliases.Inlet = "inlet"
		}
		if x.SensorAliases.Exhaust = strings.TrimSpace(x.SensorAliases.Exhaust); x.SensorAliases.Exhaust == "" {
			x.SensorAliases.Exhaust = "exhaust"
		}
	}
	h.Machines = append([]HardwareMachine(nil), h.Machines...)
	for i := range h.Machines {
		m := &h.Machines[i]
		m.Name, m.NodeExporterNodename, m.GatusEndpoint = strings.TrimSpace(m.Name), strings.TrimSpace(m.NodeExporterNodename), strings.TrimSpace(m.GatusEndpoint)
	}
	return h
}

// Validate checks the host count, the numbers' ranges, the machines, the selectors and that the
// scrub prefix is a metric name.
func (h Hardware) Validate() error {
	var p problems
	if len(h.Hosts) > 1 {
		p.add("hosts", "has %d entries; one host is supported until the hardware plugin handles several", len(h.Hosts))
	}
	for i, x := range h.Hosts {
		if x.Bays < 0 || x.Bays > maxBays {
			p.add("hosts", "entry %d: bays is %d, want 0 to %d", i, x.Bays, maxBays)
		}
		if x.FanMaxRPM < 0 || x.FanMaxRPM > maxFanMaxRPM {
			p.add("hosts", "entry %d: fanMaxRPM is %d, want 0 to %d", i, x.FanMaxRPM, maxFanMaxRPM)
		}
	}
	seen := map[string]bool{}
	for i, m := range h.Machines {
		name := strings.TrimSpace(m.Name)
		switch {
		case name == "":
			p.add("machines", "entry %d: name is required", i)
		case len(name) > MaxMachineName:
			p.add("machines", "entry %d: name is %d bytes, at most %d", i, len(name), MaxMachineName)
		case seen[strings.ToLower(name)]:
			p.add("machines", "entry %d: name %q repeats an earlier machine (names are compared ignoring case)", i, name)
		}
		seen[strings.ToLower(name)] = true
		ne, ge := strings.TrimSpace(m.NodeExporterNodename) != "", strings.TrimSpace(m.GatusEndpoint) != ""
		if ne == ge {
			p.add("machines", "entry %d (%q): set exactly one of nodeExporterNodename and gatusEndpoint", i, name)
		}
	}
	if strings.TrimSpace(h.Selectors.NodeExporterJob) == "" {
		p.add("selectors.nodeExporterJob", "is required")
	}
	if chip := h.Selectors.HwmonChip; strings.TrimSpace(chip) == "" {
		p.add("selectors.hwmonChip", "is required")
	} else if _, err := regexp.Compile(chip); err != nil {
		p.add("selectors.hwmonChip", "%q is not a valid regular expression", chip)
	}
	if t := h.TempThresholds; t.Warn <= 0 || t.Crit <= t.Warn {
		p.add("tempThresholds", "needs 0 < warn < crit, got warn %d and crit %d", t.Warn, t.Crit)
	}
	if pre := h.ZFSScrubMetricPrefix; pre != "" && !metricNameRE.MatchString(pre) { // empty hides the scrub panel
		p.add("zfsScrubMetricPrefix", "%q is not a metric name", pre)
	}
	return p.err()
}
