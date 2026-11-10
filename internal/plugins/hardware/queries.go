package hardware

import (
	"fmt"
	"strconv"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// The PromQL the plugin sends: the expressions of internal/queries' host and machine section,
// copied because the plugin owns them now, with the two selectors a setting. Every label value
// that comes from the configuration (the job, the chip, a nodename) goes through quote, so a value
// with a quote or a backslash in it stays one string literal and cannot add a matcher.

// quote renders a PromQL string literal.
func quote(s string) string { return strconv.Quote(s) }

// exprs builds the expressions of one configuration.
type exprs struct {
	job  string // plugins.hardware.selectors.nodeExporterJob
	chip string // plugins.hardware.selectors.hwmonChip, an RE2 regular expression
}

func newExprs(s pluginconf.HardwareSelectors) exprs {
	return exprs{job: s.NodeExporterJob, chip: s.HwmonChip}
}

// machineUp is the up series of a machine's node-exporter.
func (q exprs) machineUp(nodename string) string {
	return fmt.Sprintf(`up{job=%s,nodename=%s}`, quote(q.job), quote(nodename))
}

// machineStats are the queries of the node-exporter machines' stats, by name: one query per
// figure for every machine at once, each answering one sample per nodename, so the count does not
// grow with the machines. Memory falls back to macOS's names, which have no MemAvailable: used is
// what Activity Monitor calls Memory Used (app memory, wired and compressed). The disk is the
// root file system, without the initramfs rootfs that some Linux systems also mount on /. The
// temperature is the hottest package the hwmon chip selector picks (the same selector as the
// hosts'), else the hottest CPU thermal zone (single-board computers), both in °C.
func (q exprs) machineStats(nodenames []string) map[string]string {
	sel := fmt.Sprintf("job=%s,nodename=~%s", quote(q.job), nodenameRegex(nodenames))
	by := func(format string) string {
		return "max by (nodename) (" + fmt.Sprintf(format, sel) + ")"
	}
	return map[string]string{
		"uname":     fmt.Sprintf(`node_uname_info{%s}`, sel),
		"cpus":      fmt.Sprintf(`count by (nodename) (node_cpu_seconds_total{%s,mode="idle"})`, sel),
		"cpu":       fmt.Sprintf(`1 - avg by (nodename) (rate(node_cpu_seconds_total{%s,mode="idle"}[2m]))`, sel),
		"memTotal":  by(`node_memory_MemTotal_bytes{%s}`) + " or " + by(`node_memory_total_bytes{%s}`),
		"memUsed":   by(`node_memory_MemTotal_bytes{%[1]s} - node_memory_MemAvailable_bytes{%[1]s}`) + " or " + by(`node_memory_internal_bytes{%[1]s} - node_memory_purgeable_bytes{%[1]s} + node_memory_wired_bytes{%[1]s} + node_memory_compressed_bytes{%[1]s}`),
		"diskTotal": by(`node_filesystem_size_bytes{%s,mountpoint="/",fstype!="rootfs"}`),
		"diskAvail": by(`node_filesystem_avail_bytes{%s,mountpoint="/",fstype!="rootfs"}`),
		"load1":     by(`node_load1{%s}`),
		"load5":     by(`node_load5{%s}`),
		"load15":    by(`node_load15{%s}`),
		"uptime":    by(`node_time_seconds{%[1]s} - node_boot_time_seconds{%[1]s}`),
		"temp": "max by (nodename) (node_hwmon_temp_celsius{" + sel + ",chip=~" + quote(q.chip) + `,sensor="temp1"})` + " or " +
			by(`node_thermal_zone_temp{%s,type=~"cpu-thermal|cpu_thermal|x86_pkg_temp|soc-thermal|soc_thermal"}`),
	}
}

func (q exprs) cpu(n string) string {
	return fmt.Sprintf(`1 - avg(rate(node_cpu_seconds_total{nodename=%s,mode="idle"}[2m]))`, quote(n))
}

func (q exprs) memTotal(n string) string {
	return fmt.Sprintf(`node_memory_MemTotal_bytes{nodename=%s}`, quote(n))
}

func (q exprs) memAvailable(n string) string {
	return fmt.Sprintf(`node_memory_MemAvailable_bytes{nodename=%s}`, quote(n))
}

// mem is the used fraction of memory, for the host.mem series.
func (q exprs) mem(n string) string {
	return fmt.Sprintf(`1 - node_memory_MemAvailable_bytes{nodename=%[1]s} / node_memory_MemTotal_bytes{nodename=%[1]s}`, quote(n))
}

// packageTemps is the CPU package temperature, one sample per socket.
func (q exprs) packageTemps(n string) string {
	return fmt.Sprintf(`node_hwmon_temp_celsius{nodename=%s,chip=~%s,sensor="temp1"}`, quote(n), quote(q.chip))
}

func (q exprs) cpuTempMax(n string) string { return "max(" + q.packageTemps(n) + ")" }

// cpuTempMaxOf is the hottest package of several hosts, for the host.cpuTemp series without a
// node.
func (q exprs) cpuTempMaxOf(nodenames []string) string {
	return fmt.Sprintf(`max(node_hwmon_temp_celsius{nodename=~%s,chip=~%s,sensor="temp1"})`, nodenameRegex(nodenames), quote(q.chip))
}

// eachHost are the host.cpu, host.mem, host.temps and host.fans series of several hosts without
// a node: one line per host (and per sensor or fan), each keeping its nodename label, because
// sensor and fan names repeat across hosts.
func (q exprs) eachHost(series string, nodenames []string) string {
	r := nodenameRegex(nodenames)
	switch series {
	case "host.cpu":
		return fmt.Sprintf(`1 - avg by (nodename) (rate(node_cpu_seconds_total{nodename=~%s,mode="idle"}[2m]))`, r)
	case "host.mem":
		return fmt.Sprintf(`1 - node_memory_MemAvailable_bytes{nodename=~%[1]s} / node_memory_MemTotal_bytes{nodename=~%[1]s}`, r)
	case "host.temps":
		return fmt.Sprintf(`ipmi_temperature_celsius{nodename=~%s}`, r)
	case "host.fans":
		return fmt.Sprintf(`ipmi_fan_speed_rpm{nodename=~%s}`, r)
	}
	return ""
}

func (q exprs) sensors(n string) string {
	return fmt.Sprintf(`ipmi_temperature_celsius{nodename=%s}`, quote(n))
}

func (q exprs) fans(n string) string { return fmt.Sprintf(`ipmi_fan_speed_rpm{nodename=%s}`, quote(n)) }

func (q exprs) power(n string) string {
	return fmt.Sprintf(`ipmi_power_watts{nodename=%s}`, quote(n))
}

// powerSum is the power draw of several hosts together, for the host.power series without a node.
func (q exprs) powerSum(nodenames []string) string {
	return fmt.Sprintf(`sum(ipmi_power_watts{nodename=~%s})`, nodenameRegex(nodenames))
}

func (q exprs) psus(n string) string {
	return fmt.Sprintf(`ipmi_sensor_state{nodename=%s,type=~"Power Supply|Power Unit"}`, quote(n))
}

func (q exprs) selFree(n string) string {
	return fmt.Sprintf(`ipmi_sel_free_space_bytes{nodename=%s}`, quote(n))
}

func (q exprs) pools(n string) string {
	return fmt.Sprintf(`node_zfs_zpool_state{nodename=%s} == 1`, quote(n))
}

// The ZFS scrub series come from a textfile collector; prefix is a validated metric name prefix.

func (q exprs) scrubEnd(prefix, n string) string {
	return fmt.Sprintf(`%s_last_end_timestamp_seconds{nodename=%s}`, prefix, quote(n))
}

func (q exprs) scrubErrors(prefix, n string) string {
	return fmt.Sprintf(`%s_last_errors{nodename=%s}`, prefix, quote(n))
}

func (q exprs) scrubRunning(prefix, n string) string {
	return fmt.Sprintf(`%s_in_progress{nodename=%s}`, prefix, quote(n))
}

func (q exprs) smartHealthy(n string) string {
	return fmt.Sprintf(`smartmon_device_smart_healthy{nodename=%s}`, quote(n))
}

func (q exprs) smartTemp(n string) string {
	return fmt.Sprintf(`smartmon_temperature_celsius_raw_value{nodename=%s}`, quote(n))
}

func (q exprs) smartLifeRemain(n string) string {
	return fmt.Sprintf(`smartmon_percent_lifetime_remain_value{nodename=%s}`, quote(n))
}

func (q exprs) smartWearout(n string) string {
	return fmt.Sprintf(`smartmon_media_wearout_indicator_value{nodename=%s}`, quote(n))
}

// hostQueries is the host poll's query set, by name. The scrub queries are left out without a
// prefix: nothing is asked and the pool cards show no scrub data.
func (q exprs) hostQueries(n, scrubPrefix string) map[string]string {
	qs := map[string]string{
		"cpu":      q.cpu(n),
		"memTotal": q.memTotal(n),
		"memAvail": q.memAvailable(n),
		"pkg":      q.packageTemps(n),
		"sensors":  q.sensors(n),
		"fans":     q.fans(n),
		"power":    q.power(n),
		"psu":      q.psus(n),
		"sel":      q.selFree(n),
		"pools":    q.pools(n),
		"healthy":  q.smartHealthy(n),
		"temp":     q.smartTemp(n),
		"life":     q.smartLifeRemain(n),
		"wear":     q.smartWearout(n),
	}
	if scrubPrefix != "" {
		qs["scrubEnd"], qs["scrubErr"], qs["scrubRun"] = q.scrubEnd(scrubPrefix, n), q.scrubErrors(scrubPrefix, n), q.scrubRunning(scrubPrefix, n)
	}
	return qs
}
