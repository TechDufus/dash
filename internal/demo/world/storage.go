package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Storage is the kubernetes.storage payload: PVCs, the NAS backup age and etcd. The buckets and the
// RustFS capacity are in cnpg.go and rustfs.go.
func (w *World) Storage() model.StorageData { return w.storage(w.clock()) }

func (w *World) storage(now time.Time) model.StorageData {
	type pvcSpec struct {
		ns, name, sc string
		capGiB, used float64
		growth       float64
	}
	var d model.StorageData
	for _, p := range []pvcSpec{
		{"monitoring", "vmsingle-data", "local-zfs", 50, 0.83, 0.6},
		{"monitoring", "grafana", "local-zfs", 5, 0.21, 0.01},
		{"registry", "zot-data", "local-zfs", 100, 0.46, 0.8},
		{"acme", "acme-db-1", "local-zfs", 20, 0.38, 0.05},
		{"acme", "acme-db-2", "local-zfs", 20, 0.38, 0.05},
		{"dash", "dash-cache", "local-zfs", 1, 0.09, 0},
		{"gatus", "gatus-data", "local-zfs", 2, 0.27, 0.01},
	} {
		capB := gib(p.capGiB)
		ratio := Around(p.name, now, p.used, 0.004)
		used := int64(float64(capB) * ratio)
		h := model.HealthOK
		if ratio >= 0.8 {
			h = model.HealthWarn
		}
		d.PVCs = append(d.PVCs, model.PVC{
			Namespace: p.ns, Name: p.name, StorageClass: p.sc, Phase: "Bound", CapacityBytes: capB,
			UsedBytes: new(used), UsedRatio: new(ratio), GrowthBytesPerDay: new(p.growth * (1 << 30)),
			Health: h, Links: w.links.Headlamp("PersistentVolumeClaim", p.ns, p.name),
		})
	}
	d.BackupJobs = []model.BackupJob{{Name: "nas-backup", LastSuccessAgeSeconds: new(7.5 * 3600)}}
	d.Etcd = model.Etcd{DBBytes: 92 << 20, QuotaBytes: 2 << 30}
	return d
}
