package rustfs

import (
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

func TestBucketsComeFromEitherQueryAndTakeTheirHealthFromTheQuota(t *testing.T) {
	snap := snapshot{
		bucketUsed:  map[string]float64{"backups": 95, "scratch": 5, "media": 10},
		bucketQuota: map[string]float64{"backups": 100, "media": 100, "empty": 50},
	}
	got := bucketsOf(snap)
	want := []model.Bucket{
		{Ref: "rustfs:Bucket/backups", Name: "backups", UsedBytes: 95, QuotaBytes: 100, Health: model.HealthCrit},
		{Ref: "rustfs:Bucket/empty", Name: "empty", QuotaBytes: 50, Health: model.HealthOK},
		{Ref: "rustfs:Bucket/media", Name: "media", UsedBytes: 10, QuotaBytes: 100, Health: model.HealthOK},
		{Ref: "rustfs:Bucket/scratch", Name: "scratch", UsedBytes: 5, Health: model.HealthOK},
	}
	if len(got) != len(want) {
		t.Fatalf("buckets = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bucket %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	plugintest.AssertRefs(t, nil, ID, got, model.Bucket{})
}

func TestBucketHealthBoundaries(t *testing.T) {
	for _, tc := range []struct {
		used float64
		want model.Health
	}{{79, model.HealthOK}, {80, model.HealthWarn}, {89, model.HealthWarn}, {90, model.HealthCrit}, {120, model.HealthCrit}} {
		snap := snapshot{bucketUsed: map[string]float64{"b": tc.used}, bucketQuota: map[string]float64{"b": 100}}
		if got := bucketsOf(snap)[0].Health; got != tc.want {
			t.Errorf("%.0f%% = %q, want %q", tc.used, got, tc.want)
		}
	}
}

// A store with nothing in it is an empty list, never null.
func TestEmptyUsageHasNoNilSlices(t *testing.T) {
	got := usageOf(snapshot{})
	if got.Buckets == nil || len(got.Buckets) != 0 {
		t.Errorf("buckets = %#v", got.Buckets)
	}
	if got.Capacity != (model.UsedSize{}) {
		t.Errorf("capacity = %+v", got.Capacity)
	}
}

func TestCapacityComesFromTheTwoQueries(t *testing.T) {
	q := newFakeQuerier()
	q.fill()
	res := queryAll(t.Context(), q, newExprs(pluginconf.DefaultRustFS().Metrics))
	got := usageOf(applyPoll(snapshot{}, res))
	if got.Capacity != (model.UsedSize{UsedBytes: 3e12, UsableBytes: 8e12}) {
		t.Errorf("capacity = %+v", got.Capacity)
	}
	if len(got.Buckets) != 3 || got.Buckets[0] != (model.Bucket{Ref: "rustfs:Bucket/backups", Name: "backups", UsedBytes: 95, QuotaBytes: 100, Health: model.HealthCrit}) {
		t.Errorf("buckets = %+v", got.Buckets)
	}
}
