package kubernetes

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
	"github.com/TechDufus/dash/internal/svc"
)

// The PromQL of the volume and etcd figures on the storage topic. The etcd queries are
// completed with the configured job label (etcdQuery).
const (
	queryPVCUsed     = `max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_used_bytes)`
	queryPVCCapacity = `max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_capacity_bytes)`
	queryPVCGrowth   = `deriv(max by (namespace,persistentvolumeclaim)(kubelet_volume_stats_used_bytes)[7d:15m]) * 86400`

	queryEtcdDBFmt    = `max(etcd_mvcc_db_total_size_in_bytes{job=%s})`
	queryEtcdQuotaFmt = `max(etcd_server_quota_backend_bytes{job=%s})`
)

// storageSnap is the last good values of the storage queries. The maps are replaced as a whole
// when their query answers and never written afterwards, so a copy of the struct may be read
// while the next poll builds its successor.
type storageSnap struct {
	pvcUsed, pvcCap, pvcGrowth map[string]float64 // "<ns>/<pvc>"
	bucketUsed, bucketQuota    map[string]float64 // by bucket
	rustfsUsed, rustfsUsable   float64
	cnpgAge                    map[string]float64 // "<ns>/<cluster>"
	etcdDB, etcdQuota          float64
	// backups are the rows of each configured backup job, by backupKey.
	backups map[string][]backupRow
}

// queryStorage runs the storage queries and returns the snapshot: the values of a query that
// failed are those of prev. The error is the first failure of the batch, nil when every query
// answered. No backup query is sent while no backup job is configured.
func queryStorage(ctx context.Context, q svc.Querier, cfg pluginconf.Kubernetes, prev storageSnap) (storageSnap, error) {
	etcdJob := strconv.Quote(cfg.EtcdJob())
	qs := map[string]string{
		"pvcUsed": queryPVCUsed, "pvcCap": queryPVCCapacity, "pvcGrowth": queryPVCGrowth,
		"bucketUsed": queryBucketUsed, "bucketQuota": queryBucketQuota,
		"rustfsUsed": queryRustFSUsed, "rustfsUsable": queryRustFSUsable,
		"cnpg":   queryCNPGBackupAge,
		"etcdDB": fmt.Sprintf(queryEtcdDBFmt, etcdJob), "etcdQuota": fmt.Sprintf(queryEtcdQuotaFmt, etcdJob),
	}
	for i, j := range cfg.BackupJobs {
		qs[backupQueryName(i)] = backupAgeQuery(j.Namespace, j.CronJob)
	}
	res := queryAll(ctx, q, qs)

	snap := prev
	ok := func(name string) bool { return res[name].err == nil }
	if ok("pvcUsed") {
		snap.pvcUsed = byLabels(res["pvcUsed"].samples, "namespace", "persistentvolumeclaim")
	}
	if ok("pvcCap") {
		snap.pvcCap = byLabels(res["pvcCap"].samples, "namespace", "persistentvolumeclaim")
	}
	if ok("pvcGrowth") {
		snap.pvcGrowth = byLabels(res["pvcGrowth"].samples, "namespace", "persistentvolumeclaim")
	}
	if ok("bucketUsed") {
		snap.bucketUsed = byLabel(res["bucketUsed"].samples, "bucket")
	}
	if ok("bucketQuota") {
		snap.bucketQuota = byLabel(res["bucketQuota"].samples, "bucket")
	}
	if ok("rustfsUsed") {
		snap.rustfsUsed, _ = first(res["rustfsUsed"].samples)
	}
	if ok("rustfsUsable") {
		snap.rustfsUsable, _ = first(res["rustfsUsable"].samples)
	}
	if ok("cnpg") {
		snap.cnpgAge = byLabels(res["cnpg"].samples, "namespace", "cluster")
	}
	if ok("etcdDB") {
		snap.etcdDB, _ = first(res["etcdDB"].samples)
	}
	if ok("etcdQuota") {
		snap.etcdQuota, _ = first(res["etcdQuota"].samples)
	}
	snap.backups = make(map[string][]backupRow, len(cfg.BackupJobs))
	for i, j := range cfg.BackupJobs {
		key := backupKey(j)
		if r := res[backupQueryName(i)]; r.err == nil {
			snap.backups[key] = backupRows(j, r.samples)
		} else if old, had := prev.backups[key]; had {
			snap.backups[key] = old
		}
	}
	return snap, firstErr(res)
}

// storageInput is everything buildStorage reads.
type storageInput struct {
	c     *kube.Cluster
	snap  storageSnap
	cfg   pluginconf.Kubernetes
	links plugin.LinkResolver
	now   time.Time
}

// buildStorage is the kubernetes.storage payload: the volumes of the cluster with their usage,
// etcd, the backup jobs, and the object-store and database figures of the transitional rows.
func buildStorage(in storageInput) model.StorageData {
	s := in.snap
	out := model.StorageData{
		PVCs:       []model.PVC{},
		Buckets:    bucketsOf(s),
		CNPG:       cnpgOf(in.c, s, in.links),
		BackupJobs: backupJobsOf(in.cfg, s),
		RustFS:     model.UsedSize{UsedBytes: int64(s.rustfsUsed), UsableBytes: int64(s.rustfsUsable)},
		Etcd:       model.Etcd{DBBytes: int64(s.etcdDB), QuotaBytes: int64(s.etcdQuota)},
	}
	for _, p := range in.c.ListPVCs() {
		key := p.Namespace + "/" + p.Name
		pvc := model.PVC{
			Ref:       plugin.Ref(ID, "PVC", p.Namespace, p.Name),
			Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Health: model.HealthOK,
			Links: linksOf(in.links, "PersistentVolumeClaim", p.Namespace, p.Name),
		}
		if p.Spec.StorageClassName != nil {
			pvc.StorageClass = *p.Spec.StorageClassName
		}
		if q, ok := p.Status.Capacity["storage"]; ok {
			pvc.CapacityBytes = q.Value()
		} else if v, ok := s.pvcCap[key]; ok {
			pvc.CapacityBytes = int64(v)
		}
		if used, ok := s.pvcUsed[key]; ok {
			pvc.UsedBytes = new(int64(used))
			capacity := float64(pvc.CapacityBytes)
			if c, ok := s.pvcCap[key]; ok && c > 0 {
				capacity = c
			}
			if capacity > 0 {
				pvc.UsedRatio = new(used / capacity)
				pvc.Health = health.RatioHealth(*pvc.UsedRatio)
			}
		}
		if g, ok := s.pvcGrowth[key]; ok {
			pvc.GrowthBytesPerDay = new(g)
		}
		out.PVCs = append(out.PVCs, pvc)
	}
	sort.Slice(out.PVCs, func(i, j int) bool {
		return out.PVCs[i].Namespace+"/"+out.PVCs[i].Name < out.PVCs[j].Namespace+"/"+out.PVCs[j].Name
	})
	return out
}
