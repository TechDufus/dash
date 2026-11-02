package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Storage is the storage payload: PVCs, object-store buckets, Proxmox pools, a Postgres cluster,
// the NAS backup age and etcd.
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
	d.Buckets = []model.Bucket{
		{Name: "cnpg-backups", UsedBytes: gib(38), QuotaBytes: gib(200), Health: model.HealthOK},
		{Name: "velero", UsedBytes: gib(112), QuotaBytes: gib(500), Health: model.HealthOK},
		{Name: "media-cache", UsedBytes: gib(9), Health: model.HealthOK},
	}
	d.RustFS = model.UsedSize{UsedBytes: gib(159), UsableBytes: gib(1800)}
	for _, p := range w.PVEStorages() { // still part of the storage payload until the proxmox plugin publishes them
		d.PVEStorages = append(d.PVEStorages, model.PVEStorage{Name: p.Name, UsedBytes: p.UsedBytes, SizeBytes: p.SizeBytes})
	}
	d.CNPG = []model.CNPGCluster{{Namespace: "acme", Name: "acme-db", Instances: 2, ReadyInstances: 2,
		Phase: "Cluster in healthy state", LastBackupAgeSeconds: new(Around("cnpgbk", now, 5.2*3600, 1800)),
		Links: w.links.Headlamp("Cluster", "acme", "acme-db")}}
	d.NASBackups = []model.NASBackup{{Name: "nas-backup", LastSuccessAgeSeconds: new(7.5 * 3600)}}
	d.Etcd = model.Etcd{DBBytes: 92 << 20, QuotaBytes: 2 << 30}
	return d
}
