package pluginconf

import (
	"slices"
	"strings"
	"testing"
)

// The backup job rules moved here from internal/config with the section: at most one entry,
// each with both fields, reported at the same path with the same words.
func TestKubernetesBackupJobRules(t *testing.T) {
	job := BackupJob{Namespace: "backup", CronJob: "nightly"}
	for _, tc := range []struct {
		name string
		k    Kubernetes
		want []string
		msg  string
	}{
		{"nothing set is fine", Kubernetes{}, nil, ""},
		{"one complete job", Kubernetes{BackupJobs: []BackupJob{job}}, nil, ""},
		{"two jobs", Kubernetes{BackupJobs: []BackupJob{job, job}}, []string{"backupJobs"}, "has 2 entries; one job is supported until G3"},
		{"no cronjob", Kubernetes{BackupJobs: []BackupJob{{Namespace: "backup"}}}, []string{"backupJobs"}, "entry 1 needs both namespace and cronjob"},
		{"no namespace", Kubernetes{BackupJobs: []BackupJob{{CronJob: "nightly"}}}, []string{"backupJobs"}, "entry 1 needs both namespace and cronjob"},
		{"two incomplete jobs report each rule", Kubernetes{BackupJobs: []BackupJob{{}, {}}}, []string{"backupJobs", "backupJobs", "backupJobs"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.k.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("paths = %v, want %v (%v)", got, tc.want, err)
			}
			if tc.msg != "" && (err == nil || !strings.Contains(err.Error(), tc.msg)) {
				t.Errorf("error = %v, want %q", err, tc.msg)
			}
		})
	}
}
