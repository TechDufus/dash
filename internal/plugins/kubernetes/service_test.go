package kubernetes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// serviceOf is what the plugin exports, from outside.
func (e *env) serviceOf() any {
	e.t.Helper()
	v, ok := e.sup.Service(ID)
	if !ok {
		e.t.Fatal("the plugin exports no service")
	}
	return v
}

// One object implements both interfaces: Proxmox asserts NodeIndex on it, the Manager's flux
// code asserts Cluster.
func TestServiceImplementsTheNodeIndexAndTheCluster(t *testing.T) {
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{node("k8s-cp-1", "192.0.2.11", true), node("k8s-cp-2", "192.0.2.12", false)}})
	e.healthy()
	v := e.serviceOf()
	idx, ok := v.(svc.NodeIndex)
	if !ok {
		t.Fatal("the service is not a svc.NodeIndex")
	}
	if _, ok := v.(svc.Cluster); !ok {
		t.Fatal("the service is not a svc.Cluster")
	}
	if !idx.Synced() {
		t.Error("Synced is false on a healthy plugin")
	}
	if name, ready, ok := idx.K8sNode("k8s-cp-1"); !ok || !ready || name != "k8s-cp-1" {
		t.Errorf("K8sNode(k8s-cp-1) = %q %v %v", name, ready, ok)
	}
	if _, ready, ok := idx.K8sNode("k8s-cp-2"); !ok || ready {
		t.Errorf("K8sNode(k8s-cp-2): ready %v, ok %v, want a node that is not ready", ready, ok)
	}
	if _, _, ok := idx.K8sNode("pve-1"); ok {
		t.Error("a VM that is no node is a node")
	}
	if ip, ok := idx.NodeIP("k8s-cp-2"); !ok || ip != "192.0.2.12" {
		t.Errorf("NodeIP = %q %v", ip, ok)
	}
	if _, ok := idx.NodeIP("nope"); ok {
		t.Error("an unknown node has an address")
	}
}

// A node that appears, changes readiness or goes moves Revision; a restart of the plugin never
// repeats a value.
func TestNodeIndexRevisionMovesWithNodes(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.healthy()
	idx := e.serviceOf().(svc.NodeIndex)
	rev := idx.Revision()
	moved := func(what string) {
		t.Helper()
		waitFor(t, "the revision to move after "+what, func() bool { return idx.Revision() > rev })
		rev = idx.Revision()
	}
	nodes := e.kc.CoreV1().Nodes()
	if _, err := nodes.Create(context.Background(), node("n1", "192.0.2.11", true), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	moved("a node was added")
	if _, err := nodes.Update(context.Background(), node("n1", "192.0.2.11", false), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	moved("a node went not ready")
	waitFor(t, "the node in the cache", func() bool { _, ready, ok := idx.K8sNode("n1"); return ok && !ready })
	if err := nodes.Delete(context.Background(), "n1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	moved("a node was deleted")

	// A restart (a reload of the section) builds a new service whose revision is above all of the
	// old one's.
	old := e.serviceOf()
	before := idx.Revision()
	cfg := *e.cfg
	cfg.QPS, cfg.Burst = 30, 60
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg})
	e.sup.Reload()
	waitFor(t, "the restarted plugin", func() bool {
		v, ok := e.sup.Service(ID)
		return ok && v != old && v.(svc.NodeIndex).Revision() > before
	})
}

// While the typed informers have not synced, the VM-to-node lookup answers nothing: Proxmox holds
// its VMs back on Synced.
func TestK8sNodeIsNotOKBeforeTheSync(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	open := func() { once.Do(func() { close(release) }) }
	t.Cleanup(open)
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{node("n1", "192.0.2.11", true)}, prefill: func(e *env) {
		e.kc.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
			<-release
			return false, nil, nil
		})
	}})
	e.state(plugin.StateStarting)
	idx := e.serviceOf().(svc.NodeIndex)
	if idx.Synced() {
		t.Error("Synced before the nodes were listed")
	}
	if _, _, ok := idx.K8sNode("n1"); ok {
		t.Error("K8sNode answers before the sync")
	}
	open()
	waitFor(t, "the node", func() bool { _, _, ok := idx.K8sNode("n1"); return ok && idx.Synced() })
}

