package cnpg

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// clustersOf is the CloudNativePG clusters of the cache, sorted by namespace and name, with the
// age of their last backup when the metric has it. The cluster's own status wins over its spec
// for the instance count. The list is never nil.
func clustersOf(list []*unstructured.Unstructured, ages map[string]float64, links plugin.LinkResolver) []model.CNPGCluster {
	out := []model.CNPGCluster{}
	for _, u := range list {
		cl := model.CNPGCluster{
			Ref:       plugin.Ref(ID, "Cluster", u.GetNamespace(), u.GetName()),
			Namespace: u.GetNamespace(), Name: u.GetName(),
			Links: linksOf(links, u.GetNamespace(), u.GetName()),
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
		if age, ok := ages[cl.Namespace+"/"+cl.Name]; ok {
			cl.LastBackupAgeSeconds = new(age)
		}
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Name < out[j].Namespace+"/"+out[j].Name
	})
	return out
}

// linksOf is the links of one cluster into the tools whose plugins run: never nil.
func linksOf(l plugin.LinkResolver, namespace, name string) []model.Link {
	if l == nil {
		return []model.Link{}
	}
	return l.Resolve(plugin.LinkRequest{Kind: "Cluster", Namespace: namespace, Name: name})
}
