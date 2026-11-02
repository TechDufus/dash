package plugintest

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/svc"
)

// KubePatch is one Patch call a KubeCluster received.
type KubePatch struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	Merge     []byte
}

// KubeCluster is a svc.Cluster for the tests of the code that consumes the kubernetes plugin's
// service (Flux, CNPG): the state a test sets, and a record of the Watch and Patch calls it
// made. Like the real cluster it serves, lists and holds only the resources that were passed to
// Watch; a watched resource is served and has been listed until a switch says otherwise. Its
// revisions move as the real cluster's do (see GVRRevision). It is safe for concurrent use; the
// zero value is not usable.
type KubeCluster struct {
	mu        sync.Mutex
	synced    bool
	objs      map[schema.GroupVersionResource][]*unstructured.Unstructured
	unserved  map[schema.GroupVersionResource]bool
	notListed map[schema.GroupVersionResource]bool
	refused   map[schema.GroupVersionResource]string
	revs      map[schema.GroupVersionResource]uint64
	failed    []schema.GroupVersion
	watched   [][]schema.GroupVersionResource
	registry  map[schema.GroupVersionResource]bool
	patches   []KubePatch
	patchErr  error
}

var _ svc.Cluster = (*KubeCluster)(nil)

// NewKubeCluster is a synced cluster that watches nothing and holds nothing; a resource passed to
// Watch is served and listed until a switch says otherwise.
func NewKubeCluster() *KubeCluster {
	return &KubeCluster{
		synced:    true,
		objs:      map[schema.GroupVersionResource][]*unstructured.Unstructured{},
		unserved:  map[schema.GroupVersionResource]bool{},
		notListed: map[schema.GroupVersionResource]bool{},
		refused:   map[schema.GroupVersionResource]string{},
		revs:      map[schema.GroupVersionResource]uint64{},
		registry:  map[schema.GroupVersionResource]bool{},
	}
}

// bump moves gvr's revision, as an informer event or a CRD appearing or going does. The caller holds mu.
func (k *KubeCluster) bump(gvr schema.GroupVersionResource) { k.revs[gvr]++ }

// SetObjects replaces the objects of gvr and bumps its revision.
func (k *KubeCluster) SetObjects(gvr schema.GroupVersionResource, objs ...*unstructured.Unstructured) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.objs[gvr] = slices.Clone(objs)
	k.bump(gvr)
}

// SetSynced sets what Synced reports.
func (k *KubeCluster) SetSynced(v bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.synced = v
}

// SetServed sets whether the API server serves gvr (Has); an unserved gvr still counts as Listed.
// It bumps gvr's revision, as a CRD appearing or going does.
func (k *KubeCluster) SetServed(gvr schema.GroupVersionResource, served bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if served {
		delete(k.unserved, gvr)
	} else {
		k.unserved[gvr] = true
	}
	k.bump(gvr)
}

// SetListed sets whether the first list of gvr has completed (default true). Like the real
// cluster, it moves no revision: a list that completes empty is not an informer event.
func (k *KubeCluster) SetListed(gvr schema.GroupVersionResource, listed bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if listed {
		delete(k.notListed, gvr)
	} else {
		k.notListed[gvr] = true
	}
}

// SetFailedGroups sets the group versions FailedGroups reports.
func (k *KubeCluster) SetFailedGroups(gvs ...schema.GroupVersion) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.failed = slices.Clone(gvs)
}

// SetRefused makes Refused report (category, true) for gvr. It moves no revision: a list becoming
// refused as time passes is not an informer event.
func (k *KubeCluster) SetRefused(gvr schema.GroupVersionResource, category string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.refused[gvr] = category
}

// ClearRefused makes Refused report false for gvr again. It moves no revision.
func (k *KubeCluster) ClearRefused(gvr schema.GroupVersionResource) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.refused, gvr)
}

// SetPatchError makes every later Patch return err (nil restores success); the call is recorded either way.
func (k *KubeCluster) SetPatchError(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.patchErr = err
}

// Watched returns the gvrs of every Watch call, one entry per call, oldest first.
func (k *KubeCluster) Watched() [][]schema.GroupVersionResource {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([][]schema.GroupVersionResource, len(k.watched))
	for i, w := range k.watched {
		out[i] = slices.Clone(w)
	}
	return out
}

// Patches returns every Patch call, oldest first.
func (k *KubeCluster) Patches() []KubePatch {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]KubePatch, len(k.patches))
	for i, p := range k.patches {
		p.Merge = slices.Clone(p.Merge)
		out[i] = p
	}
	return out
}

// Synced reports the SetSynced switch.
func (k *KubeCluster) Synced() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.synced
}

// Has reports that gvr is served: a watched gvr is until SetServed says otherwise, and any other
// is not, as with the real cluster.
func (k *KubeCluster) Has(gvr schema.GroupVersionResource) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.registry[gvr] && !k.unserved[gvr]
}

// Listed reports that gvr has been listed, or is not served and so never will be. A gvr that was
// never watched is not listed.
func (k *KubeCluster) Listed(gvr schema.GroupVersionResource) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.registry[gvr] && (k.unserved[gvr] || !k.notListed[gvr])
}

// FailedGroups returns a sorted copy of the SetFailedGroups groups.
func (k *KubeCluster) FailedGroups() []schema.GroupVersion {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := slices.Clone(k.failed)
	slices.SortFunc(out, func(a, b schema.GroupVersion) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Version, b.Version))
	})
	return out
}

// Refused reports the category SetRefused set for gvr.
func (k *KubeCluster) Refused(gvr schema.GroupVersionResource) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	category, ok := k.refused[gvr]
	return category, ok
}

// Watch records the call and registers gvrs; the cluster needs no informer, so the objects are
// whatever SetObjects set. Only registered resources are served, listed or returned by List.
func (k *KubeCluster) Watch(gvrs ...schema.GroupVersionResource) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.watched = append(k.watched, slices.Clone(gvrs))
	for _, gvr := range gvrs {
		k.registry[gvr] = true
	}
}

// List returns a copy of the objects of gvr; nil while gvr is not watched, not served or its
// first list has not completed.
func (k *KubeCluster) List(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.registry[gvr] || k.unserved[gvr] || k.notListed[gvr] {
		return nil
	}
	return slices.Clone(k.objs[gvr])
}

// GVRRevision sums the revisions of gvrs, or of every resource when none are given. Like the real
// cluster's, a revision moves on an object change (SetObjects) and on a CRD appearing or going
// (SetServed), never because a list completed or became refused.
func (k *KubeCluster) GVRRevision(gvrs ...schema.GroupVersionResource) uint64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	var sum uint64
	if len(gvrs) == 0 {
		for _, r := range k.revs {
			sum += r
		}
		return sum
	}
	for _, gvr := range gvrs {
		sum += k.revs[gvr]
	}
	return sum
}

// Patch records the call and returns the SetPatchError error.
func (k *KubeCluster) Patch(_ context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.patches = append(k.patches, KubePatch{GVR: gvr, Namespace: namespace, Name: name, Merge: slices.Clone(merge)})
	return k.patchErr
}
