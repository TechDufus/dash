package topics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// ---- a fake svc.EndpointHealth, supplied through Deps.Services ----

// fakeEndpoints is what the gatus plugin exports, with a poll the test drives by hand: set is the
// moment a poll finishes.
type fakeEndpoints struct {
	mu    sync.Mutex
	rev   uint64
	hosts map[string]model.Health
	names map[string]bool // lowercased check name -> passing
	src   model.SourceStatus
}

var _ svc.EndpointHealth = (*fakeEndpoints)(nil)

func (f *fakeEndpoints) set(rev uint64, hosts map[string]model.Health, names map[string]bool, src model.SourceStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev, f.hosts, f.names, f.src = rev, hosts, names, src
}

func (f *fakeEndpoints) ByHost(host string) (model.Health, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hosts[strings.ToLower(host)]
	return h, ok
}

func (f *fakeEndpoints) ByName(name string) (up, known bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	up, known = f.names[strings.ToLower(name)]
	return up, known
}

func (f *fakeEndpoints) Revision() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev
}

func (f *fakeEndpoints) SourceStatuses() []model.SourceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []model.SourceStatus{f.src}
}

// gatusHolder is Deps.Services over whichever fake the test says is running: nil is "the gatus
// plugin does not run".
type gatusHolder struct{ cur atomic.Pointer[fakeEndpoints] }

func (g *gatusHolder) services(id plugin.ID) (any, bool) {
	if f := g.cur.Load(); id == "gatus" && f != nil {
		return f, true
	}
	return nil, false
}

var (
	srcOK   = model.SourceStatus{Name: "gatus", OK: true}
	srcDown = func(cat string) model.SourceStatus { return model.SourceStatus{Name: "gatus", Error: cat} }
)

const grafanaHost = "grafana.example.com"

// ---- a cluster with one app, and a Manager over it ----

// routeCluster is a cluster whose informers sync at once and which has one HTTPRoute on
// gateway/home, for grafana.example.com.
func routeCluster() *kube.Cluster {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	route := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": "grafana", "namespace": "monitoring"},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": "home", "namespace": "gateway"}},
			"hostnames":  []any{grafanaHost},
		},
	}}
	return kube.NewFromClients(kubefake.NewClientset(),
		dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, route))
}

