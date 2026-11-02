package kube

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// listKinds is what the fake dynamic client needs to answer a list of each resource.
func listKinds(gvrs ...schema.GroupVersionResource) map[schema.GroupVersionResource]string {
	out := map[schema.GroupVersionResource]string{}
	for _, g := range gvrs {
		out[g] = g.Resource + "List"
	}
	return out
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

func object(gvr schema.GroupVersionResource, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(gvr.GroupVersion().String())
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

// rig is a Cluster over fakes whose discovery the test controls.
type rig struct {
	c    *Cluster
	kube *kubefake.Clientset
	dyn  *dynfake.FakeDynamicClient
	disc *fakeDisc
}

// fakeDisc is a discovery client that answers what the test says.
type fakeDisc struct {
	discovery.ServerResourcesInterface
	mu    sync.Mutex
	lists []*metav1.APIResourceList
	err   error
	calls int
}

func (d *fakeDisc) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	return nil, d.lists, d.err
}

func (d *fakeDisc) set(err error, lists ...*metav1.APIResourceList) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lists, d.err = lists, err
}

func newRig(t *testing.T, kinds []schema.GroupVersionResource, objs ...runtime.Object) *rig {
	t.Helper()
	r := &rig{kube: kubefake.NewClientset(), disc: &fakeDisc{}}
	r.dyn = dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(kinds...), objs...)
	r.c = NewFromClients(r.kube, r.dyn)
	r.c.SetDiscovery(r.disc)
	return r
}

func (r *rig) start(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.c.Start(ctx)
	return ctx
}

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

// The informer wedge of the old design: one unserved CRD kept HasSynced false for ever. With the
// discovery gate the typed informers sync while a 404 reactor answers for the CRD, because no
// informer is created for a resource discovery does not list.
func TestMissingOptionalCRDDoesNotBlockSync(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRKustomizations, GVRCertificates})
	notFound := func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(GVRKustomizations.GroupResource(), "")
	}
	r.dyn.PrependReactor("list", "kustomizations", notFound)
	r.dyn.PrependWatchReactor("kustomizations", func(clienttesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewNotFound(GVRKustomizations.GroupResource(), "")
	})
	r.disc.set(nil, served(GVRCertificates)...) // the cluster serves no Flux
	ctx := r.start(t)
	if err := r.c.Discover(map[schema.GroupVersionResource]Kind{GVRKustomizations: KindFlux, GVRCertificates: KindCertificates}); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	syncCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !r.c.WaitForSync(syncCtx) || !r.c.HasSynced() {
		t.Fatal("the typed informers did not sync")
	}
	if r.c.Has(GVRKustomizations) || !r.c.Listed(GVRKustomizations) {
		t.Errorf("the unserved CRD: Has = %v, Listed = %v, want false and true", r.c.Has(GVRKustomizations), r.c.Listed(GVRKustomizations))
	}
	if got := r.c.List(GVRKustomizations); got != nil {
		t.Errorf("List of an unserved CRD = %v, want nil", got)
	}
}

func TestListedIsTrueForAnAbsentGVR(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCertificates, GVRHTTPRoutes}, object(GVRCertificates, "Certificate", "app", "web-tls"))
	r.disc.set(nil, served(GVRCertificates)...)
	r.start(t)

	if r.c.Listed(GVRCertificates) || r.c.Listed(GVRHTTPRoutes) {
		t.Fatal("Listed is true for an unknown, unwatched resource")
	}
	r.c.AddDynamic(GVRCertificates, KindCertificates)
	r.c.AddDynamic(GVRHTTPRoutes, KindHTTPRoutes)
	if r.c.Listed(GVRCertificates) || r.c.Listed(GVRHTTPRoutes) {
		t.Fatal("Listed is true before the first discovery")
	}
	if err := r.c.Discover(nil); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if !r.c.Listed(GVRHTTPRoutes) {
		t.Error("an absent resource is not listed")
	}
	waitFor(t, "the certificates to be listed", func() bool { return r.c.Listed(GVRCertificates) })
	if got := r.c.List(GVRCertificates); len(got) != 1 || got[0].GetName() != "web-tls" {
		t.Errorf("List = %v", got)
	}
	if !r.c.Has(GVRCertificates) || r.c.Has(GVRHTTPRoutes) {
		t.Errorf("Has: certificates %v, routes %v", r.c.Has(GVRCertificates), r.c.Has(GVRHTTPRoutes))
	}
}

