package rustfs

import (
	"sort"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// usageOf is the rustfs.usage payload of a snapshot.
func usageOf(s snapshot) model.RustFSUsageData {
	return model.RustFSUsageData{
		Buckets:  bucketsOf(s),
		Capacity: model.UsedSize{UsedBytes: int64(s.capUsed), UsableBytes: int64(s.capUsable)},
	}
}

// bucketsOf is the buckets either bucket query reported, by name. A bucket with a quota takes
// its health from how much of it is used; one without a quota is always ok. The list is never
// nil.
func bucketsOf(s snapshot) []model.Bucket {
	names := map[string]bool{}
	for b := range s.bucketUsed {
		names[b] = true
	}
	for b := range s.bucketQuota {
		names[b] = true
	}
	out := []model.Bucket{}
	for b := range names {
		bk := model.Bucket{
			Ref:  plugin.Ref(ID, "Bucket", b),
			Name: b, UsedBytes: int64(s.bucketUsed[b]), QuotaBytes: int64(s.bucketQuota[b]), Health: model.HealthOK,
		}
		if bk.QuotaBytes > 0 {
			bk.Health = health.RatioHealth(float64(bk.UsedBytes) / float64(bk.QuotaBytes))
		}
		out = append(out, bk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
