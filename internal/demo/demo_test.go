package demo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/topics"
)

// published is a hub the demo has published to, its shims all running.
func published(t *testing.T) *hub.Hub {
	t.Helper()
	h := hub.New()
	s := New(Config(config.Defaults()))
	s.Plugins(plugintest.AllRunning(h, topics.LegacyManifests()...))
	s.publish()
	return h
}

func TestPublishesEveryTopic(t *testing.T) {
	h := published(t)
	for _, topic := range []model.Topic{
		model.TopicOverview, model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux,
		model.TopicStorage, model.TopicApps,
	} {
		ev, ok := h.Latest("", topic)
		if !ok {
			t.Fatalf("topic %s not published", topic)
		}
		var env model.Envelope
		if err := json.Unmarshal(ev.Data, &env); err != nil || len(env.Data) < 3 {
			t.Fatalf("topic %s: bad envelope (%v)", topic, err)
		}
	}
}

func TestFixturesStayFake(t *testing.T) {
	h := published(t)
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	for _, ev := range snap {
		body := strings.ToLower(string(ev.Data))
		for _, bad := range []string{"techdufus", "home.", "10.0.", "192.168."} {
			if strings.Contains(body, bad) {
				t.Errorf("topic %s contains %q", ev.Topic, bad)
			}
		}
	}
}

func TestDetailsResolve(t *testing.T) {
	s := New(Config(config.Defaults()))
	ctx := context.Background()
	w := s.build()
	p := w.cluster.Pods[0]
	if _, err := s.PodDetail(ctx, p.Namespace, p.Name); err != nil {
		t.Fatalf("pod detail: %v", err)
	}
	if _, err := s.PodDetail(ctx, "nope", "nope"); !errors.Is(err, topics.ErrNotFound) {
		t.Fatalf("missing pod: %v", err)
	}
	if _, err := s.VMDetail(ctx, w.vms.VMs[0].VMID); err != nil {
		t.Fatalf("vm detail: %v", err)
	}
	if _, ok := s.NodeIP(w.cluster.Nodes[0].Name); !ok {
		t.Fatal("node ip not found")
	}
}

func TestWrapAnswersActionsAndSignsIn(t *testing.T) {
	var user string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user = r.Header.Get("X-Forwarded-User")
		w.WriteHeader(http.StatusTeapot)
	})
	rec := httptest.NewRecorder()
	Wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/actions/restart", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart: got %d, want 202", rec.Code)
	}
	// The silence routes are gone: nothing answers for them here any more, the request goes on.
	for _, c := range []struct{ method, path string }{{http.MethodPost, "/api/silences"}, {http.MethodDelete, "/api/silences/abc"}} {
		rec = httptest.NewRecorder()
		Wrap(next).ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s: got %d, want the request to reach the API", c.method, c.path, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	Wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != http.StatusTeapot || user != "demo" {
		t.Fatalf("passthrough: got %d user %q", rec.Code, user)
	}
}

