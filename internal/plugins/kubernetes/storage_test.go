package kubernetes

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// storageProm is a fake Prometheus with the given answers by expression.
func storageProm(answers map[string][]svc.Sample) *fakeProm {
	return &fakeProm{samples: answers, errs: map[string]error{}}
}

// storageCalls is the expressions the fake has been asked, in the order of arrival.
func storageCalls(f *fakeProm) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// storageLinks is a LinkResolver that answers one link per object, named after it.
type storageLinks struct{}

func (storageLinks) Resolve(r plugin.LinkRequest) []model.Link {
	return []model.Link{{Label: r.Kind + ":" + r.Namespace + "/" + r.Name}}
}

// storagePVC is a PVC with a status capacity, a class and a phase.
func storagePVC(ns, name string, capacity int64, class string) *corev1.PersistentVolumeClaim {
	p := pvc(ns, name, capacity)
	p.Spec.StorageClassName = &class
	return p
}

func TestEtcdJobIsInTheQuery(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  pluginconf.Kubernetes
		job  string
	}{
		{"default", pluginconf.Kubernetes{}, "etcd"},
		{"defaults from the config", pluginconf.DefaultKubernetes(), "etcd"},
		{"configured", pluginconf.Kubernetes{Etcd: pluginconf.KubernetesEtcd{Job: "kube-etcd"}}, "kube-etcd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prom := &fakeProm{}
			if _, err := queryStorage(context.Background(), prom, tc.cfg, storageSnap{}); err != nil {
				t.Fatal(err)
			}
			want := `job="` + tc.job + `"`
			var hits int
			for _, c := range storageCalls(prom) {
				if strings.Contains(c, "etcd_") {
					hits++
					if !strings.Contains(c, want) {
						t.Errorf("etcd query %q lacks %s", c, want)
					}
				}
			}
			if hits != 2 {
				t.Errorf("%d etcd queries, want 2 (db size and quota)", hits)
			}
		})
	}
}