// manualManager is a Manager over routeCluster with its informers synced and its storage poller
// "done", that the test rebuilds by hand: nothing runs on a ticker, so a round is exactly what
// the test calls.
func manualManager(t *testing.T, services func(plugin.ID) (any, bool)) (*Manager, *hub.Hub) {
	t.Helper()
	h := hub.New()
	cfg := config.Defaults()
	m := New(Deps{
		Cfg: cfg, Cluster: routeCluster(), Hosts: plugintest.AllRunning(h, LegacyManifests()...),
		Services: services,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.d.Cluster.Start(ctx)
	eventually(t, "the informers to sync", m.d.Cluster.HasSynced)
	m.mu.Lock()
	m.done["storage"] = true
	m.mu.Unlock()
	return m, h
}

func decodeTopic[T any](t *testing.T, h *hub.Hub, topic model.Topic) (T, []model.SourceStatus, bool) {
	t.Helper()
	var d T
	env, ok := latest(h, topic)
	if !ok {
		return d, nil, false
	}
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatalf("%s: %v", topic, err)
	}
	return d, env.Sources, true
}

// appDot is the health dot of the one app, and whether apps is published at all.
func appDot(t *testing.T, h *hub.Hub) (dot *bool, srcs []model.SourceStatus, published bool) {
	t.Helper()
	d, srcs, ok := decodeTopic[model.AppsData](t, h, model.TopicApps)
	if !ok {
		return nil, nil, false
	}
	if len(d.Apps) != 1 || d.Apps[0].Href != "https://"+grafanaHost {
		t.Fatalf("apps = %+v, want the one grafana app", d.Apps)
	}
	return d.Apps[0].Healthy, srcs, true
}

func wantGatusChip(t *testing.T, topic string, srcs []model.SourceStatus, ok bool, errText string) {
	t.Helper()
	got, has := hasSource(srcs, "gatus")
	if !has || got.OK != ok || got.Error != errText {
		t.Errorf("%s sources = %+v, want a gatus chip with ok=%v error=%q", topic, srcs, ok, errText)
	}
}

func dotIs(dot *bool, want bool) bool { return dot != nil && *dot == want }

// ---- S-2: a gatus poll updates apps with nothing else dirty ----

// The status poller used to mark apps itself. The gatus plugin cannot, so a poll that changes a
// check, with no cluster change and no contributor to make the round do anything, must still
// reach the apps' health dot through the Manager's own tick (review S-2, M1c: the Revision check
// runs before rebuild's early return). The machines reached by a gatus check are the hardware
// plugin's, which follows the same service (internal/plugins/hardware).
func TestAGatusPollUpdatesAppsThroughTheManager(t *testing.T) {
	var g gatusHolder
	fe := &fakeEndpoints{}
	fe.set(1, map[string]model.Health{grafanaHost: model.HealthOK}, nil, srcOK)
	g.cur.Store(fe)
	m, h := manualManager(t, g.services)
	m.markAll()
	m.rebuild()

	dot, srcs, ok := appDot(t, h)
	if !ok || !dotIs(dot, true) {
		t.Fatalf("apps = published %v dot %v, want a healthy dot", ok, dot)
	}
	wantGatusChip(t, "apps", srcs, true, "")

	// Nothing is dirty and nothing contributes: this is the round that returns early.
	m.dmu.Lock()
	dirty := len(m.dirty)
	m.dmu.Unlock()
	if dirty != 0 || len(m.contributors()) != 0 {
		t.Fatalf("setup: %d dirty topics, %d contributors, want none", dirty, len(m.contributors()))
	}

	// A poll finishes: grafana fails (critical groups would say crit, any non-ok is a red dot),
	// and the source status is unchanged.
	fe.set(2, map[string]model.Health{grafanaHost: model.HealthWarn}, nil, srcOK)
	m.rebuild()
	if dot, _, _ := appDot(t, h); !dotIs(dot, false) {
		t.Errorf("the apps dot after the poll = %v, want unhealthy", dot)
	}

	// A critical group's failure is the same red dot; a recovery turns it green again.
	fe.set(3, map[string]model.Health{grafanaHost: model.HealthCrit}, nil, srcOK)
	m.rebuild()
	if dot, _, _ := appDot(t, h); !dotIs(dot, false) {
		t.Errorf("a crit check's dot = %v, want unhealthy", dot)
	}
	fe.set(4, map[string]model.Health{grafanaHost: model.HealthOK}, nil, srcOK)
	m.rebuild()
	if dot, _, _ := appDot(t, h); !dotIs(dot, true) {
		t.Errorf("the apps dot after recovery = %v, want healthy", dot)
	}

	// An unchanged Revision is not a change: the quiet round stays quiet.
	m.rebuild()
	m.dmu.Lock()
	dirty = len(m.dirty)
	m.dmu.Unlock()
	if dirty != 0 {
		t.Errorf("%d topics marked with no change", dirty)
	}
}

// A check that is not in Gatus gives an app no dot at all (it is not "unhealthy").
func TestAnUnknownHostHasNoDot(t *testing.T) {
	var g gatusHolder
	fe := &fakeEndpoints{}
	fe.set(1, map[string]model.Health{"other.example.com": model.HealthOK}, map[string]bool{}, srcOK)
	g.cur.Store(fe)
	m, h := manualManager(t, g.services)
	m.markAll()
	m.rebuild()
	if dot, _, ok := appDot(t, h); !ok || dot != nil {
		t.Errorf("dot = %v (published %v), want none for a host no check probes", dot, ok)
	}
}

// ---- M1a: a failed first poll opens the apps gate ----

// Before the first poll has finished the apps wait (the dots would be a lie); a first poll that
// fails counts as finished, so apps publish with a failing gatus source instead of waiting for
// a Gatus that is down (the old pollDone("status") opened after one attempt).
func TestAFailedFirstGatusPollOpensTheAppsGate(t *testing.T) {
	var g gatusHolder
	fe := &fakeEndpoints{}
	fe.set(0, nil, nil, model.SourceStatus{Name: "gatus", Error: "not polled yet"})
	g.cur.Store(fe)
	m, h := manualManager(t, g.services)
	m.markAll()
	m.rebuild()
	if _, _, ok := appDot(t, h); ok {
		t.Fatal("apps are published before gatus has polled")
	}

	fe.set(1, nil, nil, srcDown("unreachable")) // the first poll failed
	m.rebuild()
	dot, srcs, ok := appDot(t, h)
	if !ok {
		t.Fatal("apps are still not published after the first poll failed")
	}
	if dot != nil {
		t.Errorf("dot = %v, want none: nothing is known", *dot)
	}
	wantGatusChip(t, "apps", srcs, false, "unreachable")
}

// ---- M1b: a new service is a change, whatever its Revision ----

// A Reload stops the instance and starts a new one, which is a different service. Even with the
// same Revision the Manager rebuilds, because it compares the service as well (the Revision is
// process-global in the plugin, so equal values do not happen there; the Manager does not
// rely on it).
func TestANewGatusServiceWithAnEqualRevisionRebuildsApps(t *testing.T) {
	var g gatusHolder
	before, after := &fakeEndpoints{}, &fakeEndpoints{}
	before.set(5, map[string]model.Health{grafanaHost: model.HealthOK}, nil, srcOK)
	after.set(5, map[string]model.Health{grafanaHost: model.HealthCrit}, nil, srcOK)
	g.cur.Store(before)
	m, h := manualManager(t, g.services)
	m.markAll()
	m.rebuild()
	if dot, _, _ := appDot(t, h); !dotIs(dot, true) {
		t.Fatalf("setup: dot = %v, want healthy", dot)
	}

	g.cur.Store(after)
	m.rebuild()
	if dot, _, _ := appDot(t, h); !dotIs(dot, false) {
		t.Errorf("dot after the service was replaced = %v, want unhealthy", dot)
	}

	// The plugin going away is a change too: the dots go, and the chip says not configured.
	g.cur.Store(nil)
	m.rebuild()
	dot, srcs, _ := appDot(t, h)
	if dot != nil {
		t.Errorf("dot = %v after gatus stopped, want none", *dot)
	}
	wantGatusChip(t, "apps", srcs, false, upstream.CategoryNotConfigured)
}

// ---- gatus not running ----

// Without a running gatus plugin (disabled, unconfigured, failed to start) apps still publish,
// with the gatus chip reading "not configured"; when the plugin starts, the dots and the chip
// follow without anything else changing.
func TestAppsPublishWithGatusNotRunning(t *testing.T) {
	var g gatusHolder
	m, h := manualManager(t, g.services)
	m.markAll()
	m.rebuild()

	dot, srcs, ok := appDot(t, h)
	if !ok || dot != nil {
		t.Fatalf("apps published=%v dot=%v, want published with no dot", ok, dot)
	}
	wantGatusChip(t, "apps", srcs, false, "not_configured")
	if len(srcs) != 2 {
		t.Errorf("apps sources %+v: one chip for gatus, none twice", srcs)
	}

	fe := &fakeEndpoints{}
	fe.set(1, map[string]model.Health{grafanaHost: model.HealthOK}, nil, srcOK)
	g.cur.Store(fe)
	m.rebuild()
	dot, srcs, _ = appDot(t, h)
	if !dotIs(dot, true) {
		t.Errorf("dot after gatus started = %v, want healthy", dot)
	}
	wantGatusChip(t, "apps", srcs, true, "")
}

// ---- the real plugin, through a real supervisor ----

// gatusUpstream is a Gatus API that answers with whatever it was told to.
type gatusUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	body string
}

