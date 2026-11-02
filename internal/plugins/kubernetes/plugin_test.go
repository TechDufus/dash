package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// fakeClock is the clock of Options.Now: a test moves it, so the debounce and the sync timeout run
// in milliseconds.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// syncBuffer is a bytes.Buffer that is safe to write from the plugin's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// env is the plugin under a real supervisor and hub, over a fake cluster the test controls.
type env struct {
	t     *testing.T
	hub   *hub.Hub
	sup   *plugin.Supervisor
	set   *plugintest.Settings
	kc    *kubefake.Clientset
	dc    *dynfake.FakeDynamicClient
	disc  *fakediscovery.FakeDiscovery
	prom  *fakeProm
	gatus *fakeGatus
	cfg   *pluginconf.Kubernetes
	clock *fakeClock
	logs  *syncBuffer

	failProbe atomic.Bool  // the API server's /version fails
	requests  atomic.Int64 // every request the fake API server has seen

	mu          sync.Mutex
	connErr     error
	discovery   discovery.ServerResourcesInterface // replaces the clientset's discovery when set
	connections int
	cluster     *kube.Cluster // the last Cluster connect made
	p           *Plugin       // the last plugin the factory made
	rest        *rest.Config  // what connect reports as the REST config (nil for fake clients)

	promReleased atomic.Bool // see envOpts.lateProm
}

type envOpts struct {
	cfg       func(*pluginconf.Kubernetes)
	objs      []k8sruntime.Object
	crds      []schema.GroupVersionResource // served; nil is certificates, external secrets, routes and CNPG
	noCRDs    bool                          // serve none
	noProm    bool
	lateProm  bool // prometheus runs, but exports its service only after env.promReleased is set
	noGatus   bool
	timing    func(*timing)
	fakeClock bool
	connErr   error
	prefill   func(*env) // fills the fakes before the plugin starts: a poll never sees half of it
	demo      bool
	links     func(plugin.LinkRequest) []model.Link // a headlamp-like link provider
	noRun     bool                                  // build everything but do not start the supervisor
	refs      *secret.K8sRefs                       // the k8s: references the plugin reads (nil: none)
}

func fastTiming() timing {
	return timing{
		probe: 5 * time.Millisecond, usage: 20 * time.Millisecond, storage: 20 * time.Millisecond,
		discovery: 50 * time.Millisecond, tick: 10 * time.Millisecond,
		unavailableAfter: 10 * time.Second, typedSyncTimeout: 60 * time.Second,
	}
}

// lateService is a fake plugin that exports its service once released is set.
type lateService struct {
	*plugintest.Fake
	released *atomic.Bool
}

func (l *lateService) Service() any {
	if !l.released.Load() {
		return nil
	}
	return l.Fake.Service()
}

