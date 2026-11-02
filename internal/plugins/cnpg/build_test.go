package cnpg

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/svc"
)

type fakeLinks struct{}

func (fakeLinks) Resolve(req plugin.LinkRequest) []model.Link {
	return []model.Link{{Label: req.Kind + ":" + req.Namespace + "/" + req.Name, Href: "https://headlamp.example.com/"}}
}

// The moved parity test of the kubernetes plugin's storage extras: sorted by namespace and name,
// the cluster's status wins over its spec for the instance count, the age only where the metric
// has it, and links of kind Cluster.
func TestClusters(t *testing.T) {
	list := []*unstructured.Unstructured{
		cnpgCluster("db", "pg-main", map[string]any{"instances": int64(3), "readyInstances": int64(2), "phase": "Cluster in healthy state"}, nil),
		cnpgCluster("apps", "pg-spec", nil, map[string]any{"instances": int64(2)}),
		cnpgCluster("apps", "pg-both", map[string]any{"instances": int64(1)}, map[string]any{"instances": int64(5)}),
	}
	got := clustersOf(list, map[string]float64{"db/pg-main": 3600, "other/ghost": 1}, fakeLinks{})
	if len(got) != 3 {
		t.Fatalf("clusters = %+v", got)
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
	plugintest.AssertRefs(t, nil, ID, got, model.CNPGCluster{})
	if main.Ref != "cnpg:Cluster/db/pg-main" {
		t.Errorf("ref = %q", main.Ref)
	}
}

// Nothing in the cache is an empty list with no nil slice, and a nil resolver gives empty links.
func TestNoClustersIsAnEmptyList(t *testing.T) {
	if got := clustersOf(nil, nil, nil); got == nil || len(got) != 0 {
		t.Errorf("clusters = %#v", got)
	}
	one := clustersOf([]*unstructured.Unstructured{cnpgCluster("a", "b", nil, nil)}, nil, nil)
	if one[0].Links == nil || len(one[0].Links) != 0 {
		t.Errorf("links = %#v, want an empty non-nil list", one[0].Links)
	}
}

// The age answers by namespace and cluster labels.
func TestAgesAreIndexedByNamespaceAndCluster(t *testing.T) {
	got := byLabels([]svc.Sample{sample(90, "namespace", "db", "cluster", "pg-main")}, "namespace", "cluster")
	if got["db/pg-main"] != 90 || len(got) != 1 {
		t.Errorf("ages = %v", got)
	}
}
