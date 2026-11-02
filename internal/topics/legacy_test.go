package topics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/upstream"
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

// fakeCluster is an empty cluster whose informers sync at once.
func fakeCluster() *kube.Cluster {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	return kube.NewFromClients(kubefake.NewClientset(), dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
}

// testManager is a Manager over an empty cluster and the given hosts; the Proxmox upstream is the
// one cfg configures, and no plugin exports a service.
func testManager(cfg config.Config, hosts plugin.HostProvider) *Manager {
	return testManagerWith(cfg, hosts, nil)
}

// testManagerWith is testManager with plugins that contribute to the overview.
func testManagerWith(cfg config.Config, hosts plugin.HostProvider, contributors func() []plugin.Contributor) *Manager {
	return New(Deps{
		Cfg: cfg, Cluster: fakeCluster(), Hosts: hosts, Contributors: contributors,
	})
}

// promServices is Deps.Services as the supervisor gives it while the prometheus plugin runs
// with the client p as its service; no other plugin exports anything.
func promServices(p *upstream.Prom) func(plugin.ID) (any, bool) {
	return func(id plugin.ID) (any, bool) { return p, id == "prometheus" }
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

var legacyTopics = []model.Topic{
	model.TopicCluster, model.TopicFlux, model.TopicStorage, model.TopicApps,
}

// The Manager publishes through the Host of each topic's owner, and the SSE names stay the ones
// the SPA reads: "cluster", …. The payloads are what the Manager builds.
func TestManagerPublishesLegacyTopicsThroughHosts(t *testing.T) {
	h := hub.New()
	m := testManager(config.Defaults(), plugintest.AllRunning(h, LegacyManifests()...))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)

	eventually(t, "every topic", func() bool {
		for _, topic := range append([]model.Topic{model.TopicOverview}, legacyTopics...) {
			if _, ok := h.Latest("", topic); !ok {
				return false
			}
		}
		return true
	})
	snap, _, stop := h.Subscribe("", nil)
	defer stop()
	var got []string
	for _, ev := range snap {
		got = append(got, ev.Topic)
	}
	slices.Sort(got)
	want := []string{"apps", "cluster", "core.overview", "flux", "storage"}
	if !slices.Equal(got, want) {
		t.Fatalf("published %v, want exactly %v", got, want)
	}

	builds := map[model.Topic]any{
		model.TopicCluster: m.buildCluster(m.now()),
		model.TopicFlux:    m.buildFlux(), model.TopicStorage: m.buildStorage(m.now()),
		model.TopicApps: m.buildApps(),
	}
	for topic, data := range builds {
		env, _ := latest(h, topic)
		wantJSON, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		if string(env.Data) != string(wantJSON) {
			t.Errorf("%s payload differs from the Manager's build:\n got %s\nwant %s", topic, env.Data, wantJSON)
		}
		if env.Topic != string(topic) {
			t.Errorf("%s envelope is named %q", topic, env.Topic)
		}
	}
}

// promRecorder is a Prometheus that answers every query with an empty vector and counts the
// queries by expression.
type promRecorder struct {
	mu sync.Mutex
	n  map[string]int
}

func (p *promRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.n == nil {
		p.n = map[string]int{}
	}
	p.n[r.FormValue("query")]++
	p.mu.Unlock()
	_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
}

func (p *promRecorder) count(expr string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n[expr]
}

// The same mechanism on the kubernetes shim, the only reader of the usage and storage pollers:
// with it running the pollers query and the topics come from its Host; with it off nothing is
// polled, nothing is published for it, and the overview does not wait.
func TestDisabledKubernetesPublishesNothingAndItsPollersAreSkipped(t *testing.T) {
	rec := &promRecorder{}
	prom := httptest.NewServer(rec)
	defer prom.Close()

	run := func(off ...plugin.ID) (*hub.Hub, *Manager, *plugintest.Partial) {
		h := hub.New()
		hp := plugintest.Without(plugintest.AllRunning(h, LegacyManifests()...), off...)
		cfg := config.Defaults()
		m := New(Deps{
			Cfg: cfg, Cluster: fakeCluster(), Hosts: hp,
			Services: promServices(upstream.NewProm(prom.URL, nil)),
		})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		m.Run(ctx)
		return h, m, hp
	}

	// Kubernetes on: both pollers query and the cluster topic is published.
	h, _, hp := run()
	eventually(t, "the cluster topic", func() bool { _, ok := h.Latest("", model.TopicCluster); return ok })
	eventually(t, "the usage and storage queries", func() bool {
		return rec.count(queries.PodCPU) > 0 && rec.count(queries.PVCUsed) > 0
	})
	if hp.Publishes("kubernetes") == 0 {
		t.Fatal("the recording host saw no publish for a running kubernetes")
	}

	// Off: not polled, nothing published for it, and the overview still comes.
	rec.mu.Lock()
	rec.n = map[string]int{}
	rec.mu.Unlock()
	h, m, hp := run("kubernetes")
	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	eventually(t, "the usage and storage pollers' first round", func() bool { return m.pollDone("usage", "storage") })
	if n := rec.count(queries.PodCPU) + rec.count(queries.PVCUsed); n != 0 {
		t.Errorf("a disabled kubernetes was polled %d times", n)
	}
	if n := hp.Publishes("kubernetes"); n != 0 {
		t.Errorf("the Manager called the disabled kubernetes' Host %d times", n)
	}
	if _, ok := h.Latest("", model.TopicCluster); ok {
		t.Error("the cluster topic is published although kubernetes does not run")
	}
}

// The source keys of the usage and storage pollers on the kubernetes shim: with kubernetes off
// they are left out of every source list and the shims' status (they would read "not polled yet"
// forever), while the informers' key stays; with it on, an unpolled key reads "not polled yet".
func TestSkippedKubernetesPollersLeaveTheirSourcesOut(t *testing.T) {
	at := time.Unix(100, 0)
	okSrc := srcState{ok: true, at: at}
	keys := []string{"kubernetes", "cluster:victoriametrics", "storage:victoriametrics"}

	hp := plugintest.Without(plugintest.AllRunning(hub.New(), LegacyManifests()...), "kubernetes")
	m := New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster(), Hosts: hp})
	got := m.sources(keys...)
	if len(got) != 1 || got[0].Name != "kubernetes" {
		t.Errorf("sources of a disabled kubernetes = %+v, want only the informers", got)
	}
	for _, id := range []plugin.ID{"kubernetes", "flux"} {
		st := m.legacyStatus()
		if st.State != plugin.StateStarting {
			t.Errorf("%s before the informers sync = %s, want starting", id, st.State)
		}
		for _, s := range st.Sources {
			if s.Error == "not polled yet" {
				t.Errorf("%s: source %q is reported as not polled", id, s.Name)
			}
		}
	}

	// A key of a running poller still counts: not polled yet, then its state.
	hp = plugintest.Without(plugintest.AllRunning(hub.New(), LegacyManifests()...))
	m = New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster(), Hosts: hp})
	got = m.sources(keys[1:]...)
	if len(got) != 1 || got[0].OK || got[0].Error != "not polled yet" {
		t.Errorf("sources before the first poll = %+v, want one not polled yet", got)
	}
	m.srcs["cluster:victoriametrics"], m.srcs["storage:victoriametrics"] = okSrc, okSrc
	got = m.sources(keys[1:]...)
	if len(got) != 1 || !got[0].OK {
		t.Errorf("sources after the first poll = %+v, want one ok", got)
	}
}