// newEnv starts the plugin (or its twin) and the fakes it uses.
func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	e := &env{t: t, logs: &syncBuffer{}, prom: &fakeProm{samples: map[string][]svc.Sample{}, errs: map[string]error{}}, gatus: &fakeGatus{rev: 1}}
	cfg := pluginconf.DefaultKubernetes()
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	e.cfg = &cfg

	var typed, custom []k8sruntime.Object
	for _, ob := range o.objs {
		if _, ok := ob.(interface{ UnstructuredContent() map[string]any }); ok {
			custom = append(custom, ob)
		} else {
			typed = append(typed, ob)
		}
	}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, g := range append([]schema.GroupVersionResource{kube.GVRKustomizations}, allCRDs...) {
		listKinds[g] = g.Resource + "List"
	}
	e.kc = kubefake.NewClientset(typed...)
	e.dc = dynfake.NewSimpleDynamicClientWithCustomListKinds(k8sruntime.NewScheme(), listKinds, custom...)
	e.disc = e.kc.Discovery().(*fakediscovery.FakeDiscovery)
	crds := o.crds
	if crds == nil && !o.noCRDs {
		crds = []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters}
	}
	e.disc.Resources = served(crds...)
	e.connErr = o.connErr
	count := func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		e.requests.Add(1)
		return false, nil, nil
	}
	e.kc.PrependReactor("*", "*", count)
	e.dc.PrependReactor("*", "*", count)
	e.kc.PrependWatchReactor("*", func(clienttesting.Action) (bool, watch.Interface, error) {
		e.requests.Add(1)
		return false, nil, nil
	})
	e.kc.PrependReactor("get", "version", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		if e.failProbe.Load() {
			return true, nil, errors.New("connection refused")
		}
		return false, nil, nil
	})

	tm := fastTiming()
	if o.timing != nil {
		o.timing(&tm)
	}
	factory := func() plugin.Plugin {
		p := &Plugin{t: tm, connect: e.connect, refs: o.refs}
		e.mu.Lock()
		e.p = p
		e.mu.Unlock()
		return p
	}

	e.hub = hub.New()
	e.set = &plugintest.Settings{}
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg, ActionsEnabled: cfg.Actions.Enabled, DenyNamespaces: cfg.Actions.DenyNamespaces})
	opts := plugin.Options{
		Publisher: e.hub, Settings: e.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log:  slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Demo: o.demo,
	}
	if o.fakeClock {
		e.clock = newClock()
		opts.Now = e.clock.Now
	}
	e.sup = plugin.NewSupervisor(opts)
	if !o.noProm {
		prom := plugintest.New("prometheus")
		prom.ServiceVal = svc.Querier(e.prom)
		factory := prom.Factory()
		if o.lateProm {
			// The service is withheld until the test releases it: a credential filled late.
			factory = func() plugin.Plugin { return &lateService{Fake: prom, released: &e.promReleased} }
		}
		if err := e.sup.Add(factory); err != nil {
			t.Fatal(err)
		}
	}
	if o.links != nil {
		hl := plugintest.New("headlamp")
		hl.LinksFn = o.links
		if err := e.sup.Add(hl.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if !o.noGatus {
		g := plugintest.New("gatus")
		g.ServiceVal = svc.EndpointHealth(e.gatus)
		if err := e.sup.Add(g.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.sup.Add(factory); err != nil {
		t.Fatal(err)
	}
	e.hub.SetOrder(e.sup.TopicOrder)
	if o.prefill != nil {
		o.prefill(e)
	}
	if o.noRun {
		return e
	}
	e.start(t)
	return e
}

// start runs the supervisor of an env built with noRun.
func (e *env) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); e.sup.Shutdown(context.Background()) })
}

// connect is the Connect of the plugin under test: a fresh Cluster over the same fake clients on
// every start, as a real restart builds one.
func (e *env) connect(pluginconf.Kubernetes) (*Connection, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.connErr != nil {
		return nil, e.connErr
	}
	cl := kube.NewFromClients(e.kc, e.dc)
	if e.discovery != nil {
		cl.SetDiscovery(e.discovery)
	}
	e.connections++
	e.cluster = cl
	return &Connection{Cluster: cl, REST: e.rest}, nil
}

// payload reads the latest envelope of a topic into out and returns its sources.
func (e *env) payload(topic string, out any) ([]model.SourceStatus, bool) {
	e.t.Helper()
	ev, found := e.hub.Latest("", model.Topic(topic))
	if !found {
		return nil, false
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		e.t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		e.t.Fatal(err)
	}
	return env.Sources, true
}

// info is the plugin's row.
func (e *env) info() model.PluginInfo {
	e.t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == string(ID) {
			return pi
		}
	}
	e.t.Fatal("the plugin has no row")
	return model.PluginInfo{}
}

// state waits for the row to reach a state.
func (e *env) state(want plugin.State) model.PluginInfo {
	e.t.Helper()
	var pi model.PluginInfo
	waitFor(e.t, "the row to be "+string(want), func() bool {
		pi = e.info()
		return pi.State == want
	})
	return pi
}

// healthy waits for the row to settle: healthy, with the three topics published.
func (e *env) healthy() model.PluginInfo {
	e.t.Helper()
	pi := e.state(plugin.StateHealthy)
	for _, topic := range []string{"kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"} {
		waitFor(e.t, topic, func() bool { _, ok := e.hub.Latest("", model.Topic(topic)); return ok })
	}
	return pi
}

