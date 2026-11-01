package topics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/secret"
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
		Cfg: cfg, Cluster: fakeCluster(), Hosts: hosts, Contributors: contributors, Links: links.New(cfg),
		PVE: upstream.NewProxmox(cfg.ProxmoxURL, cfg.ProxmoxNode, cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, nil),
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
	model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux, model.TopicStorage,
	model.TopicApps,
}

// The Manager publishes through the Host of each topic's owner, and the SSE names stay the ones
// the SPA reads: "vms", "host", …, not "proxmox.vms". The payloads are what the Manager builds.
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
	want := []string{"apps", "cluster", "core.overview", "flux", "host", "storage", "vms"}
	if !slices.Equal(got, want) {
		t.Fatalf("published %v, want exactly %v", got, want)
	}

	builds := map[model.Topic]any{
		model.TopicHost: m.buildHost(), model.TopicVMs: m.buildVMs(), model.TopicCluster: m.buildCluster(m.now()),
		model.TopicFlux: m.buildFlux(), model.TopicStorage: m.buildStorage(m.now()),
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

// A plugin that does not run publishes nothing, the overview does not wait for it, and a poller
// whose every reader is off makes no request. The recording host proves the Manager never even
// calls the disabled owner's Host (a Host that drops the call would hide a missing skip).
func TestDisabledOwnerPublishesNothingAndItsPollerIsSkipped(t *testing.T) {
	var requests atomic.Int32
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer pve.Close()

	run := func(off ...plugin.ID) (*hub.Hub, *Manager, *plugintest.Partial) {
		h := hub.New()
		hp := plugintest.Without(plugintest.AllRunning(h, LegacyManifests()...), off...)
		cfg := config.Defaults()
		cfg.ProxmoxURL, cfg.ProxmoxNode, cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret = pve.URL, "pve", "u@pve!t", secret.New("s3cret", "")
		m := testManager(cfg, hp)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		m.Run(ctx)
		return h, m, hp
	}

	// Proxmox on: its poller runs and its topic is published.
	h, _, hp := run()
	eventually(t, "the vms topic", func() bool { _, ok := h.Latest("", model.TopicVMs); return ok })
	if requests.Load() == 0 {
		t.Fatal("proxmox is running but was not polled")
	}
	if hp.Publishes("proxmox") == 0 {
		t.Fatal("the recording host saw no publish for a running proxmox")
	}

	// Off: not polled, nothing published, and the overview still comes.
	requests.Store(0)
	h, m, hp := run("proxmox", "hardware") // the host poller reads Proxmox too
	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	eventually(t, "the vms and host pollers' first round", func() bool { return m.pollDone("vms", "host") })
	if n := requests.Load(); n != 0 {
		t.Errorf("a disabled proxmox was polled %d times", n)
	}
	if n := hp.Publishes("proxmox"); n != 0 {
		t.Errorf("the Manager called the disabled proxmox's Host %d times", n)
	}
	if _, ok := h.Latest("", model.TopicVMs); ok {
		t.Error("the vms topic is published although proxmox does not run")
	}
}

// A source whose poller is skipped (nothing that reads it runs) is not waited for: the shims
// that own other keys of it reach their real state instead of staying in "starting", and the
// overview's source list leaves it out.
func TestSkippedPollersDoNotStickShimsInStarting(t *testing.T) {
	at := time.Unix(100, 0)
	okSrc := srcState{ok: true, at: at}
	// Without hardware the host poller is skipped (host:proxmox never gets polled), so the
	// proxmox shim, which owns that key too, reaches its state from vms:proxmox alone.
	hp := plugintest.Without(plugintest.AllRunning(hub.New(), LegacyManifests()...), "hardware")
	m := New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster(), Hosts: hp})
	m.srcs["vms:proxmox"] = okSrc

	st := m.legacyStatus("proxmox")
	if st.State != plugin.StateHealthy || st.Message != "" {
		t.Errorf("proxmox = %s %q, want healthy", st.State, st.Message)
	}
	for _, s := range st.Sources {
		if s.Error == "not polled yet" {
			t.Errorf("source %q is still reported as not polled", s.Name)
		}
	}

	// Overview: only the polled source is listed; the skipped ones would read "not polled yet".
	hp = plugintest.Without(plugintest.AllRunning(hub.New(), LegacyManifests()...), "hardware", "proxmox")
	m = New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster(), Hosts: hp})
	m.srcs["cluster:victoriametrics"], m.srcs["storage:victoriametrics"] = okSrc, okSrc
	for _, s := range m.sources("host:victoriametrics", "vms:proxmox", "vms:victoriametrics", "cluster:victoriametrics") {
		if !s.OK {
			t.Errorf("overview source %q = %q, want only polled sources", s.Name, s.Error)
		}
	}

	// Nothing that reads the source runs at all: a message instead of a wait that never ends.
	if st := m.legacyStatus("proxmox"); st.State != plugin.StateUnavailable {
		t.Errorf("proxmox with every reader off = %s %q, want unavailable", st.State, st.Message)
	}

	// A key of a running poller still counts: not polled yet means starting.
	hp = plugintest.Without(plugintest.AllRunning(hub.New(), LegacyManifests()...))
	m = New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster(), Hosts: hp})
	if st := m.legacyStatus("proxmox"); st.State != plugin.StateStarting {
		t.Errorf("proxmox before its first poll = %s, want starting", st.State)
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
	if states["kubernetes"].State == model.PluginDisabled || len(pd.Plugins) != 5 {
		t.Errorf("kubernetes = %s of %d plugins", states["kubernetes"].State, len(pd.Plugins))
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
	for _, moved := range []string{"prometheus", "alertmanager", "gatus", "grafana", "headlamp", "launcher"} {
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

// Status mapping of design §1.9: all keys not configured -> unavailable, any not polled yet or
// informers unsynced -> starting, all ok -> healthy, some -> degraded, none -> error.
func TestLegacyStatusMapping(t *testing.T) {
	at := time.Unix(100, 0)
	okSrc := srcState{ok: true, at: at}
	bad := func(cat string) srcState { return srcState{err: cat, at: at} }
	off := bad(upstream.CategoryNotConfigured)

	tests := []struct {
		name  string
		id    plugin.ID
		srcs  map[string]srcState
		state plugin.State
		msg   string
	}{
		{"proxmox unset", "proxmox", map[string]srcState{"vms:proxmox": off, "host:proxmox": off}, plugin.StateUnavailable,
			"not configured: set plugins.proxmox.url, tokenID and tokenSecret"},
		{"proxmox half", "proxmox", map[string]srcState{"vms:proxmox": okSrc, "host:proxmox": bad("timeout")}, plugin.StateDegraded, "timeout"},
		{"proxmox one not polled", "proxmox", map[string]srcState{"vms:proxmox": okSrc}, plugin.StateStarting, "waiting for the first poll"},
		{"hardware ok", "hardware", map[string]srcState{"host:victoriametrics": okSrc}, plugin.StateHealthy, ""},
		{"hardware tls", "hardware", map[string]srcState{"host:victoriametrics": bad("tls")}, plugin.StateError, "tls"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster()})
			for k, v := range tc.srcs {
				m.srcs[k] = v
			}
			got := m.legacyStatus(tc.id)
			if got.State != tc.state || got.Message != tc.msg {
				t.Errorf("status = %s %q, want %s %q", got.State, got.Message, tc.state, tc.msg)
			}
		})
	}

	t.Run("informers", func(t *testing.T) {
		m := New(Deps{Cfg: config.Defaults(), Cluster: fakeCluster()})
		for _, id := range []plugin.ID{"kubernetes", "flux"} {
			if got := m.legacyStatus(id); got.State != plugin.StateStarting {
				t.Errorf("%s before the informers sync: %s", id, got.State)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m.d.Cluster.Start(ctx)
		eventually(t, "the informers to sync", func() bool { return m.d.Cluster.HasSynced() })
		for _, id := range []plugin.ID{"kubernetes", "flux"} {
			if got := m.legacyStatus(id); got.State != plugin.StateHealthy {
				t.Errorf("%s after sync: %s", id, got.State)
			}
		}
	})

	t.Run("no poller", func(t *testing.T) {
		cfg := config.Defaults()
		m := New(Deps{Cfg: cfg, Cluster: fakeCluster()})
		for _, id := range []plugin.ID{"victorialogs"} {
			got := m.legacyStatus(id)
			if got.State != plugin.StateUnavailable || !strings.HasPrefix(got.Message, "not configured: set plugins."+string(id)+".") {
				t.Errorf("%s unset: %s %q", id, got.State, got.Message)
			}
		}
		cfg.VLogsURL = "https://logs.example.com"
		m = New(Deps{Cfg: cfg, Cluster: fakeCluster()})
		for _, id := range []plugin.ID{"victorialogs"} {
			if got := m.legacyStatus(id); got.State != plugin.StateHealthy || got.Message != "" {
				t.Errorf("%s set: %s %q", id, got.State, got.Message)
			}
		}
	})
}

// A viewer must never read a URL or host out of a plugin's status: messages name a setting or
// an error category.
func TestLegacyStatusNeverNamesAnUpstream(t *testing.T) {
	cfg := config.Defaults()
	cfg.ProxmoxURL = "https://pve.internal.example.com:8006"
	m := New(Deps{Cfg: cfg, Cluster: fakeCluster()})
	m.setSource("vms:proxmox", &upstream.StatusError{Code: 500})
	m.setSource("host:proxmox", &upstream.StatusError{Code: 500})
	st := m.legacyStatus("proxmox")
	if st.State != plugin.StateError || strings.Contains(st.Message, "example.com") || st.Message != "http_500" {
		t.Errorf("status = %s %q", st.State, st.Message)
	}
}
