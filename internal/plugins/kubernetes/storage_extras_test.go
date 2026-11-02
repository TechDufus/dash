package kubernetes

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

func TestBucketsComeFromEitherQueryAndTakeTheirHealthFromTheQuota(t *testing.T) {
	snap := storageSnap{
		bucketUsed:  map[string]float64{"backups": 95, "scratch": 5, "media": 10},
		bucketQuota: map[string]float64{"backups": 100, "media": 100, "empty": 50},
	}
	got := buildStorage(storageInput{c: testCluster(t, nil), snap: snap}).Buckets
	want := []model.Bucket{
		{Name: "backups", UsedBytes: 95, QuotaBytes: 100, Health: model.HealthCrit},
		{Name: "empty", QuotaBytes: 50, Health: model.HealthOK},
		{Name: "media", UsedBytes: 10, QuotaBytes: 100, Health: model.HealthOK},
		{Name: "scratch", UsedBytes: 5, Health: model.HealthOK},
	}
	if len(got) != len(want) {
		t.Fatalf("buckets = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestBucketWarnsAtEightyPercent(t *testing.T) {
	snap := storageSnap{bucketUsed: map[string]float64{"b": 80}, bucketQuota: map[string]float64{"b": 100}}
	if got := buildStorage(storageInput{c: testCluster(t, nil), snap: snap}).Buckets[0].Health; got != model.HealthWarn {
		t.Errorf("80%% = %q", got)
	}
}

func TestRustFSAndBucketQueriesFillTheRows(t *testing.T) {
	prom := storageProm(map[string][]svc.Sample{
		queryBucketUsed:   {sample(40, "bucket", "backups")},
		queryBucketQuota:  {sample(100, "bucket", "backups")},
		queryRustFSUsed:   {sample(3e12)},
		queryRustFSUsable: {sample(8e12)},
	})
	snap, err := queryStorage(context.Background(), prom, pluginconf.Kubernetes{}, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	got := buildStorage(storageInput{c: testCluster(t, nil), snap: snap})
	if got.RustFS != (model.UsedSize{UsedBytes: 3e12, UsableBytes: 8e12}) {
		t.Errorf("rustfs = %+v", got.RustFS)
	}
	if len(got.Buckets) != 1 || got.Buckets[0] != (model.Bucket{Name: "backups", UsedBytes: 40, QuotaBytes: 100, Health: model.HealthOK}) {
		t.Errorf("buckets = %+v", got.Buckets)
	}
}

// storageCluster is a CloudNativePG cluster object; the fields are set under their paths.
func storageCluster(ns, name string, fields map[string]any) *unstructured.Unstructured {
	u := obj(kube.GVRCNPGClusters, ns, name)
	for path, v := range fields {
		switch x := v.(type) {
		case string:
			_ = unstructured.SetNestedField(u.Object, x, "status", path)
		case int64:
			section, key := "status", path
			if len(path) > 5 && path[:5] == "spec." {
				section, key = "spec", path[5:]
			}
			_ = unstructured.SetNestedField(u.Object, x, section, key)
		}
	}
	return u
}

func TestCNPGClusters(t *testing.T) {
	c := testCluster(t, nil,
		storageCluster("db", "pg-main", map[string]any{"instances": int64(3), "readyInstances": int64(2), "phase": "Cluster in healthy state"}),
		storageCluster("apps", "pg-spec", map[string]any{"spec.instances": int64(2)}),
		storageCluster("apps", "pg-both", map[string]any{"instances": int64(1), "spec.instances": int64(5)}),
	)
	snap := storageSnap{cnpgAge: map[string]float64{"db/pg-main": 3600, "other/ghost": 1}}
	got := buildStorage(storageInput{c: c, snap: snap, links: storageLinks{}}).CNPG
	if len(got) != 3 {
		t.Fatalf("cnpg = %+v", got)
	}
	both, spec, main := got[0], got[1], got[2]
	if both.Name != "pg-both" || spec.Name != "pg-spec" || main.Name != "pg-main" {
		t.Fatalf("not sorted by namespace/name: %s %s %s", both.Name, spec.Name, main.Name)
	}
	if both.Instances != 1 {
		t.Errorf("the status instances win over the spec: %d", both.Instances)
	}
	if spec.Instances != 2 || spec.ReadyInstances != 0 || spec.LastBackupAgeSeconds != nil {
		t.Errorf("spec = %+v", spec)
	}
	if main.Instances != 3 || main.ReadyInstances != 2 || main.Phase != "Cluster in healthy state" {
		t.Errorf("main = %+v", main)
	}
	if main.LastBackupAgeSeconds == nil || *main.LastBackupAgeSeconds != 3600 {
		t.Errorf("backup age = %v", main.LastBackupAgeSeconds)
	}
	if len(main.Links) != 1 || main.Links[0].Label != "Cluster:db/pg-main" {
		t.Errorf("links = %+v", main.Links)
	}
}

func TestCNPGBackupAgeQueryFillsTheRow(t *testing.T) {
	prom := storageProm(map[string][]svc.Sample{
		queryCNPGBackupAge: {sample(90, "namespace", "db", "cluster", "pg-main")},
	})
	snap, err := queryStorage(context.Background(), prom, pluginconf.Kubernetes{}, storageSnap{})
	if err != nil {
		t.Fatal(err)
	}
	c := testCluster(t, nil, storageCluster("db", "pg-main", nil))
	got := buildStorage(storageInput{c: c, snap: snap}).CNPG
	if len(got) != 1 || got[0].LastBackupAgeSeconds == nil || *got[0].LastBackupAgeSeconds != 90 {
		t.Errorf("cnpg = %+v", got)
	}
}
