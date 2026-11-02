// Package queries holds every PromQL expression dash sends to VictoriaMetrics, so the topics
// (instant values) and /api/v1/series (ranges) share one definition.
package queries

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NodeExporterPort is the node-exporter port; Kubernetes node series carry instance="<ip>:9100".
const NodeExporterPort = 9100

// Pod usage (all pods), topic cluster.
const (
	PodCPU = `sum by (namespace,pod)(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD"}[2m]))`
	PodMem = `sum by (namespace,pod)(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD"})`
)

// Kubernetes node usage keyed by instance.
const (
	NodeCPU = `1 - avg by (instance)(rate(node_cpu_seconds_total{job="node-exporter",mode="idle"}[2m]))`
	NodeMem = `1 - node_memory_MemAvailable_bytes{job="node-exporter"} / node_memory_MemTotal_bytes{job="node-exporter"}`
)

// Storage topic.
const (
	PVCUsed     = `max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_used_bytes)`
	PVCCapacity = `max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_capacity_bytes)`
	PVCGrowth   = `deriv(max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_used_bytes)[7d:15m]) * 86400`

	BucketUsed   = `max by (bucket)(rustfs_bucket_usage_usage_bytes)`
	BucketQuota  = `max by (bucket)(rustfs_bucket_usage_quota_bytes)`
	RustFSUsed   = `max(rustfs_cluster_capacity_used_bytes)`
	RustFSUsable = `max(rustfs_cluster_capacity_usable_total_bytes)`

	CNPGBackupAge = `time() - max by (namespace,cluster)(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp{role="primary"})`

	EtcdDB    = `max(etcd_mvcc_db_total_size_in_bytes{job="etcd"})`
	EtcdQuota = `max(etcd_server_quota_backend_bytes{job="etcd"})`
)

// NASBackupAge is the age of the last successful run of the NAS backup CronJob(s): the CronJob
// named cronjob, or cronjob-<anything>, in namespace ns.
func NASBackupAge(ns, cronjob string) string {
	return fmt.Sprintf(`time() - kube_cronjob_status_last_successful_time{namespace=%s,cronjob=~%s}`,
		quote(ns), quote(regexp.QuoteMeta(cronjob)+"(-.+)?"))
}

// MachineUp is the up series of a non-Kubernetes machine's node-exporter.
func MachineUp(nodename string) string {
	return fmt.Sprintf(`up{job="node-exporter",nodename=%s}`, quote(nodename))
}

// Host expressions; n is DASH_HOST_NODENAME.

func HostCPU(n string) string {
	return fmt.Sprintf(`1 - avg(rate(node_cpu_seconds_total{nodename=%s,mode="idle"}[2m]))`, quote(n))
}
func HostMemTotal(n string) string {
	return fmt.Sprintf(`node_memory_MemTotal_bytes{nodename=%s}`, quote(n))
}
func HostMemAvailable(n string) string {
	return fmt.Sprintf(`node_memory_MemAvailable_bytes{nodename=%s}`, quote(n))
}
func HostMem(n string) string {
	return fmt.Sprintf(`1 - node_memory_MemAvailable_bytes{nodename=%[1]s} / node_memory_MemTotal_bytes{nodename=%[1]s}`, quote(n))
}
func HostPackageTemps(n string) string {
	return fmt.Sprintf(`node_hwmon_temp_celsius{nodename=%s,chip=~"platform_coretemp_.*",sensor="temp1"}`, quote(n))
}
func HostCPUTempMax(n string) string { return "max(" + HostPackageTemps(n) + ")" }
func HostSensors(n string) string {
	return fmt.Sprintf(`ipmi_temperature_celsius{nodename=%s}`, quote(n))
}
func HostFans(n string) string { return fmt.Sprintf(`ipmi_fan_speed_rpm{nodename=%s}`, quote(n)) }
func HostPower(n string) string {
	return fmt.Sprintf(`ipmi_power_watts{nodename=%s}`, quote(n))
}
func HostPSUs(n string) string {
	return fmt.Sprintf(`ipmi_sensor_state{nodename=%s,type=~"Power Supply|Power Unit"}`, quote(n))
}
func HostSELFree(n string) string {
	return fmt.Sprintf(`ipmi_sel_free_space_bytes{nodename=%s}`, quote(n))
}
func HostPools(n string) string {
	return fmt.Sprintf(`node_zfs_zpool_state{nodename=%s} == 1`, quote(n))
}