// demoSupervisor is a real supervisor over the demo's shims and a real hub, as main wires them.
func demoSupervisor(t *testing.T, set *plugintest.Settings) (*Source, *plugin.Supervisor, *hub.Hub) {
	t.Helper()
	if set == nil {
		set = &plugintest.Settings{}
	}
	h := hub.New()
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond})
	s := New(Config(config.Defaults()))
	if err := sup.Add(s.Plugins(sup)...); err != nil {
		t.Fatal(err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	return s, sup, h
}

func TestDemoPublishesLegacyTopicsAndCorePlugins(t *testing.T) {
	s, _, h := demoSupervisor(t, nil)
	s.publish()
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	var got []string
	for _, ev := range snap {
		got = append(got, ev.Topic)
	}
	// core.plugins first, then the overview, then the integrations' topics under the names
	// they keep until their plugin migrates (the alertmanager twin is registered by cmd/dash).
	want := []string{"core.plugins", "core.overview", "vms", "host", "cluster", "storage", "apps", "flux"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("topics = %v, want %v", got, want)
	}
	var env model.Envelope
	if err := json.Unmarshal(snap[0].Data, &env); err != nil {
		t.Fatal(err)
	}
	var pd model.PluginsData
	if err := json.Unmarshal(env.Data, &pd); err != nil {
		t.Fatal(err)
	}
	if len(pd.Plugins) != 4 { // the shims; the real plugins' twins (prometheus, alertmanager, gatus, victorialogs, grafana, headlamp, launcher) are registered by cmd/dash
		t.Fatalf("core.plugins lists %d plugins, want 4", len(pd.Plugins))
	}
}

func TestDemoPluginsAreHealthy(t *testing.T) {
	_, sup, _ := demoSupervisor(t, nil)
	infos := sup.Infos()
	if len(infos) != 4 {
		t.Fatalf("%d plugins, want 4", len(infos))
	}
	for _, in := range infos {
		if in.State != model.PluginHealthy || in.Message != "" {
			t.Errorf("%s: %s %q, want healthy with no message", in.ID, in.State, in.Message)
		}
	}
	if !sup.Ready() {
		t.Error("the demo is not ready")
	}
}

// The overview's alerts tile and source chip are the alertmanager plugin's: the demo adds them
// while its twin runs and leaves both out when the plugin does not run.
func TestDemoOverviewHasTheAlertmanagerKPIOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) (model.OverviewData, []model.SourceStatus) {
		s := New(Config(config.Defaults()))
		s.Plugins(hp)
		s.publish()
		ev, ok := h.Latest("", model.TopicOverview)
		if !ok {
			t.Fatal("no overview")
		}
		var env model.Envelope
		var d model.OverviewData
		if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
			t.Fatalf("bad overview envelope (%v)", err)
		}
		return d, env.Sources
	}
	hasChip := func(srcs []model.SourceStatus) bool {
		for _, s := range srcs {
			if s.Name == "alertmanager" {
				return true
			}
		}
		return false
	}

	h := hub.New()
	d, srcs := overview(plugintest.AllRunning(h, append(topics.LegacyManifests(), alertmanager.New().Manifest())...), h)
	if len(d.KPIList) != 1 || d.KPIList[0].Plugin != "alertmanager" || d.KPIList[0].Key != "firing" || d.KPIList[0].Value != 2 || !hasChip(srcs) {
		t.Errorf("with alertmanager running: kpiList %+v, sources %+v", d.KPIList, srcs)
	}
	h = hub.New()
	d, srcs = overview(plugintest.Without(plugintest.AllRunning(h, topics.LegacyManifests()...), "alertmanager"), h)
	if len(d.KPIList) != 0 || hasChip(srcs) {
		t.Errorf("without alertmanager: kpiList %+v, sources %+v", d.KPIList, srcs)
	}
}

// A plugin disabled in demo mode publishes nothing: its topic is absent and its page goes.
func TestDemoDisabledPluginPublishesNothing(t *testing.T) {
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &config.FluxConfig{}})
	s, _, h := demoSupervisor(t, set)
	s.publish()
	if _, ok := h.Latest("", model.TopicFlux); ok {
		t.Error("the flux topic is published although flux is disabled")
	}
	if _, ok := h.Latest("", model.TopicCluster); !ok {
		t.Error("the cluster topic is missing")
	}
}

// The demo plugins, unlike the production shims, follow a config reload: disabling one removes
// its topic and lists it as disabled, enabling it again brings the topic back on the next tick.
func TestDemoPluginSwitchesOffAndOnWithoutRestart(t *testing.T) {
	set := &plugintest.Settings{}
	s, sup, h := demoSupervisor(t, set)
	s.publish()
	if _, ok := h.Latest("", model.TopicFlux); !ok {
		t.Fatal("the flux topic is missing before flux is disabled")
	}
	state := func() model.PluginState {
		for _, in := range sup.Infos() {
			if in.ID == "flux" {
				return in.State
			}
		}
		t.Fatal("flux is not listed")
		return ""
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	off := false
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &config.FluxConfig{}})
	sup.Reload()
	waitFor("flux to be disabled", func() bool { return state() == model.PluginDisabled })
	waitFor("the flux topic to go", func() bool { _, ok := h.Latest("", model.TopicFlux); return !ok })
	s.publish()
	if _, ok := h.Latest("", model.TopicFlux); ok {
		t.Error("the flux topic is published although flux is disabled")
	}
	if _, ok := h.Latest("", model.TopicCluster); !ok {
		t.Error("another plugin's topic went with it")
	}

	on := true
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &on, Config: &config.FluxConfig{}})
	sup.Reload()
	waitFor("flux to run again", func() bool { return state() == model.PluginHealthy })
	s.publish() // what the next tick does
	if _, ok := h.Latest("", model.TopicFlux); !ok {
		t.Error("the flux topic did not come back after flux was enabled")
	}
}

// Demo does not even ask a plugin that does not run to publish (each ask would log a WARN on
// every tick), and still publishes the rest and the core topics.
func TestDemoSkipsOwnersThatAreNotRunning(t *testing.T) {
	h := hub.New()
	hp := plugintest.Without(plugintest.AllRunning(h, topics.LegacyManifests()...), "flux")
	s := New(Config(config.Defaults()))
	s.Plugins(hp)
	s.publish()
	if n := hp.Publishes("flux"); n != 0 {
		t.Errorf("demo called a stopped flux's Host %d times", n)
	}
	if hp.Publishes("kubernetes") == 0 {
		t.Error("demo did not publish the topics of a running plugin")
	}
	if _, ok := h.Latest("", model.TopicOverview); !ok {
		t.Error("the overview is missing")
	}
}