// A CRD that is installed after Start gets its informer when the next discovery sees it, and the
// appearance fires one KindCRDs change and moves the revision.
func TestAddDynamicAfterStartFiresOnChange(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCNPGClusters}, object(GVRCNPGClusters, "Cluster", "db", "main"))
	var crds atomic.Int32
	r.c.OnChange(func(k Kind) {
		if k == KindCRDs {
			crds.Add(1)
		}
	})
	r.disc.set(nil) // nothing is served
	r.start(t)
	r.c.AddDynamic(GVRCNPGClusters, KindCNPG)
	select {
	case <-r.c.DiscoveryRequests():
	default:
		t.Fatal("a new resource did not request a discovery")
	}
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	if r.c.Has(GVRCNPGClusters) || !r.c.Listed(GVRCNPGClusters) || crds.Load() != 0 {
		t.Fatalf("before the CRD exists: Has %v, Listed %v, changes %d", r.c.Has(GVRCNPGClusters), r.c.Listed(GVRCNPGClusters), crds.Load())
	}
	rev := r.c.Revision(GVRCNPGClusters)

	r.disc.set(nil, served(GVRCNPGClusters)...)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	if !r.c.Has(GVRCNPGClusters) {
		t.Fatal("the new CRD is not present")
	}
	waitFor(t, "the informer to list", func() bool { return r.c.Listed(GVRCNPGClusters) && len(r.c.List(GVRCNPGClusters)) == 1 })
	if crds.Load() != 1 {
		t.Errorf("KindCRDs fired %d times, want once", crds.Load())
	}
	if r.c.Revision(GVRCNPGClusters) <= rev {
		t.Errorf("revision %d did not move past %d", r.c.Revision(GVRCNPGClusters), rev)
	}
	if err := r.c.Discover(nil); err != nil || crds.Load() != 1 {
		t.Errorf("a discovery that changes nothing: err %v, KindCRDs %d", err, crds.Load())
	}
}

func TestWatchUsesTheKindOfAKnownResource(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRKustomizations}, object(GVRKustomizations, "Kustomization", "flux-system", "apps"))
	var got atomic.Value
	r.c.OnChange(func(k Kind) {
		if k != KindCRDs {
			got.Store(k)
		}
	})
	r.disc.set(nil, served(GVRKustomizations)...)
	r.start(t)
	r.c.Watch(GVRKustomizations)
	r.c.Watch(GVRKustomizations) // idempotent
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first list", func() bool { return r.c.Listed(GVRKustomizations) && got.Load() != nil })
	if got.Load() != KindFlux {
		t.Errorf("kind = %v, want flux", got.Load())
	}
	if len(r.c.dynamics) != 1 {
		t.Errorf("Watch twice registered %d resources", len(r.c.dynamics))
	}
}