// GVRRevision counts per resource; Watch asks for a resource and the plugin discovers it at once;
// Patch is a merge patch as dash.
func TestClusterServiceWatchesListsAndPatches(t *testing.T) {
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{obj(kube.GVRCertificates, "app", "web-tls")}})
	e.healthy()
	cl := e.serviceOf().(svc.Cluster)
	if !cl.Synced() {
		t.Error("Synced")
	}
	if !cl.Has(kube.GVRCertificates) || cl.Has(kube.GVRKustomizations) {
		t.Errorf("Has: certificates %v, kustomizations %v", cl.Has(kube.GVRCertificates), cl.Has(kube.GVRKustomizations))
	}
	if got := cl.List(kube.GVRCertificates); len(got) != 1 || got[0].GetName() != "web-tls" {
		t.Errorf("List = %v", got)
	}
	certs, flux := cl.GVRRevision(kube.GVRCertificates), cl.GVRRevision(kube.GVRKustomizations)
	if _, err := e.dc.Resource(kube.GVRCertificates).Namespace("app").Create(context.Background(), obj(kube.GVRCertificates, "app", "other-tls"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the certificate revision", func() bool { return cl.GVRRevision(kube.GVRCertificates) > certs })
	if cl.GVRRevision(kube.GVRKustomizations) != flux {
		t.Error("a certificate moved the kustomizations' revision")
	}

	// Flux appears in the cluster after the plugin started; asking for it triggers a discovery.
	if _, err := e.dc.Resource(kube.GVRKustomizations).Namespace("flux-system").Create(context.Background(), obj(kube.GVRKustomizations, "flux-system", "apps"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e.disc.Resources = append(e.disc.Resources, served(kube.GVRKustomizations)...)
	cl.Watch(kube.GVRKustomizations)
	waitFor(t, "the kustomizations to be listed", func() bool { return cl.Listed(kube.GVRKustomizations) && len(cl.List(kube.GVRKustomizations)) == 1 })

	patch := []byte(`{"metadata":{"annotations":{"reconcile.fluxcd.io/requestedAt":"2026-06-01T12:00:00Z"}}}`)
	if err := cl.Patch(context.Background(), kube.GVRKustomizations, "flux-system", "apps", patch); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	u, err := e.dc.Resource(kube.GVRKustomizations).Namespace("flux-system").Get(context.Background(), "apps", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := u.GetAnnotations()["reconcile.fluxcd.io/requestedAt"]; got != "2026-06-01T12:00:00Z" {
		t.Errorf("annotation = %q", got)
	}
}

// The transport hook of upstreamViaAPIServer reads the running plugin's service per request: no
// service, no wrap; the flag off, no wrap; the flag on with a REST config, a wrap.
func TestUpstreamViaAPIServerWrapsLazily(t *testing.T) {
	wrapOf := func(e *env) func(http.RoundTripper) http.RoundTripper {
		v, ok := e.sup.Service(ID)
		if !ok {
			return nil
		}
		h, ok := v.(interface {
			APIServerTransport() func(http.RoundTripper) http.RoundTripper
		})
		if !ok {
			t.Fatal("the service has no APIServerTransport")
		}
		return h.APIServerTransport()
	}
	off := newEnv(t, envOpts{})
	off.healthy()
	if wrapOf(off) != nil {
		t.Error("a wrap with upstreamViaAPIServer off")
	}

	on := newEnv(t, envOpts{cfg: func(c *pluginconf.Kubernetes) { c.UpstreamViaAPIServer = true }, noRun: true})
	if wrapOf(on) != nil {
		t.Error("a wrap before the plugin runs")
	}
	on.start(t)
	on.healthy()
	if wrapOf(on) != nil {
		t.Error("a wrap without a REST config (fake clients)")
	}
	on.mu.Lock()
	on.rest = &rest.Config{Host: "https://192.0.2.1:6443"}
	on.mu.Unlock()
	cfg := *on.cfg
	cfg.QPS = 21
	on.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg})
	on.sup.Reload()
	var wrap func(http.RoundTripper) http.RoundTripper
	waitFor(t, "the wrap", func() bool { wrap = wrapOf(on); return wrap != nil })
	var seen *http.Request
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{StatusCode: 204, Body: http.NoBody}, nil
	})
	req, _ := http.NewRequest(http.MethodGet, "https://grafana.example.com/api", nil)
	resp, err := wrap(base).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if seen == nil || seen.URL.Host != "grafana.example.com" {
		t.Errorf("a request for a host that is no service went to %v", seen)
	}
}

