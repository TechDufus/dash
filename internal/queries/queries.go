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

// quote renders a PromQL string literal.
func quote(s string) string { return strconv.Quote(s) }

// Args are the validated parameters of a /api/v1/series request.
type Args struct {
	VMID      string // digits only
	Instance  string // "<node InternalIP>:9100"
	Namespace string // DNS-1123
	Pod       string // DNS-1123
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
