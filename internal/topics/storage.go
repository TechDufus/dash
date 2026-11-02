package topics

import (
	"context"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/upstream"
)

type storageSnap struct {
	pvcUsed, pvcCap, pvcGrowth map[string]float64 // "<ns>/<pvc>"
	bucketUsed, bucketQuota    map[string]float64
	rustfsUsed, rustfsUsable   float64
	nasAge                     map[string]float64
	cnpgAge                    map[string]float64 // "<ns>/<cluster>"
	etcdDB, etcdQuota          float64
}

func (m *Manager) pollStorage(ctx context.Context) {
	qs := map[string]string{
		"pvcUsed": queries.PVCUsed, "pvcCap": queries.PVCCapacity, "pvcGrowth": queries.PVCGrowth,
		"bucketUsed": queries.BucketUsed, "bucketQuota": queries.BucketQuota,
		"rustfsUsed": queries.RustFSUsed, "rustfsUsable": queries.RustFSUsable,
		"cnpg":   queries.CNPGBackupAge,
		"etcdDB": queries.EtcdDB, "etcdQuota": queries.EtcdQuota,
	}
	if c := m.d.Cfg.Conventions; c.NASBackupNamespace != "" && c.NASBackupCronJob != "" { // no job configured: no NAS backup rows
		qs["nas"] = queries.NASBackupAge(c.NASBackupNamespace, c.NASBackupCronJob)
	}
	res := m.queryAll(ctx, qs)
	m.setSource("storage:victoriametrics", firstErr(res))

	m.mu.Lock()
	defer m.mu.Unlock()
	s := &m.storage
	set := func(key string, fn func([]upstream.Sample)) {
		if res[key].err == nil {
			fn(res[key].samples)
		}
	}
	set("pvcUsed", func(x []upstream.Sample) { s.pvcUsed = byLabels(x, "namespace", "persistentvolumeclaim") })
	set("pvcCap", func(x []upstream.Sample) { s.pvcCap = byLabels(x, "namespace", "persistentvolumeclaim") })
	set("pvcGrowth", func(x []upstream.Sample) { s.pvcGrowth = byLabels(x, "namespace", "persistentvolumeclaim") })
	set("bucketUsed", func(x []upstream.Sample) { s.bucketUsed = byLabel(x, "bucket") })
	set("bucketQuota", func(x []upstream.Sample) { s.bucketQuota = byLabel(x, "bucket") })
	set("rustfsUsed", func(x []upstream.Sample) { s.rustfsUsed, _ = first(x) })
	set("rustfsUsable", func(x []upstream.Sample) { s.rustfsUsable, _ = first(x) })
	set("nas", func(x []upstream.Sample) { s.nasAge = byLabel(x, "cronjob") })
	set("cnpg", func(x []upstream.Sample) { s.cnpgAge = byLabels(x, "namespace", "cluster") })
	set("etcdDB", func(x []upstream.Sample) { s.etcdDB, _ = first(x) })
	set("etcdQuota", func(x []upstream.Sample) { s.etcdQuota, _ = first(x) })
	m.mark(model.TopicStorage)
}

func (m *Manager) buildStorage(now time.Time) model.StorageData {
	_ = now
	m.mu.RLock()
	s := m.storage
	m.mu.RUnlock()
	out := model.StorageData{
		PVCs: []model.PVC{}, Buckets: []model.Bucket{},
		CNPG: []model.CNPGCluster{}, NASBackups: []model.NASBackup{},
		RustFS: model.UsedSize{UsedBytes: int64(s.rustfsUsed), UsableBytes: int64(s.rustfsUsable)},
		Etcd:   model.Etcd{DBBytes: int64(s.etcdDB), QuotaBytes: int64(s.etcdQuota)},
	}

	for _, p := range m.d.Cluster.ListPVCs() {
		key := p.Namespace + "/" + p.Name
		pvc := model.PVC{
			Namespace: p.Namespace, Name: p.Name, Phase: string(p.Status.Phase), Health: model.HealthOK,
			Links: m.link("PersistentVolumeClaim", p.Namespace, p.Name),
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
				pvc.Health = RatioHealth(*pvc.UsedRatio)
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

	names := map[string]bool{}
	for b := range s.bucketUsed {
		names[b] = true
	}
	for b := range s.bucketQuota {
		names[b] = true
	}
	for b := range names {
		bk := model.Bucket{Name: b, UsedBytes: int64(s.bucketUsed[b]), QuotaBytes: int64(s.bucketQuota[b]), Health: model.HealthOK}
		if bk.QuotaBytes > 0 {
			bk.Health = RatioHealth(float64(bk.UsedBytes) / float64(bk.QuotaBytes))
		}
		out.Buckets = append(out.Buckets, bk)
	}
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Name < out.Buckets[j].Name })

	for _, u := range m.d.Cluster.List(kube.GVRCNPGClusters) {
		c := model.CNPGCluster{Namespace: u.GetNamespace(), Name: u.GetName(),
			Links: m.link("Cluster", u.GetNamespace(), u.GetName())}
		if n, ok, _ := unstructured.NestedInt64(u.Object, "status", "instances"); ok {
			c.Instances = int(n)
		} else if n, ok, _ := unstructured.NestedInt64(u.Object, "spec", "instances"); ok {
			c.Instances = int(n)
		}
		if n, ok, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances"); ok {
			c.ReadyInstances = int(n)
		}
		c.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
		if age, ok := s.cnpgAge[c.Namespace+"/"+c.Name]; ok {
			c.LastBackupAgeSeconds = new(age)
		}
		out.CNPG = append(out.CNPG, c)
	}
	sort.Slice(out.CNPG, func(i, j int) bool {
		return out.CNPG[i].Namespace+"/"+out.CNPG[i].Name < out.CNPG[j].Namespace+"/"+out.CNPG[j].Name
	})

	for name, age := range s.nasAge {
		out.NASBackups = append(out.NASBackups, model.NASBackup{Name: name, LastSuccessAgeSeconds: new(age)})
	}
	sort.Slice(out.NASBackups, func(i, j int) bool { return out.NASBackups[i].Name < out.NASBackups[j].Name })
	return out
}
