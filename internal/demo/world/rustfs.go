package world

import "github.com/TechDufus/dash/internal/model"

// RustFS is the rustfs.usage payload: three buckets, two with a quota, and the capacity of the
// store. The buckets carry no ref; the twin that publishes them adds it.
func (w *World) RustFS() model.RustFSUsageData {
	return model.RustFSUsageData{
		Buckets: []model.Bucket{
			{Name: "cnpg-backups", UsedBytes: gib(38), QuotaBytes: gib(200), Health: model.HealthOK},
			{Name: "velero", UsedBytes: gib(112), QuotaBytes: gib(500), Health: model.HealthOK},
			{Name: "media-cache", UsedBytes: gib(9), Health: model.HealthOK},
		},
		Capacity: model.UsedSize{UsedBytes: gib(159), UsableBytes: gib(1800)},
	}
}
