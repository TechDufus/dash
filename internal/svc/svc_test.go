package svc_test

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/svc"
)

// kubeService is the shape of what the kubernetes plugin exports: one object that is the node
// index and the cluster. Synced is shared by the two interfaces and Revision belongs to the node
// index alone (the cluster's is GVRRevision), so the methods of both can sit on one type.
type kubeService struct{}

func (kubeService) K8sNode(string) (string, bool, bool)                           { return "", false, false }
func (kubeService) NodeIP(string) (string, bool)                                  { return "", false }
func (kubeService) Synced() bool                                                  { return true }
func (kubeService) Revision() uint64                                              { return 7 }
func (kubeService) Has(schema.GroupVersionResource) bool                          { return true }
func (kubeService) Listed(schema.GroupVersionResource) bool                       { return true }
func (kubeService) Watch(...schema.GroupVersionResource)                          {}
func (kubeService) List(schema.GroupVersionResource) []*unstructured.Unstructured { return nil }
func (kubeService) GVRRevision(...schema.GroupVersionResource) uint64             { return 9 }
func (kubeService) Patch(context.Context, schema.GroupVersionResource, string, string, []byte) error {
	return nil
}

// Compile-time: the two interfaces never clash on a method name or signature.
var _ interface {
	svc.Cluster
	svc.NodeIndex
} = kubeService{}

// A consumer holds the service as an any and asserts it to the interface it needs; both
// assertions succeed on the one value, and the two revisions stay apart.
func TestOneServiceIsClusterAndNodeIndex(t *testing.T) {
	var v any = kubeService{}
	c, ok := v.(svc.Cluster)
	if !ok {
		t.Fatal("not a svc.Cluster")
	}
	idx, ok := v.(svc.NodeIndex)
	if !ok {
		t.Fatal("not a svc.NodeIndex")
	}
	if c.GVRRevision() == idx.Revision() {
		t.Error("the cluster's and the node index's revisions are one counter")
	}
}