// A failure of one group applies the healthy groups, and the failed group is neither present nor
// absent: a resource that was present stays present.
func TestPartialDiscoveryFailureKeepsHealthyGroups(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCertificates, GVRExternalSecrets, GVRHTTPRoutes},
		object(GVRCertificates, "Certificate", "a", "c"), object(GVRExternalSecrets, "ExternalSecret", "a", "s"))
	wanted := map[schema.GroupVersionResource]Kind{GVRCertificates: KindCertificates, GVRExternalSecrets: KindExtSecrets, GVRHTTPRoutes: KindHTTPRoutes}
	r.disc.set(nil, served(GVRCertificates, GVRExternalSecrets)...)
	r.start(t)
	if err := r.c.Discover(wanted); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both to list", func() bool { return r.c.Listed(GVRCertificates) && r.c.Listed(GVRExternalSecrets) })

	// Now the external-secrets group fails, and the gateway group appears.
	failing := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{GVRExternalSecrets.GroupVersion(): errors.New("503"),
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("503")}}
	r.disc.set(failing, served(GVRCertificates, GVRHTTPRoutes)...)
	err := r.c.Discover(nil)
	var gd *ErrGroupDiscoveryFailed
	if !errors.As(err, &gd) || len(gd.Groups) != 2 {
		t.Fatalf("err = %v, want the group failure with both groups", err)
	}
	if !r.c.Has(GVRExternalSecrets) {
		t.Error("the failed group lost its present resource")
	}
	if !r.c.Listed(GVRExternalSecrets) {
		t.Error("a resource of a failed group is not listed")
	}
	if !r.c.Has(GVRHTTPRoutes) || !r.c.Has(GVRCertificates) {
		t.Error("a healthy group was not applied")
	}
	if got := r.c.FailedGroups(); len(got) != 1 || got[0] != GVRExternalSecrets.GroupVersion() {
		t.Errorf("FailedGroups = %v: only a wanted group counts, and the metrics group is not one", got)
	}

	// The group recovers.
	r.disc.set(nil, served(GVRCertificates, GVRExternalSecrets, GVRHTTPRoutes)...)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	if got := r.c.FailedGroups(); len(got) != 0 {
		t.Errorf("FailedGroups after recovery = %v", got)
	}
}

// A discovery that fails altogether keeps what was known; before anything was known it makes
// every wanted resource count as listed so nothing waits for ever.
func TestTotalDiscoveryFailureKeepsState(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCertificates, GVRHTTPRoutes}, object(GVRCertificates, "Certificate", "a", "c"))
	r.c.AddDynamic(GVRCertificates, KindCertificates)
	r.c.AddDynamic(GVRHTTPRoutes, KindHTTPRoutes)
	r.start(t)
	boom := errors.New("connection refused")

	r.disc.set(boom)
	if err := r.c.Discover(nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !r.c.Listed(GVRCertificates) || !r.c.Listed(GVRHTTPRoutes) || r.c.Has(GVRCertificates) {
		t.Errorf("a first discovery that failed: listed %v %v, has %v", r.c.Listed(GVRCertificates), r.c.Listed(GVRHTTPRoutes), r.c.Has(GVRCertificates))
	}
	if got := r.c.FailedGroups(); len(got) != 2 {
		t.Errorf("FailedGroups = %v, want both wanted groups", got)
	}

	r.disc.set(nil, served(GVRCertificates)...)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the certificates", func() bool { return r.c.Listed(GVRCertificates) && len(r.c.List(GVRCertificates)) == 1 })

	r.disc.set(boom)
	if err := r.c.Discover(nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if !r.c.Has(GVRCertificates) || len(r.c.List(GVRCertificates)) != 1 || r.c.Has(GVRHTTPRoutes) {
		t.Error("a failed discovery changed the state")
	}
	if got := r.c.FailedGroups(); len(got) != 0 {
		t.Errorf("FailedGroups = %v: with a state to keep, a total failure names no group", got)
	}
}

func TestCRDRemovedMarksAbsent(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCertificates}, object(GVRCertificates, "Certificate", "a", "c"))
	var crds atomic.Int32
	r.c.OnChange(func(k Kind) {
		if k == KindCRDs {
			crds.Add(1)
		}
	})
	r.disc.set(nil, served(GVRCertificates)...)
	r.start(t)
	r.c.AddDynamic(GVRCertificates, KindCertificates)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the list", func() bool { return len(r.c.List(GVRCertificates)) == 1 })
	rev := r.c.Revision()

	r.disc.set(nil)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	if r.c.Has(GVRCertificates) || r.c.List(GVRCertificates) != nil || !r.c.Listed(GVRCertificates) {
		t.Errorf("a removed CRD: Has %v, List %v, Listed %v", r.c.Has(GVRCertificates), r.c.List(GVRCertificates), r.c.Listed(GVRCertificates))
	}
	if crds.Load() != 2 || r.c.Revision() <= rev {
		t.Errorf("KindCRDs %d (want 2: appeared, vanished), revision %d (was %d)", crds.Load(), r.c.Revision(), rev)
	}
	// It comes back: the informer that kept running serves it again.
	r.disc.set(nil, served(GVRCertificates)...)
	if err := r.c.Discover(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the CRD to come back", func() bool { return len(r.c.List(GVRCertificates)) == 1 })
}

