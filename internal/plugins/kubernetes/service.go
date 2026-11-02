package kubernetes

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/svc"
)

// service is what the plugin exports: the node index Proxmox reads and the cluster caches the
// Manager's flux code reads. One object implements both, which is why svc.Cluster's revision is
// GVRRevision: NodeIndex.Revision has another signature.
type service struct{ r *runtime }

// One object is both the node index and the cluster: a consumer asks Supervisor.Service for one
// value and type-asserts it to either. A change that made the two disagree (a clash of Synced or
// Revision) fails here, at build time, not in a consumer.
var _ interface {
	svc.Cluster
	svc.NodeIndex
} = (*service)(nil)

// svc.NodeIndex.

func (s *service) K8sNode(vmName string) (string, bool, bool) { return s.r.k8sNode(vmName) }
func (s *service) NodeIP(node string) (string, bool)          { return s.r.nodeAddress(node) }
func (s *service) Synced() bool                               { return s.r.cl.HasSynced() }

// Revision changes when the set of nodes or a node's state changes: every node event bumps it,
// a superset of readiness changes (a consumer that republishes on a change it cannot see is
// merely early). It never repeats for the life of the process.
func (s *service) Revision() uint64 { return s.r.nodeRev.Load() }

// svc.Cluster (Synced is shared with the node index).

func (s *service) Has(gvr schema.GroupVersionResource) bool    { return s.r.cl.Has(gvr) }
func (s *service) Listed(gvr schema.GroupVersionResource) bool { return s.r.cl.Listed(gvr) }
func (s *service) FailedGroups() []schema.GroupVersion         { return s.r.cl.FailedGroups() }
func (s *service) Watch(gvrs ...schema.GroupVersionResource)   { s.r.cl.Watch(gvrs...) }
func (s *service) List(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	return s.r.cl.List(gvr)
}
func (s *service) GVRRevision(gvrs ...schema.GroupVersionResource) uint64 {
	return s.r.cl.Revision(gvrs...)
}
func (s *service) Patch(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error {
	return s.r.cl.Patch(ctx, gvr, namespace, name, merge)
}