type gatusCheck struct {
	group, name, host string
	ok                bool
}

func statusesJSON(checks ...gatusCheck) string {
	type result struct {
		Hostname  string    `json:"hostname"`
		Duration  int64     `json:"duration"`
		Success   bool      `json:"success"`
		Timestamp time.Time `json:"timestamp"`
	}
	type status struct {
		Name    string   `json:"name"`
		Group   string   `json:"group"`
		Key     string   `json:"key"`
		Results []result `json:"results"`
	}
	out := []status{}
	for _, c := range checks {
		out = append(out, status{Name: c.name, Group: c.group, Key: strings.ToLower(c.group + "_" + c.name), Results: []result{{
			Hostname: c.host, Duration: 1e6, Success: c.ok, Timestamp: time.Now().Add(-time.Minute).UTC(),
		}}})
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func newGatusUpstream(t *testing.T, checks ...gatusCheck) *gatusUpstream {
	t.Helper()
	g := &gatusUpstream{body: statusesJSON(checks...)}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		body := g.body
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *gatusUpstream) answer(checks ...gatusCheck) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.body = statusesJSON(checks...)
}

// gatusRig is a Manager over a real supervisor that runs the real gatus plugin next to the
// shims, wired as cmd/dash wires it (Services, Contributors).
type gatusRig struct {
	m   *Manager
	sup *plugin.Supervisor
	set *plugintest.Settings
	h   *hub.Hub
}

func gatusRow(url string, critical ...string) plugin.InstanceConfig {
	c := pluginconf.DefaultGatus()
	c.URL, c.CriticalGroups = url, critical
	return plugin.InstanceConfig{Name: "gatus", Type: "gatus", Config: &c}
}

func newGatusRig(t *testing.T, row plugin.InstanceConfig) *gatusRig {
	t.Helper()
	r := &gatusRig{h: hub.New(), set: &plugintest.Settings{}}
	r.set.Set(row)
	r.sup = plugin.NewSupervisor(plugin.Options{Publisher: r.h, Settings: r.set, StatusEvery: 10 * time.Millisecond})
	r.h.SetOrder(r.sup.TopicOrder)
	cfg := config.Defaults()
	r.m = New(Deps{
		Cfg: cfg, Cluster: routeCluster(), Hosts: r.sup, Contributors: r.sup.Contributors,
		Services: r.sup.Service, Secrets: secret.NewRegistry(),
	})
	if err := r.sup.Add(append(r.m.Plugins(), gatus.New)...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.sup.Shutdown(context.Background()) })
	r.m.Run(ctx)
	return r
}