// The ZFS scrub series come from a textfile collector; prefix is a validated metric name prefix.
func HostScrubEnd(prefix, n string) string {
	return fmt.Sprintf(`%s_last_end_timestamp_seconds{nodename=%s}`, prefix, quote(n))
}
func HostScrubErrors(prefix, n string) string {
	return fmt.Sprintf(`%s_last_errors{nodename=%s}`, prefix, quote(n))
}
func HostScrubRunning(prefix, n string) string {
	return fmt.Sprintf(`%s_in_progress{nodename=%s}`, prefix, quote(n))
}
func HostSMARTHealthy(n string) string {
	return fmt.Sprintf(`smartmon_device_smart_healthy{nodename=%s}`, quote(n))
}
func HostSMARTTemp(n string) string {
	return fmt.Sprintf(`smartmon_temperature_celsius_raw_value{nodename=%s}`, quote(n))
}
func HostSMARTLifeRemain(n string) string {
	return fmt.Sprintf(`smartmon_percent_lifetime_remain_value{nodename=%s}`, quote(n))
}
func HostSMARTWearout(n string) string {
	return fmt.Sprintf(`smartmon_media_wearout_indicator_value{nodename=%s}`, quote(n))
}

// quote renders a PromQL string literal.
func quote(s string) string { return strconv.Quote(s) }

// Args are the validated parameters of a /api/v1/series request.
type Args struct {
	HostNodename string // DASH_HOST_NODENAME
	VMID         string // digits only
	Instance     string // "<node InternalIP>:9100"
	Namespace    string // DNS-1123
	Pod          string // DNS-1123
}

// ErrBadSeries is returned for an unknown series name or a missing argument.
var ErrBadSeries = errors.New("bad series request")

var (
	vmidRE = regexp.MustCompile(`^[0-9]{1,9}$`)
	dnsRE  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
)

// ValidVMID reports whether s is an acceptable vmid argument.
func ValidVMID(s string) bool { return vmidRE.MatchString(s) }

// ValidDNS reports whether s is an acceptable namespace or pod name argument.
func ValidDNS(s string) bool { return dnsRE.MatchString(s) }

// Series returns the PromQL expression of a named /api/v1/series.
func Series(name string, a Args) (string, error) {
	switch name {
	case "host.power":
		return HostPower(a.HostNodename), nil
	case "host.cpuTemp":
		return HostCPUTempMax(a.HostNodename), nil
	case "host.cpu":
		return HostCPU(a.HostNodename), nil
	case "host.mem":
		return HostMem(a.HostNodename), nil
	case "host.temps":
		return HostSensors(a.HostNodename), nil
	case "host.fans":
		return HostFans(a.HostNodename), nil
	case "node.cpu":
		if a.Instance == "" {
			return "", fmt.Errorf("%w: node required", ErrBadSeries)
		}
		return strings.Replace(NodeCPU, `job="node-exporter",mode="idle"`, `job="node-exporter",instance=`+quote(a.Instance)+`,mode="idle"`, 1), nil
	case "node.mem":
		if a.Instance == "" {
			return "", fmt.Errorf("%w: node required", ErrBadSeries)
		}
		sel := `{job="node-exporter",instance=` + quote(a.Instance) + `}`
		return `1 - node_memory_MemAvailable_bytes` + sel + ` / node_memory_MemTotal_bytes` + sel, nil
	case "pod.cpu", "pod.mem":
		if !ValidDNS(a.Namespace) || !ValidDNS(a.Pod) {
			return "", fmt.Errorf("%w: namespace and pod required", ErrBadSeries)
		}
		sel := `namespace=` + quote(a.Namespace) + `,pod=` + quote(a.Pod)
		if name == "pod.cpu" {
			return `sum(rate(container_cpu_usage_seconds_total{metrics_path="/metrics/cadvisor",container!="",container!="POD",` + sel + `}[2m]))`, nil
		}
		return `sum(container_memory_working_set_bytes{metrics_path="/metrics/cadvisor",container!="",container!="POD",` + sel + `})`, nil
	}
	return "", fmt.Errorf("%w: unknown series %q", ErrBadSeries, name)
}

// Range maps a range name to its window and step.
func Range(name string) (window, step time.Duration, ok bool) {
	switch name {
	case "15m":
		return 15 * time.Minute, 15 * time.Second, true
	case "1h":
		return time.Hour, 30 * time.Second, true
	case "6h":
		return 6 * time.Hour, 2 * time.Minute, true
	case "24h":
		return 24 * time.Hour, 5 * time.Minute, true
	case "7d":
		return 7 * 24 * time.Hour, 30 * time.Minute, true
	}
	return 0, 0, false
}
