package pluginconf

// The defaults of plugins.rustfs.metrics: the series RustFS exports.
const (
	RustFSBucketUsed     = "rustfs_bucket_usage_usage_bytes"
	RustFSBucketQuota    = "rustfs_bucket_usage_quota_bytes"
	RustFSCapacityUsed   = "rustfs_cluster_capacity_used_bytes"
	RustFSCapacityUsable = "rustfs_cluster_capacity_usable_total_bytes"
)

// RustFS is plugins.rustfs: the buckets and capacity of a RustFS object store, read from
// Prometheus. Unlike the other plugins it is off until plugins.rustfs.enabled is true: there is
// nothing to configure that says an object store exists.
type RustFS struct {
	PluginBase
	Metrics RustFSMetrics `json:"metrics" doc:"The names of the Prometheus series dash reads. Each must be a valid metric name."`
}

// RustFSMetrics are the series the plugin reads.
type RustFSMetrics struct {
	BucketUsed     string `json:"bucketUsed" doc:"Series of a bucket's used bytes, labelled bucket. The default is rustfs_bucket_usage_usage_bytes."`
	BucketQuota    string `json:"bucketQuota" doc:"Series of a bucket's quota in bytes, labelled bucket. A bucket without one has no quota. The default is rustfs_bucket_usage_quota_bytes."`
	CapacityUsed   string `json:"capacityUsed" doc:"Series of the cluster's used bytes. The default is rustfs_cluster_capacity_used_bytes."`
	CapacityUsable string `json:"capacityUsable" doc:"Series of the cluster's usable bytes. The default is rustfs_cluster_capacity_usable_total_bytes."`
}

// DefaultRustFS is the section with nothing set: RustFS's own series names. Enabled stays unset,
// which keeps the plugin off.
func DefaultRustFS() RustFS {
	return RustFS{Metrics: RustFSMetrics{
		BucketUsed: RustFSBucketUsed, BucketQuota: RustFSBucketQuota,
		CapacityUsed: RustFSCapacityUsed, CapacityUsable: RustFSCapacityUsable,
	}}
}

// Effective is m with the default of every name that is empty.
func (m RustFSMetrics) Effective() RustFSMetrics {
	or := func(v, d string) string {
		if v == "" {
			return d
		}
		return v
	}
	return RustFSMetrics{
		BucketUsed:     or(m.BucketUsed, RustFSBucketUsed),
		BucketQuota:    or(m.BucketQuota, RustFSBucketQuota),
		CapacityUsed:   or(m.CapacityUsed, RustFSCapacityUsed),
		CapacityUsable: or(m.CapacityUsable, RustFSCapacityUsable),
	}
}

// Validate checks that every metric is a metric name: they are put into PromQL expressions.
func (r RustFS) Validate() error {
	var p problems
	for _, f := range []struct{ path, name string }{
		{"metrics.bucketUsed", r.Metrics.BucketUsed},
		{"metrics.bucketQuota", r.Metrics.BucketQuota},
		{"metrics.capacityUsed", r.Metrics.CapacityUsed},
		{"metrics.capacityUsable", r.Metrics.CapacityUsable},
	} {
		if f.name != "" && !metricNameRE.MatchString(f.name) {
			p.add(f.path, notMetricName)
		}
	}
	return p.err()
}