// Revision moves for the resources asked for only.
func TestRevisionIsPerResource(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRCertificates, GVRHTTPRoutes})
	r.disc.set(nil, served(GVRCertificates, GVRHTTPRoutes)...)
	r.start(t)
	if err := r.c.Discover(map[schema.GroupVersionResource]Kind{GVRCertificates: KindCertificates, GVRHTTPRoutes: KindHTTPRoutes}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both listed", func() bool { return r.c.Listed(GVRCertificates) && r.c.Listed(GVRHTTPRoutes) })
	certs, routes, all := r.c.Revision(GVRCertificates), r.c.Revision(GVRHTTPRoutes), r.c.Revision()

	if _, err := r.dyn.Resource(GVRCertificates).Namespace("a").Create(context.Background(), object(GVRCertificates, "Certificate", "a", "c"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the certificate revision", func() bool { return r.c.Revision(GVRCertificates) > certs })
	if r.c.Revision(GVRHTTPRoutes) != routes {
		t.Error("a certificate moved the routes' revision")
	}
	if r.c.Revision() <= all {
		t.Error("the total did not move")
	}
}

// Patch is a JSON merge patch under the field manager dash.
func TestPatchSendsAMergePatchAsDash(t *testing.T) {
	r := newRig(t, []schema.GroupVersionResource{GVRKustomizations}, object(GVRKustomizations, "Kustomization", "flux-system", "apps"))
	var seen clienttesting.PatchAction
	r.dyn.PrependReactor("patch", "kustomizations", func(a clienttesting.Action) (bool, runtime.Object, error) {
		seen = a.(clienttesting.PatchAction)
		return false, nil, nil
	})
	if err := r.c.Patch(context.Background(), GVRKustomizations, "flux-system", "apps", []byte(`{"metadata":{"annotations":{"a":"b"}}}`)); err != nil {
		t.Fatal(err)
	}
	if seen == nil || string(seen.GetPatchType()) != "application/merge-patch+json" || string(seen.GetPatch()) != `{"metadata":{"annotations":{"a":"b"}}}` ||
		seen.GetNamespace() != "flux-system" || seen.GetName() != "apps" {
		t.Fatalf("patch = %+v", seen)
	}
	// The API server's error is returned as it is.
	r.dyn.PrependReactor("patch", "kustomizations", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(GVRKustomizations.GroupResource(), "apps", errors.New("denied"))
	})
	if err := r.c.Patch(context.Background(), GVRKustomizations, "flux-system", "apps", []byte(`{}`)); !apierrors.IsForbidden(err) {
		t.Errorf("err = %v, want the forbidden status unwrapped", err)
	}
}

// The fake clientset's own discovery serves the resources a test lists.
func TestDiscoverReadsTheClientsDiscovery(t *testing.T) {
	client := kubefake.NewClientset()
	client.Discovery().(*fakediscovery.FakeDiscovery).Resources = served(GVRHTTPRoutes)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(GVRHTTPRoutes))
	c := NewFromClients(client, dyn)
	if err := c.Discover(map[schema.GroupVersionResource]Kind{GVRHTTPRoutes: KindHTTPRoutes}); err != nil {
		t.Fatal(err)
	}
	if !c.Has(GVRHTTPRoutes) {
		t.Error("the route CRD is not present")
	}
}