// The shims, the supervisor, the Manager and the hub together: an explicitly disabled plugin
// shows as disabled in core.plugins and its topic never reaches a viewer, while the others run.
func TestExplicitDisableRemovesShimTopics(t *testing.T) {
	h := hub.New()
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &config.FluxConfig{}})
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond})
	h.SetOrder(sup.TopicOrder)
	m := testManager(config.Defaults(), sup)
	if err := sup.Add(m.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	m.Run(ctx)

	eventually(t, "the cluster topic", func() bool { _, ok := h.Latest("", model.TopicCluster); return ok })
	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	if _, ok := h.Latest("", model.TopicFlux); ok {
		t.Error("flux is disabled but its topic is still served")
	}
	env, ok := latest(h, model.TopicPlugins)
	if !ok {
		t.Fatal("core.plugins is not published")
	}
	var pd model.PluginsData
	if err := json.Unmarshal(env.Data, &pd); err != nil {
		t.Fatal(err)
	}
	states := map[string]model.PluginInfo{}
	for _, in := range pd.Plugins {
		states[in.ID] = in
	}
	if states["flux"].State != model.PluginDisabled || !slices.Equal(states["flux"].Topics, []string{"flux"}) {
		t.Errorf("flux = %+v, want disabled with the topic flux", states["flux"])
	}
	if states["kubernetes"].State == model.PluginDisabled || len(pd.Plugins) != 2 {
		t.Errorf("kubernetes = %s of %d plugins", states["kubernetes"].State, len(pd.Plugins))
	}
}

