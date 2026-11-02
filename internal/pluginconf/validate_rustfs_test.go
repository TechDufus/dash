package pluginconf

import (
	"slices"
	"strings"
	"testing"
)

func TestRustFSDefaultsAreValidAndOff(t *testing.T) {
	d := DefaultRustFS()
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	want := RustFSMetrics{
		BucketUsed: "rustfs_bucket_usage_usage_bytes", BucketQuota: "rustfs_bucket_usage_quota_bytes",
		CapacityUsed: "rustfs_cluster_capacity_used_bytes", CapacityUsable: "rustfs_cluster_capacity_usable_total_bytes",
	}
	if d.Metrics != want {
		t.Errorf("default metrics = %+v", d.Metrics)
	}
	if d.Enabled != nil {
		t.Errorf("enabled = %v: rustfs is opt-in, so the default leaves it unset", *d.Enabled)
	}
}

func TestRustFSEffectiveMetricsFillOnlyTheEmptyNames(t *testing.T) {
	got := RustFSMetrics{BucketUsed: "my_used"}.Effective()
	want := DefaultRustFS().Metrics
	want.BucketUsed = "my_used"
	if got != want {
		t.Errorf("effective = %+v, want %+v", got, want)
	}
}

func TestRustFSMetricNamesAreValidated(t *testing.T) {
	set := map[string]func(*RustFS, string){
		"metrics.bucketUsed":     func(r *RustFS, v string) { r.Metrics.BucketUsed = v },
		"metrics.bucketQuota":    func(r *RustFS, v string) { r.Metrics.BucketQuota = v },
		"metrics.capacityUsed":   func(r *RustFS, v string) { r.Metrics.CapacityUsed = v },
		"metrics.capacityUsable": func(r *RustFS, v string) { r.Metrics.CapacityUsable = v },
	}
	for path, apply := range set {
		for _, ok := range []string{"", "a_b", "a:b"} {
			r := DefaultRustFS()
			apply(&r, ok)
			if err := r.Validate(); err != nil {
				t.Errorf("%s %q: %v", path, ok, err)
			}
		}
		for _, bad := range []string{"a-b", "1x", `x{a="b"}`, "x) or (y"} {
			r := DefaultRustFS()
			apply(&r, bad)
			err := r.Validate()
			if got := paths(err); !slices.Equal(got, []string{path}) || !strings.Contains(err.Error(), "is not a metric name") {
				t.Errorf("%s %q: %v", path, bad, err)
			}
		}
	}
}