func (r *gatusRig) endpoints(t *testing.T) svc.EndpointHealth {
	t.Helper()
	v, ok := r.sup.Service("gatus")
	if !ok {
		t.Fatal("the gatus plugin exports no service")
	}
	return v.(svc.EndpointHealth)
}

func (r *gatusRig) overview(t *testing.T) (model.OverviewData, []model.SourceStatus, bool) {
	t.Helper()
	return decodeTopic[model.OverviewData](t, r.h, model.TopicOverview)
}

// What the viewer reads of the real gatus plugin: its topic under its own name with a ref on every
// endpoint, the overview item with the same ref (must-hold b), the apps' dots and the gatus chip
// on both. Then a Reload, which builds a new service: the Manager follows it without any other
// change, and the Revision never goes back.
func TestTheGatusPluginDrivesAppsAndTheOverview(t *testing.T) {
	up := newGatusUpstream(t,
		gatusCheck{"apps", "Grafana", grafanaHost, false},
		gatusCheck{"infrastructure", "UniFi", "unifi.example.com", false},
		gatusCheck{"apps", "Backup SSH", "backup.example.com", true})
	r := newGatusRig(t, gatusRow(up.URL, "infrastructure"))

	eventually(t, "apps, vms and the overview from the gatus data", func() bool {
		dot, _, ok := appDotQuiet(r.h)
		if !ok || dot == nil {
			return false
		}
		d, _, ok := r.overview(t)
		_, has := alertItem(d, "gatus:apps_grafana")
		return ok && has
	})

	dot, srcs, _ := appDot(t, r.h)
	if !dotIs(dot, false) {
		t.Errorf("the dot of the failing grafana check = %v, want unhealthy", dot)
	}
	wantGatusChip(t, "apps", srcs, true, "")

	// The topic, as a viewer's stream carries it.
	status, _, ok := decodeTopic[model.StatusData](t, r.h, model.TopicStatus)
	env, _ := latest(r.h, model.TopicStatus)
	if !ok || env.Topic != "gatus.status" || len(status.Endpoints) != 3 {
		t.Fatalf("gatus.status = %+v (envelope %q)", status, env.Topic)
	}
	for _, e := range status.Endpoints {
		if e.Ref != "gatus:Endpoint/"+e.Key || e.Key == "" {
			t.Errorf("endpoint %s: ref %q key %q", e.Name, e.Ref, e.Key)
		}
	}
	if len(status.CriticalGroups) != 1 || status.CriticalGroups[0] != "infrastructure" {
		t.Errorf("criticalGroups = %v", status.CriticalGroups)
	}

	// The overview: the failing checks carry the same refs, the severity follows criticalGroups
	// (infrastructure is critical, apps warn), and the gatus chip is the plugin's.
	d, osrcs, _ := r.overview(t)
	graf, _ := alertItem(d, "gatus:apps_grafana")
	unifi, has := alertItem(d, "gatus:infrastructure_unifi")
	if !has {
		t.Fatal("the critical-group failure is not on the overview")
	}
	if graf.Ref != "gatus:Endpoint/apps_grafana" || graf.Severity != model.SeverityWarn || graf.Plugin != "gatus" || graf.Route != "/status" {
		t.Errorf("grafana item = %+v", graf)
	}
	if unifi.Ref != "gatus:Endpoint/infrastructure_unifi" || unifi.Severity != model.SeverityCrit {
		t.Errorf("unifi item = %+v", unifi)
	}
	if _, has := hasSource(osrcs, "gatus"); !has {
		t.Errorf("overview sources %+v lack the gatus chip", osrcs)
	}

	// Reload: a new instance, a new service. Gatus now says grafana is fine; the old service would
	// not poll again for 30 s, so only the new one can show it.
	oldSvc, oldRev := r.endpoints(t), r.endpoints(t).Revision()
	up.answer(
		gatusCheck{"apps", "Grafana", grafanaHost, true},
		gatusCheck{"infrastructure", "UniFi", "unifi.example.com", false},
		gatusCheck{"apps", "Backup SSH", "backup.example.com", true})
	r.set.Set(gatusRow(up.URL, "apps")) // a changed section restarts the instance
	r.sup.Reload()
	eventually(t, "the Manager to follow the new service", func() bool {
		dot, _, ok := appDotQuiet(r.h)
		return ok && dot != nil && *dot
	})
	if now := r.endpoints(t); now == oldSvc || now.Revision() <= oldRev {
		t.Errorf("after the reload: same service %v, revision %d after %d", now == oldSvc, now.Revision(), oldRev)
	}
}

