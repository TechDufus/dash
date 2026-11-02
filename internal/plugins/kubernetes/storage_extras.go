package kubernetes

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// The PromQL of the transitional storage rows: the RustFS buckets and cluster capacity, and the
// age of the last CloudNativePG backup. They stay on this topic until the plugins that own
// those systems take them over.
const (
	queryBucketUsed    = `max by (bucket)(rustfs_bucket_usage_usage_bytes)`
	queryBucketQuota   = `max by (bucket)(rustfs_bucket_usage_quota_bytes)`
	queryRustFSUsed    = `max(rustfs_cluster_capacity_used_bytes)`
	queryRustFSUsable  = `max(rustfs_cluster_capacity_usable_total_bytes)`
	queryCNPGBackupAge = `time() - max by (namespace,cluster)(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp{role="primary"})`
)

// bucketsOf is the buckets either bucket query reported, by name. A bucket with a quota takes
// its health from how much of it is used; one without a quota is always ok.
func bucketsOf(s storageSnap) []model.Bucket {
	names := map[string]bool{}
	for b := range s.bucketUsed {
		names[b] = true
	}
	for b := range s.bucketQuota {
		names[b] = true
	}
	out := []model.Bucket{}
	for b := range names {
		bk := model.Bucket{Name: b, UsedBytes: int64(s.bucketUsed[b]), QuotaBytes: int64(s.bucketQuota[b]), Health: model.HealthOK}
		if bk.QuotaBytes > 0 {
			bk.Health = health.RatioHealth(float64(bk.UsedBytes) / float64(bk.QuotaBytes))
		}
		out = append(out, bk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// cnpgOf is the CloudNativePG clusters of the cache, sorted by namespace and name, with the age
// of their last backup when the metric has it. The cluster's own status wins over its spec for
// the instance count.
func cnpgOf(c *kube.Cluster, s storageSnap, links plugin.LinkResolver) []model.CNPGCluster {
	out := []model.CNPGCluster{}
	for _, u := range c.List(kube.GVRCNPGClusters) {
		cl := model.CNPGCluster{
			Namespace: u.GetNamespace(), Name: u.GetName(),
			Links: linksOf(links, "Cluster", u.GetNamespace(), u.GetName()),
		}
		if n, ok, _ := unstructured.NestedInt64(u.Object, "status", "instances"); ok {
			cl.Instances = int(n)
		} else if n, ok, _ := unstructured.NestedInt64(u.Object, "spec", "instances"); ok {
			cl.Instances = int(n)
		}
		if n, ok, _ := unstructured.NestedInt64(u.Object, "status", "readyInstances"); ok {
			cl.ReadyInstances = int(n)
		}
		cl.Phase, _, _ = unstructured.NestedString(u.Object, "status", "phase")
		if age, ok := s.cnpgAge[cl.Namespace+"/"+cl.Name]; ok {
			cl.LastBackupAgeSeconds = new(age)
		}
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out
}