// A REST config the transport cannot be built from is logged once, not on every request.
func TestAPIServerTransportErrorIsLoggedOncePerChange(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg: func(c *pluginconf.Kubernetes) { c.UpstreamViaAPIServer = true },
		prefill: func(e *env) {
			e.rest = &rest.Config{Host: "https://192.0.2.1:6443", TLSClientConfig: rest.TLSClientConfig{CAData: []byte("not a certificate")}}
		},
	})
	e.healthy()
	v, ok := e.sup.Service(ID)
	if !ok {
		t.Fatal("no service")
	}
	hook := v.(interface {
		APIServerTransport() func(http.RoundTripper) http.RoundTripper
	})
	for range 50 {
		if hook.APIServerTransport() != nil {
			t.Fatal("a wrap from a broken REST config")
		}
	}
	if got := strings.Count(e.logs.String(), "cannot route upstream requests through the API server"); got != 1 {
		t.Errorf("the error was logged %d times for 50 requests, want once", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The pod sheet is a read: a namespace that actions.denyNamespaces refuses is still readable,
// because the kubernetes section has no read deny list.
func TestPodDetailIgnoresActionDenyNamespaces(t *testing.T) {
	if _, has := any(pluginconf.Kubernetes{}).(interface{ ReadDenyConfig() []string }); has {
		t.Fatal("the kubernetes section implements ReadDenyConfig: actions.denyNamespaces would refuse a viewer's pod sheet")
	}
	e := newEnv(t, envOpts{
		objs: []k8sruntime.Object{namespace("media", nil), runningPod("media", "app-0", "n1")},
		cfg: func(c *pluginconf.Kubernetes) {
			c.Actions = pluginconf.Actions{Enabled: true, DenyNamespaces: []string{"media"}}
		},
	})
	e.healthy()
	if w := e.get("/pods/media/app-0"); w.Code != http.StatusOK {
		t.Fatalf("pod detail in a namespace denied to actions = %d %s", w.Code, w.Body)
	}
	for _, ri := range e.sup.RouteInfos(ID) {
		if ri.Spec.Namespace != nil {
			t.Errorf("route %s names a namespace for the read-deny check", ri.Pattern)
		}
	}
}

// The objects carry the links the running link providers give them (here a Headlamp-like one).
func TestClusterObjectsCarryTheProvidersLinks(t *testing.T) {
	e := newEnv(t, envOpts{
		objs: []k8sruntime.Object{node("n1", "192.0.2.11", true), namespace("media", nil), runningPod("media", "app-0", "n1"), deployment("media", "app", 1, 1)},
		links: func(r plugin.LinkRequest) []model.Link {
			return []model.Link{{Kind: "headlamp", Label: r.Kind + " in Headlamp", Href: "https://headlamp.example.com/" + r.Kind + "/" + r.Namespace + "/" + r.Name}}
		},
	})
	e.healthy()
	var cl model.ClusterData
	waitFor(t, "the links", func() bool {
		e.payload("kubernetes.cluster", &cl)
		return len(cl.Pods) == 1 && len(cl.Pods[0].Links) == 1
	})
	if got := cl.Pods[0].Links[0].Href; got != "https://headlamp.example.com/Pod/media/app-0" {
		t.Errorf("pod link = %q", got)
	}
	if len(cl.Nodes[0].Links) != 1 || len(cl.Workloads[0].Links) != 1 {
		t.Errorf("node links %v, workload links %v", cl.Nodes[0].Links, cl.Workloads[0].Links)
	}
}

// forbidList makes the list of a resource a client may not make.
func forbidList(e *env, resource string) {
	e.dc.PrependReactor("list", resource, func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("no"))
	})
}

