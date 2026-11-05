package kubernetes

import (
	"context"
	"errors"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// backupsOf runs the storage queries against prom and builds the backup rows.
func backupsOf(t *testing.T, prom *fakeProm, cfg pluginconf.Kubernetes, prev storageSnap) (model.StorageData, storageSnap, error) {
	t.Helper()
	snap, err := queryStorage(context.Background(), prom, cfg, prev)
	return buildStorage(storageInput{c: testCluster(t, nil), snap: snap, cfg: cfg}), snap, err
}

func backupAnswer(ns, cronjob string, samples ...svc.Sample) map[string][]svc.Sample {
	return map[string][]svc.Sample{backupAgeQuery(ns, cronjob): samples}
}

func TestBackupJobLabelNamesTheRow(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nightly-backup", Label: "NAS"}}}
	prom := storageProm(backupAnswer("backup", "nightly-backup", sample(7200, "cronjob", "nightly-backup")))
	got, _, err := backupsOf(t, prom, cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 1 {
		t.Fatalf("rows = %+v", got.BackupJobs)
	}
	b := got.BackupJobs[0]
	if b.Name != "NAS" || b.Ref != "kubernetes:BackupJob/backup/NAS" || b.LastSuccessAgeSeconds == nil || *b.LastSuccessAgeSeconds != 7200 {
		t.Errorf("row = %+v", b)
	}
	plugintest.AssertRefs(t, nil, ID, got, model.BackupJob{})
}

func TestTwoBackupJobsGiveTwoRows(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{
		{Namespace: "backup", CronJob: "nas", Label: "nas"},
		{Namespace: "db", CronJob: "dump", Label: "dump"},
	}}
	answers := backupAnswer("backup", "nas", sample(60, "cronjob", "nas"))
	answers[backupAgeQuery("db", "dump")] = []svc.Sample{sample(120, "cronjob", "dump")}
	got, _, err := backupsOf(t, storageProm(answers), cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 2 {
		t.Fatalf("rows = %+v", got.BackupJobs)
	}
	if a, b := got.BackupJobs[0], got.BackupJobs[1]; a.Ref != "kubernetes:BackupJob/backup/nas" || *a.LastSuccessAgeSeconds != 60 ||
		b.Ref != "kubernetes:BackupJob/db/dump" || *b.LastSuccessAgeSeconds != 120 {
		t.Errorf("rows = %+v %+v", a, b)
	}
	plugintest.AssertRefs(t, nil, ID, got, model.BackupJob{})
}

func TestUnlabelledBackupJobIsOneRowPerMatchedCronJob(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nas"}}}
	prom := storageProm(backupAnswer("backup", "nas",
		sample(300, "cronjob", "nas-weekly"), sample(100, "cronjob", "nas"), sample(5, "cronjob", "")))
	got, _, err := backupsOf(t, prom, cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 2 {
		t.Fatalf("rows = %+v", got.BackupJobs)
	}
	if a, b := got.BackupJobs[0], got.BackupJobs[1]; a.Name != "nas" || *a.LastSuccessAgeSeconds != 100 ||
		b.Name != "nas-weekly" || b.Ref != "kubernetes:BackupJob/backup/nas-weekly" || *b.LastSuccessAgeSeconds != 300 {
		t.Errorf("rows = %+v %+v", a, b)
	}
	plugintest.AssertRefs(t, nil, ID, got, model.BackupJob{})
}

func TestLabelledBackupJobTakesTheFreshestSuccess(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nas", Label: "nas"}}}
	prom := storageProm(backupAnswer("backup", "nas",
		sample(900, "cronjob", "nas-a"), sample(40, "cronjob", "nas-b"), sample(500, "cronjob", "nas")))
	got, _, err := backupsOf(t, prom, cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 1 || *got.BackupJobs[0].LastSuccessAgeSeconds != 40 {
		t.Errorf("rows = %+v", got.BackupJobs)
	}
}

func TestBackupJobWithNoMatchedSeriesHasNoRow(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{
		{Namespace: "backup", CronJob: "a", Label: "labelled"}, {Namespace: "backup", CronJob: "b"},
	}}
	got, _, err := backupsOf(t, &fakeProm{}, cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupJobs == nil || len(got.BackupJobs) != 0 {
		t.Errorf("rows = %#v, want an empty, non-nil list", got.BackupJobs)
	}
}

// A job whose query failed keeps the rows of the last poll; the others update.
func TestFailedBackupQueryKeepsOldRows(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{
		{Namespace: "backup", CronJob: "nas", Label: "nas"},
		{Namespace: "db", CronJob: "dump"},
	}}
	first := backupAnswer("backup", "nas", sample(60, "cronjob", "nas"))
	first[backupAgeQuery("db", "dump")] = []svc.Sample{sample(30, "cronjob", "dump")}
	_, snap, err := backupsOf(t, storageProm(first), cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	second := backupAnswer("backup", "nas", sample(180, "cronjob", "nas"))
	prom := storageProm(second)
	prom.errs[backupAgeQuery("db", "dump")] = boom
	got, snap2, err := backupsOf(t, prom, cfg, snap)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if len(got.BackupJobs) != 2 {
		t.Fatalf("rows = %+v", got.BackupJobs)
	}
	if a, b := got.BackupJobs[0], got.BackupJobs[1]; a.Name != "nas" || *a.LastSuccessAgeSeconds != 180 ||
		b.Name != "dump" || *b.LastSuccessAgeSeconds != 30 {
		t.Errorf("rows = %+v %+v", a, b)
	}
	if len(snap.backups) != 2 || len(snap2.backups) != 2 {
		t.Errorf("snapshots = %+v / %+v", snap.backups, snap2.backups)
	}
}

// A successful but empty answer is not a failure: the job's rows go away.
func TestEmptyBackupAnswerDropsOldRows(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nas", Label: "nas"}}}
	_, snap, _ := backupsOf(t, storageProm(backupAnswer("backup", "nas", sample(60, "cronjob", "nas"))), cfg, storageSnap{})
	got, _, err := backupsOf(t, &fakeProm{}, cfg, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 0 {
		t.Errorf("rows = %+v", got.BackupJobs)
	}
}

// Rows of a job that is no longer configured are not shown, whatever the snapshot holds.
func TestBackupRowsFollowTheConfiguredJobs(t *testing.T) {
	old := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nas", Label: "nas"}}}
	_, snap, _ := backupsOf(t, storageProm(backupAnswer("backup", "nas", sample(60, "cronjob", "nas"))), old, storageSnap{})
	got, snap2, err := backupsOf(t, &fakeProm{}, pluginconf.Kubernetes{}, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 0 || len(snap2.backups) != 0 {
		t.Errorf("rows = %+v, snapshot %+v", got.BackupJobs, snap2.backups)
	}
}

// Two jobs can report the same row (the second job's CronJob is a suffix match of the first's);
// the payload keeps one row per ref, the freshest.
func TestOverlappingBackupJobsShareOneRow(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{
		{Namespace: "backup", CronJob: "nas"}, {Namespace: "backup", CronJob: "nas-weekly"},
	}}
	answers := backupAnswer("backup", "nas", sample(300, "cronjob", "nas-weekly"))
	answers[backupAgeQuery("backup", "nas-weekly")] = []svc.Sample{sample(200, "cronjob", "nas-weekly")}
	got, _, err := backupsOf(t, storageProm(answers), cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.BackupJobs) != 1 || *got.BackupJobs[0].LastSuccessAgeSeconds != 200 {
		t.Errorf("rows = %+v", got.BackupJobs)
	}
	plugintest.AssertRefs(t, nil, ID, got, model.BackupJob{})
}

func TestBackupRowsAreSortedByRef(t *testing.T) {
	cfg := pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "j"}}}
	prom := storageProm(backupAnswer("backup", "j",
		sample(1, "cronjob", "j-c"), sample(2, "cronjob", "j-a"), sample(3, "cronjob", "j-b")))
	got, _, err := backupsOf(t, prom, cfg, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range got.BackupJobs {
		names = append(names, b.Name)
	}
	if len(names) != 3 || names[0] != "j-a" || names[1] != "j-b" || names[2] != "j-c" {
		t.Errorf("order = %v", names)
	}
}
