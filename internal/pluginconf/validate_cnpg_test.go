package pluginconf

import (
	"slices"
	"strings"
	"testing"
)

func TestCNPGDefaultsAreValid(t *testing.T) {
	d := DefaultCNPG()
	if err := d.Validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if d.BackupMetric != "barman_cloud_cloudnative_pg_io_last_available_backup_timestamp" {
		t.Errorf("default backup metric = %q", d.BackupMetric)
	}
	if d.Enabled != nil {
		t.Errorf("enabled = %v: cnpg switches itself on", *d.Enabled)
	}
}

// An empty metric means the default, so a section that never went through the defaults still
// queries the Barman series.
func TestCNPGBackupMetricNameFallsBackToTheDefault(t *testing.T) {
	if got := (CNPG{}).BackupMetricName(); got != CNPGBackupMetric {
		t.Errorf("empty = %q", got)
	}
	if got := (CNPG{BackupMetric: "my_backup_ts"}).BackupMetricName(); got != "my_backup_ts" {
		t.Errorf("set = %q", got)
	}
}

// The metric is put into a PromQL expression: only a metric name may pass.
func TestCNPGBackupMetricIsValidatedAgainstInjection(t *testing.T) {
	for _, ok := range []string{"", "my_backup_ts", "a:b_c", "_x"} {
		if err := (CNPG{BackupMetric: ok}).Validate(); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"a-b", "1abc", "has space", `x{role="replica"}`, "a)or(b", "x or vector(1)"} {
		err := (CNPG{BackupMetric: bad}).Validate()
		if got := paths(err); !slices.Equal(got, []string{"backupMetric"}) || !strings.Contains(err.Error(), "is not a metric name") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