// svc.Cluster.Refused: a served resource whose list the client may not make is refused after the
// sync timeout of API-server up-time, with the category of the watch's error. Before that, for a
// resource that lists, and for one nobody registered, it is not.
func TestServiceRefusesAServedListThatNeverCompletes(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true, prefill: func(e *env) { forbidList(e, "certificates") }})
	cl := e.serviceOf().(svc.Cluster)
	r := e.runtime()
	waitFor(t, "the watch error of the certificates", func() bool { return r.cl.WatchError(kube.GVRCertificates) != nil })
	waitFor(t, "the external secrets to list", func() bool { return cl.Listed(kube.GVRExternalSecrets) })

	refused := func(gvr schema.GroupVersionResource) (string, bool) { return cl.Refused(gvr) }
	if cat, ok := refused(kube.GVRCertificates); ok {
		t.Fatalf("refused at once: %q", cat)
	}
	e.clock.advance(59 * time.Second)
	if cat, ok := refused(kube.GVRCertificates); ok {
		t.Fatalf("refused after 59 s: %q", cat)
	}
	e.clock.advance(time.Second)
	if cat, ok := refused(kube.GVRCertificates); !ok || cat != "auth" {
		t.Errorf("Refused(certificates) after 60 s = %q, %v, want auth, true", cat, ok)
	}
	if cat, ok := refused(kube.GVRExternalSecrets); ok {
		t.Errorf("a resource that lists is refused: %q", cat)
	}
	if cat, ok := refused(kube.GVRKustomizations); ok {
		t.Errorf("a resource nobody registered is refused: %q", cat)
	}
}

// A resource a consumer registers through Watch has the clock too, counted from the moment it
// appears: one that shows up an hour in is not refused when it does, though dash started an hour
// ago.
func TestServiceRefusedClockOfAWatchedResourceStartsWhenItAppears(t *testing.T) {
	gvr := kube.GVRKustomizations
	d := &stubDisc{}
	d.set(nil, served(allServed...)...)
	e := newEnv(t, envOpts{fakeClock: true, prefill: func(e *env) {
		e.discovery = d
		forbidList(e, "kustomizations")
	}})
	e.healthy()
	cl := e.serviceOf().(svc.Cluster)
	r := e.runtime()

	cl.Watch(gvr) // not served yet
	e.clock.advance(time.Hour)
	if cat, ok := cl.Refused(gvr); ok {
		t.Fatalf("a resource that is not served is refused: %q", cat)
	}

	d.set(nil, served(append(slices.Clone(allServed), gvr)...)...)
	e.clock.advance(time.Second) // the next discovery sees it
	waitFor(t, "the kustomizations to be served", func() bool { return cl.Has(gvr) })
	if cat, ok := cl.Refused(gvr); ok {
		t.Fatalf("refused the moment it appeared, with an hour of dash up-time: %q", cat)
	}
	waitFor(t, "the watch error", func() bool { return r.cl.WatchError(gvr) != nil })
	e.clock.advance(58 * time.Second)
	if cat, ok := cl.Refused(gvr); ok {
		t.Fatalf("refused 58 s after it appeared: %q", cat)
	}
	e.clock.advance(2 * time.Second)
	if cat, ok := cl.Refused(gvr); !ok || cat != "auth" {
		t.Errorf("Refused after 60 s = %q, %v, want auth, true", cat, ok)
	}
}

// A list that never completes and never says why (a conversion webhook that does not answer; here
// an informer that never ran) is refused as unreachable.
func TestRefusedCategoryIsUnreachableWithoutAWatchError(t *testing.T) {
	gvr := kube.GVRExternalSecrets
	kc := kubefake.NewClientset()
	kc.Discovery().(*fakediscovery.FakeDiscovery).Resources = served(gvr)
	dc := dynfake.NewSimpleDynamicClientWithCustomListKinds(k8sruntime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "externalsecretsList"})
	c := kube.NewFromClients(kc, dc) // not started: the informer exists and never lists
	if err := c.Discover(map[schema.GroupVersionResource]kube.Kind{gvr: kube.KindExtSecrets}); err != nil {
		t.Fatal(err)
	}
	if !c.Has(gvr) || c.Listed(gvr) || c.WatchError(gvr) != nil {
		t.Fatalf("the fixture: has %v, listed %v, watch error %v", c.Has(gvr), c.Listed(gvr), c.WatchError(gvr))
	}

	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r := &runtime{cl: c, started: t0, t: timing{typedSyncTimeout: 60 * time.Second}}
	r.h = dtHost{now: t0.Add(59 * time.Second)}
	if cat, ok := r.refused(gvr); ok {
		t.Fatalf("refused after 59 s: %q", cat)
	}
	r.h = dtHost{now: t0.Add(60 * time.Second)}
	if cat, ok := r.refused(gvr); !ok || cat != "unreachable" {
		t.Errorf("refused = %q, %v, want unreachable, true", cat, ok)
	}
	if _, ok := (&service{r: r}).Refused(kube.GVRCertificates); ok {
		t.Error("a resource nobody registered is refused")
	}
}
