package kubernetes

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// The custom resources the plugin reads.
var allCRDs = []schema.GroupVersionResource{
	kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters, kube.GVRIngresses,
}

// crdKinds is the kind each GVR's objects carry.
var crdKinds = map[schema.GroupVersionResource]string{
	kube.GVRCertificates: "Certificate", kube.GVRExternalSecrets: "ExternalSecret", kube.GVRHTTPRoutes: "HTTPRoute",
	kube.GVRCNPGClusters: "Cluster", kube.GVRIngresses: "Ingress",
	kube.GVRKustomizations: "Kustomization", kube.GVRHelmReleases: "HelmRelease",
	kube.GVRGitRepositories: "GitRepository", kube.GVROCIRepositories: "OCIRepository", kube.GVRHelmRepositories: "HelmRepository",
}

// obj is an unstructured custom resource; fields are set through the returned object.
func obj(gvr schema.GroupVersionResource, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion(gvr.GroupVersion().String())
	u.SetKind(crdKinds[gvr])
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// served is the discovery answer for the given resources.
func served(gvrs ...schema.GroupVersionResource) []*metav1.APIResourceList {
	var out []*metav1.APIResourceList
	for _, g := range gvrs {
		out = append(out, &metav1.APIResourceList{
			GroupVersion: g.GroupVersion().String(),
			APIResources: []metav1.APIResource{{Name: g.Resource}, {Name: g.Resource + "/status"}},
		})
	}
	return out
}

// testCluster is a kube.Cluster over fakes, started, with the typed informers synced and every
// custom resource in crds served and listed. objs are typed objects (pods, nodes, deployments,
// PVCs, namespaces, ...) and unstructured custom resources (made with obj); crds is every CRD of
// allCRDs but ingresses when it is nil.
func testCluster(t *testing.T, crds []schema.GroupVersionResource, objs ...k8sruntime.Object) *kube.Cluster {
	t.Helper()
	if crds == nil {
		crds = []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters}
	}
	var typed, custom []k8sruntime.Object
	for _, o := range objs {
		if _, ok := o.(*unstructured.Unstructured); ok {
			custom = append(custom, o)
		} else {
			typed = append(typed, o)
		}
	}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, g := range allCRDs {
		listKinds[g] = g.Resource + "List"
	}
	kc := kubefake.NewClientset(typed...)
	kc.Discovery().(*fakediscovery.FakeDiscovery).Resources = served(crds...)
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(k8sruntime.NewScheme(), listKinds, custom...)
	c := kube.NewFromClients(kc, dc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx)
	wanted := map[schema.GroupVersionResource]kube.Kind{}
	for _, g := range allCRDs {
		wanted[g] = kube.KindOther
	}
	if err := c.Discover(wanted); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	wctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if !c.WaitForSync(wctx) {
		t.Fatal("the typed informers did not sync")
	}
	waitFor(t, "the custom resources to be listed", func() bool {
		for _, g := range allCRDs {
			if !c.Listed(g) {
				return false
			}
		}
		return true
	})
	return c
}

// waitFor polls cond until it holds, or fails.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Typed object constructors for fixtures.

func node(name, ip string, ready bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.34.1"},
		},
	}
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func controllerOf(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, Controller: new(true)}}
}

// runningPod is a Running pod with one ready container.
func runningPod(ns, name, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))},
		Spec:       corev1.PodSpec{NodeName: nodeName, Containers: []corev1.Container{{Name: "app", Image: "example.com/app:1"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
}

func deployment(ns, name string, desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: &desired,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/app:1"}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func statefulSet(ns, name string, desired, ready int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &desired,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example.com/app:1"}}}},
		},
		Status: appsv1.StatefulSetStatus{ReadyReplicas: ready},
	}
}

func cronJob(ns, name string) *batchv1.CronJob {
	return &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

func pvc(ns, name string, capacityBytes int64) *corev1.PersistentVolumeClaim {
	q := *resource.NewQuantity(capacityBytes, resource.BinarySI)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{"storage": q}},
	}
}

// fakeProm is a svc.Querier whose answers a test sets before the plugin starts.
type fakeProm struct {
	mu      sync.Mutex
	samples map[string][]svc.Sample // by exact expression
	errs    map[string]error        // by exact expression
	calls   []string
}

func (f *fakeProm) Instant(_ context.Context, expr string) ([]svc.Sample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, expr)
	if err := f.errs[expr]; err != nil {
		return nil, err
	}
	return f.samples[expr], nil
}

func (f *fakeProm) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]model.SeriesLine, error) {
	return nil, nil
}

// requests is how many queries the fake has answered.
func (f *fakeProm) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func sample(value float64, labels ...string) svc.Sample {
	m := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		m[labels[i]] = labels[i+1]
	}
	return svc.Sample{Labels: m, Value: value}
}

// fakeGatus is a svc.EndpointHealth.
type fakeGatus struct {
	mu    sync.Mutex
	hosts map[string]model.Health
	rev   uint64
	srcs  []model.SourceStatus
	// reads counts the calls of Revision: the publisher makes one on each of its rounds, so a
	// test can wait for rounds to have happened.
	reads atomic.Int64
}

func (f *fakeGatus) ByHost(h string) (model.Health, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.hosts[h]
	return v, ok
}
func (f *fakeGatus) ByName(string) (bool, bool) { return false, false }
func (f *fakeGatus) Revision() uint64 {
	f.reads.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev
}
func (f *fakeGatus) SourceStatuses() []model.SourceStatus { return f.srcs }
func (f *fakeGatus) set(host string, h model.Health) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hosts == nil {
		f.hosts = map[string]model.Health{}
	}
	f.hosts[host] = h
	f.rev++
}