// Gatus is configured but down at start: the first poll fails, the plugin's Revision goes above
// zero, the apps gate opens and apps publish with a failing gatus source and no dots (M1a, with
// the real plugin).
func TestGatusDownAtStartStillPublishesApps(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	r := newGatusRig(t, gatusRow(url))

	eventually(t, "apps with a failing gatus source", func() bool {
		_, srcs, ok := appDotQuiet(r.h)
		g, has := hasSource(srcs, "gatus")
		return ok && has && !g.OK && g.Error == upstream.CategoryUnreachable
	})
	if rev := r.endpoints(t).Revision(); rev == 0 {
		t.Error("Revision is still 0 after the first poll failed")
	}
	if dot, _, _ := appDot(t, r.h); dot != nil {
		t.Errorf("dot = %v, want none while gatus is unreachable", *dot)
	}
}

// appDotQuiet is appDot for a polling loop: no test failure, just "not yet".
func appDotQuiet(h *hub.Hub) (dot *bool, srcs []model.SourceStatus, ok bool) {
	env, found := latest(h, model.TopicApps)
	if !found {
		return nil, nil, false
	}
	var d model.AppsData
	if json.Unmarshal(env.Data, &d) != nil || len(d.Apps) != 1 {
		return nil, nil, false
	}
	return d.Apps[0].Healthy, env.Sources, true
}
