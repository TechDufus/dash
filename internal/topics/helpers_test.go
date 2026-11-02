package topics

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
)

// eventually polls cond until it holds or five seconds pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// latest is the decoded envelope of topic, or ok=false.
func latest(h *hub.Hub, topic model.Topic) (model.Envelope, bool) {
	ev, ok := h.Latest("", topic)
	if !ok {
		return model.Envelope{}, false
	}
	var env model.Envelope
	if json.Unmarshal(ev.Data, &env) != nil {
		return model.Envelope{}, false
	}
	return env, true
}

// fakeKube is the svc.Cluster the kubernetes plugin exports, with the state a test sets: whether
// the typed informers have synced, the objects of each resource, which resources have not
// completed their first list, and the revision the resources move.
type fakeKube struct {
	mu      sync.Mutex
	synced  bool
	objs    map[schema.GroupVersionResource][]*unstructured.Unstructured
	pending map[schema.GroupVersionResource]bool // not listed yet
	absent  map[schema.GroupVersionResource]bool // not served: counts as listed
	rev     uint64
	failed  []schema.GroupVersion           // groups whose discovery failed
	watched [][]schema.GroupVersionResource // the gvrs of every Watch call
	patches []string
}

// newFakeKube is a synced cluster that has listed everything and holds nothing.
func newFakeKube() *fakeKube {
	return &fakeKube{
		synced:  true,
		objs:    map[schema.GroupVersionResource][]*unstructured.Unstructured{},
		pending: map[schema.GroupVersionResource]bool{},
		absent:  map[schema.GroupVersionResource]bool{},
	}
}

func (f *fakeKube) Synced() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.synced
}

func (f *fakeKube) Has(gvr schema.GroupVersionResource) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.absent[gvr]
}

func (f *fakeKube) Listed(gvr schema.GroupVersionResource) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.absent[gvr] || !f.pending[gvr]
}

func (f *fakeKube) FailedGroups() []schema.GroupVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.failed)
}

// Refused: the Manager's flux code gates on Listed alone, so the fake refuses nothing.
func (f *fakeKube) Refused(schema.GroupVersionResource) (string, bool) { return "", false }

func (f *fakeKube) Watch(gvrs ...schema.GroupVersionResource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watched = append(f.watched, slices.Clone(gvrs))
}

func (f *fakeKube) List(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.absent[gvr] || f.pending[gvr] {
		return nil
	}
	return slices.Clone(f.objs[gvr])
}

func (f *fakeKube) GVRRevision(...schema.GroupVersionResource) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev
}

func (f *fakeKube) Patch(_ context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, gvr.Resource+"/"+namespace+"/"+name+" "+string(merge))
	return nil
}

var _ svc.Cluster = (*fakeKube)(nil)

// set changes the fake under its lock.
func (f *fakeKube) set(fn func(*fakeKube)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// add puts obj into gvr and moves the revision, as an informer event does.
func (f *fakeKube) add(gvr schema.GroupVersionResource, obj *unstructured.Unstructured) {
	f.set(func(f *fakeKube) {
		f.objs[gvr] = append(f.objs[gvr], obj)
		f.rev++
	})
}

func (f *fakeKube) watchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.watched)
}

// kubeHolder is what the supervisor does with the kubernetes service: it hands out whichever one
// runs now, or none, and a restart swaps it for another value.
type kubeHolder struct{ cur atomic.Pointer[fakeKube] }

func holding(f *fakeKube) *kubeHolder {
	h := &kubeHolder{}
	h.cur.Store(f)
	return h
}

func (h *kubeHolder) get() (svc.Cluster, bool) {
	if f := h.cur.Load(); f != nil {
		return f, true
	}
	return nil, false
}

// kubeOf is the accessor of a cluster that never goes away.
func kubeOf(f *fakeKube) func() (svc.Cluster, bool) { return holding(f).get }

// fluxObject is a Flux object of gvr with the Ready condition at status.
func fluxObject(kind, namespace, name, ready string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"kind":       kind,
		"apiVersion": "v1",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
		"status": map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": ready, "message": "msg"},
		}},
	}}
	return u
}

// kustomization is a Kustomization named name in flux-system.
func kustomization(name string, ready bool) *unstructured.Unstructured {
	s := "False"
	if ready {
		s = "True"
	}
	return fluxObject("Kustomization", "flux-system", name, s)
}

// testManager is a Manager without the kubernetes service (none runs) over the given hosts.
func testManager(cfg config.Config, hosts plugin.HostProvider) *Manager {
	return testManagerWith(cfg, hosts, nil)
}

// testManagerWith is testManager with plugins that contribute to the overview.
func testManagerWith(cfg config.Config, hosts plugin.HostProvider, contributors func() []plugin.Contributor) *Manager {
	return New(Deps{Cfg: cfg, Hosts: hosts, Contributors: contributors})
}

// legacyTopics are the topics the Manager builds.
var legacyTopics = []model.Topic{model.TopicFlux}

// schemaGVR keeps the table of resources in tests short.
type schemaGVR = schema.GroupVersionResource