// runtime is the plugin's running instance.
func (e *env) runtime() *runtime {
	e.t.Helper()
	var r *runtime
	waitFor(e.t, "the runtime", func() bool {
		p := e.plugin()
		if p == nil {
			return false
		}
		r = p.rt.Load()
		return r != nil
	})
	return r
}

// plugin is the plugin instance the supervisor started last.
func (e *env) plugin() *Plugin {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.p
}

func (e *env) get(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	h, ok := e.sup.Router(ID, plugin.RouteGuard{})
	if !ok {
		e.t.Fatal("the plugin has no routes")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/plugins/kubernetes"+path, nil))
	return w
}

// A plugin that has not started has no row state of its own.
func TestManifestKeepsTheShimsNavAndTopics(t *testing.T) {
	m := New().Manifest()
	if m.ID != "kubernetes" || m.Name != "Kubernetes" || m.Version != "1.0.0" || m.Description != "How dash reaches and reads the cluster." {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != (model.NavItem{Path: "/cluster", Label: "Cluster", Title: "Kubernetes cluster", Icon: "Boxes"}) {
		t.Errorf("nav = %+v", m.UI.Nav)
	}
	var topics []string
	for _, tp := range m.Topics {
		topics = append(topics, tp.Name)
		if tp.Legacy != "" {
			t.Errorf("topic %s keeps the legacy name %q", tp.Name, tp.Legacy)
		}
	}
	if !slices.Equal(topics, []string{"cluster", "storage", "apps"}) {
		t.Errorf("topics = %v: the wire order is cluster, storage, apps", topics)
	}
	var series []string
	for _, s := range m.Series {
		series = append(series, s.Name)
	}
	if !slices.Equal(series, []string{"node.cpu", "node.mem", "pod.cpu", "pod.mem"}) {
		t.Errorf("series = %v", series)
	}
	if len(m.Actions) != 1 || m.Actions[0].Name != "restart" {
		t.Errorf("actions = %+v", m.Actions)
	}
	if len(m.Requires) != 0 || !slices.Equal(m.Uses, []plugin.ID{"prometheus", "gatus"}) {
		t.Errorf("requires %v, uses %v", m.Requires, m.Uses)
	}
	if h := New().(*Plugin).ConfigHint(); h != "plugins.kubernetes.kubeconfig or run in a cluster" {
		t.Errorf("hint = %q", h)
	}
	if !New().(plugin.Configured).Configured(&pluginconf.Kubernetes{}) {
		t.Error("the plugin is not always configured")
	}
}

// The whole path: connect, sync, discover, poll, publish; the row, the topics and the sources.
func TestStartsAndPublishesEverything(t *testing.T) {
	e := newEnv(t, envOpts{objs: []k8sruntime.Object{
		node("k8s-cp-1", "192.0.2.11", true), namespace("media", nil), runningPod("media", "app-0", "k8s-cp-1"), deployment("media", "app", 1, 1),
		pvc("media", "data", 10<<30),
	}})
	pi := e.healthy()
	if len(pi.Topics) != 3 || pi.Message != "" {
		t.Errorf("row = %+v", pi)
	}
	var cl model.ClusterData
	srcs, _ := e.payload("kubernetes.cluster", &cl)
	if len(cl.Nodes) != 1 || cl.Nodes[0].Ref != "kubernetes:Node/k8s-cp-1" || len(cl.Pods) != 1 || len(cl.Workloads) != 1 {
		t.Errorf("cluster = %+v", cl)
	}
	names := map[string]bool{}
	for _, s := range srcs {
		names[s.Name] = s.OK
	}
	if !names["kubernetes"] || !names["prometheus"] {
		t.Errorf("cluster sources = %+v", srcs)
	}
	var st model.StorageData
	e.payload("kubernetes.storage", &st)
	if len(st.PVCs) != 1 || st.PVCs[0].Ref != "kubernetes:PVC/media/data" {
		t.Errorf("storage = %+v", st)
	}
	var ap model.AppsData
	e.payload("kubernetes.apps", &ap)
	if ap.Apps == nil {
		t.Error("apps is null on the wire")
	}
}

// Every goroutine of the plugin ends with it: its loops (counted by the runtime itself), its
// informers (the Cluster says when they have all ended: Stop shuts the factories down), and the
// Secret watchers of its k8s: references (Stop waits for them). The last value read stays. Nothing
// is counted process-wide: other tests' goroutines come and go.
func TestStopEndsEveryGoroutine(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	held, err := refs.Handle("k8s:dash/creds#token")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, envOpts{refs: refs, objs: []k8sruntime.Object{
		node("n1", "192.0.2.11", true), swSecret("dash", "creds", map[string]string{"token": swCanary}),
	}})
	e.healthy()
	swEventually(t, "the Secret watcher to read the value", reveals(held, swCanary))
	p := e.plugin()
	r := e.runtime()
	if r.live.Load() == 0 || e.cluster.Stopped() || p.secrets == nil {
		t.Fatalf("a running plugin: %d loops, informers stopped %v, watchers %v", r.live.Load(), e.cluster.Stopped(), p.secrets != nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n := r.live.Load(); n != 0 {
		t.Errorf("%d loops still run after Stop", n)
	}
	if !e.cluster.Stopped() {
		t.Error("an informer still runs after Stop")
	}
	if got := held.Reveal(); got != swCanary || held.Reason() != secret.ReasonKubernetesUnavailable {
		t.Errorf("after Stop the value must stay and the reason say so: reason %q", held.Reason())
	}
	if err := p.Stop(ctx); err != nil {
		t.Errorf("a second Stop: %v", err)
	}
}

func TestStatusBeforeStartIsStarting(t *testing.T) {
	if st := New().Status(); st.State != plugin.StateStarting {
		t.Errorf("status = %+v", st)
	}
	if New().(*Plugin).Service() != nil {
		t.Error("a plugin that has not started exports a service")
	}
}

// restart is mounted only while the instance has actions enabled, and the deny list reaches the
// core's chain (which answers the 403 and audits it, as it does for every plugin).
func TestRestartIsMountedOnlyWhenEnabled(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.healthy()
	if _, ok := e.sup.Action(ID, "restart"); ok {
		t.Fatal("restart is mounted with actions disabled")
	}
	cfg := *e.cfg
	cfg.Actions = pluginconf.Actions{Enabled: true, DenyNamespaces: []string{"kube-system"}}
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg, ActionsEnabled: true, DenyNamespaces: cfg.Actions.DenyNamespaces})
	e.sup.Reload()
	var ref plugin.ActionRef
	waitFor(t, "restart to be mounted", func() bool {
		var ok bool
		ref, ok = e.sup.Action(ID, "restart")
		return ok
	})
	if !slices.Equal(ref.DenyNamespaces, []string{"kube-system"}) {
		t.Errorf("deny list = %v", ref.DenyNamespaces)
	}
	if ref.Spec.MinRole != "admin" || !ref.Spec.Destructive {
		t.Errorf("spec = %+v", ref.Spec)
	}
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: e.cfg})
	e.sup.Reload()
	waitFor(t, "restart to be unmounted", func() bool { _, ok := e.sup.Action(ID, "restart"); return !ok })
}

// A start that panics is not this plugin's business; a Start with a configuration of the wrong
// type is an error of Start.
func TestStartRefusesAForeignConfig(t *testing.T) {
	p := New()
	if err := p.Start(context.Background(), nil, 42); err == nil {
		t.Error("Start accepted a configuration of the wrong type")
	}
	if err := New().(plugin.Validator).Validate(42); err == nil {
		t.Error("Validate accepted a configuration of the wrong type")
	}
	bad := pluginconf.DefaultKubernetes()
	bad.Burst = 1
	if err := New().(plugin.Validator).Validate(&bad); err == nil {
		t.Error("Validate accepted burst below qps")
	}
}