// The explicit-disable path on the kubernetes shim, the owner of the usage and storage pollers:
// disabled in the settings it shows as disabled in core.plugins, its topics never reach a viewer,
// and its pollers make no Prometheus request.
func TestExplicitDisableOfKubernetesStopsItsPollers(t *testing.T) {
	rec := &promRecorder{}
	prom := httptest.NewServer(rec)
	defer prom.Close()

	h := hub.New()
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "kubernetes", Enabled: &off, Config: &config.KubernetesConfig{}})
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond})
	h.SetOrder(sup.TopicOrder)
	cfg := config.Defaults()
	m := New(Deps{
		Cfg: cfg, Cluster: fakeCluster(), Hosts: sup,
		Services: promServices(upstream.NewProm(prom.URL, nil)),
	})
	if err := sup.Add(m.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	m.Run(ctx)

	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	eventually(t, "the usage and storage pollers' first round", func() bool { return m.pollDone("usage", "storage") })
	if n := rec.count(queries.PodCPU) + rec.count(queries.PVCUsed); n != 0 {
		t.Errorf("a disabled kubernetes was polled %d times", n)
	}
	for _, topic := range []model.Topic{model.TopicCluster, model.TopicStorage, model.TopicApps} {
		if _, ok := h.Latest("", topic); ok {
			t.Errorf("kubernetes is disabled but its topic %s is still served", topic)
		}
	}
	env, ok := latest(h, model.TopicPlugins)
	if !ok {
		t.Fatal("core.plugins is not published")
	}
	var pd model.PluginsData
	if err := json.Unmarshal(env.Data, &pd); err != nil {
		t.Fatal(err)
	}
	for _, in := range pd.Plugins {
		if in.ID == "kubernetes" && in.State != model.PluginDisabled {
			t.Errorf("kubernetes = %s, want disabled", in.State)
		}
	}
}

// Every shim's Config returns the type of its section in config.PluginsConfig, so the two
// cannot drift (the schema of a plugin is reflected from Config).
func TestLegacyManifestsMatchConfigSections(t *testing.T) {
	sections := map[string]reflect.Type{}
	pt := reflect.TypeFor[config.PluginsConfig]()
	for i := range pt.NumField() {
		name, _, _ := strings.Cut(pt.Field(i).Tag.Get("json"), ",")
		sections[name] = pt.Field(i).Type
	}
	// The sections of the integrations that moved out of the Manager have no shim: each runs as
	// the plugin of internal/plugins/<id>.
	for _, moved := range []string{"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "grafana", "headlamp", "launcher"} {
		delete(sections, moved)
	}
	ms := LegacyManifests()
	if len(ms) != len(sections) {
		t.Fatalf("%d manifests for %d sections", len(ms), len(sections))
	}
	for _, man := range ms {
		typ, ok := sections[string(man.ID)]
		if !ok {
			t.Errorf("%s has no plugins.%s section", man.ID, man.ID)
			continue
		}
		if got := reflect.TypeOf(man.Config()); got != reflect.PointerTo(typ) {
			t.Errorf("%s: Config returns %v, want *%v", man.ID, got, typ)
		}
		if man.Requires != nil {
			t.Errorf("%s declares Requires %v; a shim must not block on a missing source", man.ID, man.Requires)
		}
	}
	// The same ids and legacy topic names are valid to the supervisor, which checks the rest.
	sup := plugin.NewSupervisor(plugin.Options{})
	if err := sup.Add((&Manager{}).Plugins()...); err != nil {
		t.Fatalf("the shims are not a valid plugin set: %v", err)
	}
}

func TestLegacyShimsAreRestartOnly(t *testing.T) {
	for _, f := range (&Manager{}).Plugins() {
		if _, ok := f().(plugin.RestartOnly); !ok {
			t.Errorf("%s can be reconfigured in-process; the Manager cannot", f().Manifest().ID)
		}
	}
}

// The status of a shim follows the informers: starting until they sync, healthy after.
func TestLegacyStatusFollowsTheInformers(t *testing.T) {
	m := New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster()})
	if got := m.legacyStatus(); got.State != plugin.StateStarting || got.Message != "informers syncing" {
		t.Errorf("before the informers sync: %s %q", got.State, got.Message)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.d.Cluster.Start(ctx)
	eventually(t, "the informers to sync", func() bool { return m.d.Cluster.HasSynced() })
	if got := m.legacyStatus(); got.State != plugin.StateHealthy {
		t.Errorf("after sync: %s", got.State)
	}
	// A viewer never reads a URL or host out of a plugin's status: the shims name no upstream.
	for _, s := range m.legacyStatus().Sources {
		if s.Name != "kubernetes" {
			t.Errorf("source %q on a shim's status", s.Name)
		}
	}
}