func TestEtcdQueriesAreTheOldOnes(t *testing.T) {
	prom := &fakeProm{}
	if _, err := queryStorage(context.Background(), prom, pluginconf.Kubernetes{}, storageSnap{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`max(etcd_mvcc_db_total_size_in_bytes{job="etcd"})`,
		`max(etcd_server_quota_backend_bytes{job="etcd"})`,
	} {
		if !slices.Contains(storageCalls(prom), want) {
			t.Errorf("%s was not queried; got %q", want, storageCalls(prom))
		}
	}
}

// The NAS-backup query needs its convention: with no backup job configured the series is not
// requested; with one, it is.
func TestNASQueryNeedsItsConvention(t *testing.T) {
	run := func(cfg pluginconf.Kubernetes) []string {
		prom := &fakeProm{}
		if _, err := queryStorage(context.Background(), prom, cfg, storageSnap{}); err != nil {
			t.Fatal(err)
		}
		return storageCalls(prom)
	}
	got := strings.Join(run(pluginconf.Kubernetes{}), "\n")
	if !strings.Contains(got, "kubelet_volume_stats") {
		t.Fatalf("Prometheus was not queried at all:\n%s", got)
	}
	if strings.Contains(got, "kube_cronjob_status_last_successful_time") {
		t.Errorf("no backup job set, yet the backup age was queried:\n%s", got)
	}

	got = strings.Join(run(pluginconf.Kubernetes{BackupJobs: []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nas-backup"}}}), "\n")
	want := `time() - kube_cronjob_status_last_successful_time{namespace="backup",cronjob=~"nas-backup(-.+)?"}`
	if !strings.Contains(got, want) {
		t.Errorf("a backup job set, yet %q was not queried:\n%s", want, got)
	}
}

func TestBackupQueryEscapesTheCronJobName(t *testing.T) {
	got := backupAgeQuery("backup", "nas.backup+1")
	want := `time() - kube_cronjob_status_last_successful_time{namespace="backup",cronjob=~"nas\\.backup\\+1(-.+)?"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestPVCRows(t *testing.T) {
	c := testCluster(t, nil,
		storagePVC("media", "plex", 100, "fast"),
		storagePVC("apps", "db", 1000, "slow"),
	)
	snap := storageSnap{
		pvcUsed:   map[string]float64{"media/plex": 95, "apps/db": 250},
		pvcCap:    map[string]float64{"media/plex": 100, "apps/db": 1000},
		pvcGrowth: map[string]float64{"apps/db": -12.5},
	}
	got := buildStorage(storageInput{c: c, snap: snap, links: storageLinks{}})
	if len(got.PVCs) != 2 {
		t.Fatalf("PVCs = %+v", got.PVCs)
	}
	db, plex := got.PVCs[0], got.PVCs[1]
	if db.Name != "db" || plex.Name != "plex" {
		t.Fatalf("not sorted by namespace/name: %s, %s", db.Name, plex.Name)
	}
	if db.Ref != "kubernetes:PVC/apps/db" || plex.Ref != "kubernetes:PVC/media/plex" {
		t.Errorf("refs = %q, %q", db.Ref, plex.Ref)
	}
	if db.StorageClass != "slow" || db.Phase != "Bound" || db.CapacityBytes != 1000 {
		t.Errorf("db = %+v", db)
	}
	if db.UsedBytes == nil || *db.UsedBytes != 250 || db.UsedRatio == nil || *db.UsedRatio != 0.25 || db.Health != model.HealthOK {
		t.Errorf("db usage = %+v", db)
	}
	if db.GrowthBytesPerDay == nil || *db.GrowthBytesPerDay != -12.5 {
		t.Errorf("db growth = %v", db.GrowthBytesPerDay)
	}
	if plex.GrowthBytesPerDay != nil {
		t.Errorf("plex has no growth sample, got %v", *plex.GrowthBytesPerDay)
	}
	if plex.Health != model.HealthCrit {
		t.Errorf("plex at 95%% = %q", plex.Health)
	}
	if len(db.Links) != 1 || db.Links[0].Label != "PersistentVolumeClaim:apps/db" {
		t.Errorf("links = %+v", db.Links)
	}
	plugintest.AssertRefs(t, nil, ID, got, model.PVC{})
}

func TestPVCRatioBoundaries(t *testing.T) {
	for _, tc := range []struct {
		used int
		want model.Health
	}{
		{79, model.HealthOK}, {80, model.HealthWarn}, {89, model.HealthWarn}, {90, model.HealthCrit}, {100, model.HealthCrit},
	} {
		c := testCluster(t, nil, pvc("n", "p", 100))
		snap := storageSnap{pvcUsed: map[string]float64{"n/p": float64(tc.used)}, pvcCap: map[string]float64{"n/p": 100}}
		got := buildStorage(storageInput{c: c, snap: snap})
		if got.PVCs[0].Health != tc.want {
			t.Errorf("%d%% used = %q, want %q", tc.used, got.PVCs[0].Health, tc.want)
		}
	}
}

// A PVC the cluster reports without a capacity takes it from the metric; without a metric the
// ratio is unknown and the volume stays ok.
func TestPVCCapacityFallsBackToTheMetric(t *testing.T) {
	bare := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "bare"}}
	lone := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "n", Name: "lone"}}
	c := testCluster(t, nil, bare, lone)
	snap := storageSnap{
		pvcUsed: map[string]float64{"n/bare": 900, "n/lone": 900},
		pvcCap:  map[string]float64{"n/bare": 1000},
	}
	got := buildStorage(storageInput{c: c, snap: snap})
	if len(got.PVCs) != 2 {
		t.Fatalf("PVCs = %+v", got.PVCs)
	}
	b, l := got.PVCs[0], got.PVCs[1]
	if b.CapacityBytes != 1000 || b.UsedRatio == nil || *b.UsedRatio != 0.9 || b.Health != model.HealthCrit {
		t.Errorf("bare = %+v", b)
	}
	if l.CapacityBytes != 0 || l.UsedRatio != nil || l.Health != model.HealthOK || l.UsedBytes == nil || *l.UsedBytes != 900 {
		t.Errorf("lone = %+v", l)
	}
}

// The metric's capacity, when present, is what the ratio divides by (it is what the volume
// reports; the claim's request may be larger).
func TestPVCRatioUsesTheMetricCapacity(t *testing.T) {
	c := testCluster(t, nil, pvc("n", "p", 1000))
	snap := storageSnap{pvcUsed: map[string]float64{"n/p": 50}, pvcCap: map[string]float64{"n/p": 100}}
	got := buildStorage(storageInput{c: c, snap: snap})
	if p := got.PVCs[0]; p.CapacityBytes != 1000 || *p.UsedRatio != 0.5 {
		t.Errorf("pvc = %+v ratio %v", p, *p.UsedRatio)
	}
}

func TestPVCWithoutMetricsHasNoUsage(t *testing.T) {
	c := testCluster(t, nil, pvc("n", "p", 100))
	got := buildStorage(storageInput{c: c})
	p := got.PVCs[0]
	if p.UsedBytes != nil || p.UsedRatio != nil || p.GrowthBytesPerDay != nil || p.Health != model.HealthOK {
		t.Errorf("pvc = %+v", p)
	}
}

func TestEmptyStoragePayloadHasNoNilSlices(t *testing.T) {
	got := buildStorage(storageInput{c: testCluster(t, nil)})
	if got.PVCs == nil || got.Buckets == nil || got.CNPG == nil || got.BackupJobs == nil {
		t.Errorf("a nil slice in %+v", got)
	}
	if len(got.PVCs)+len(got.Buckets)+len(got.CNPG)+len(got.BackupJobs) != 0 {
		t.Errorf("rows from nothing: %+v", got)
	}
}

func TestStorageQueriesFillTheSnapshot(t *testing.T) {
	prom := storageProm(map[string][]svc.Sample{
		queryPVCUsed:     {sample(30, "namespace", "n", "persistentvolumeclaim", "p")},
		queryPVCCapacity: {sample(100, "namespace", "n", "persistentvolumeclaim", "p")},
		queryPVCGrowth:   {sample(4, "namespace", "n", "persistentvolumeclaim", "p")},
		`max(etcd_mvcc_db_total_size_in_bytes{job="etcd"})`: {sample(2e9)},
		`max(etcd_server_quota_backend_bytes{job="etcd"})`:  {sample(8e9)},
	})
	snap, err := queryStorage(context.Background(), prom, pluginconf.Kubernetes{}, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	c := testCluster(t, nil, pvc("n", "p", 100))
	got := buildStorage(storageInput{c: c, snap: snap})
	if got.Etcd != (model.Etcd{DBBytes: 2e9, QuotaBytes: 8e9}) {
		t.Errorf("etcd = %+v", got.Etcd)
	}
	p := got.PVCs[0]
	if *p.UsedBytes != 30 || *p.UsedRatio != 0.3 || *p.GrowthBytesPerDay != 4 {
		t.Errorf("pvc = %+v", p)
	}
}

// A query that fails leaves its old value, the others take the new, and the error is returned.
func TestFailedQueryKeepsPrevValues(t *testing.T) {
	boom := errors.New("boom")
	prom := storageProm(map[string][]svc.Sample{
		queryPVCCapacity: {sample(200, "namespace", "n", "persistentvolumeclaim", "p")},
		`max(etcd_server_quota_backend_bytes{job="etcd"})`: {sample(9e9)},
	})
	prom.errs[queryPVCUsed] = boom
	prom.errs[`max(etcd_mvcc_db_total_size_in_bytes{job="etcd"})`] = boom
	prev := storageSnap{
		pvcUsed: map[string]float64{"n/p": 50},
		pvcCap:  map[string]float64{"n/p": 100},
		etcdDB:  1e9, etcdQuota: 4e9,
	}
	snap, err := queryStorage(context.Background(), prom, pluginconf.Kubernetes{}, prev)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the failing query's error", err)
	}
	if snap.pvcUsed["n/p"] != 50 || snap.etcdDB != 1e9 {
		t.Errorf("failed queries lost their old values: %+v", snap)
	}
	if snap.pvcCap["n/p"] != 200 || snap.etcdQuota != 9e9 {
		t.Errorf("answered queries did not update: %+v", snap)
	}
	if prev.pvcCap["n/p"] != 100 {
		t.Error("prev was written to")
	}
}

func TestAnsweredQueryWithNoSamplesClearsItsValues(t *testing.T) {
	prev := storageSnap{pvcUsed: map[string]float64{"n/p": 50}, etcdDB: 1e9, rustfsUsed: 5}
	snap, err := queryStorage(context.Background(), &fakeProm{}, pluginconf.Kubernetes{}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.pvcUsed) != 0 || snap.etcdDB != 0 || snap.rustfsUsed != 0 {
		t.Errorf("an empty answer must replace the old values: %+v", snap)
	}
}
