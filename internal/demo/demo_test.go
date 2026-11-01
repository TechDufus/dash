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
	"github.com/TechDufus/dash/internal/topics"
)

// published is a hub the demo has published to, its shims all running.
func published(t *testing.T) *hub.Hub {
	t.Helper()
	h := hub.New()
	s := New(Config(config.Defaults()))
	s.Plugins(plugintest.AllRunning(h, topics.LegacyManifests()...))
	s.publish(h)
	return h
}

func TestPublishesEveryTopic(t *testing.T) {
	h := published(t)
	for _, topic := range []model.Topic{
		model.TopicOverview, model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux,
		model.TopicStorage, model.TopicAlerts, model.TopicStatus, model.TopicApps,
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
	end := time.Now()
	lines, err := s.Range(ctx, `ipmi_fan_speed_rpm{nodename="pve-demo"}`, end.Add(-time.Hour), end, 30*time.Second)
	if err != nil || len(lines) != 6 || len(lines[0].Points) < 100 {
		t.Fatalf("fan series: %d lines, err %v", len(lines), err)
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
	s.publish(h)
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	var got []string
	for _, ev := range snap {
		got = append(got, ev.Topic)
	}
	// core.plugins first, then the overview, then the integrations' topics under the names
	// they keep until their plugin migrates.
	want := []string{"core.plugins", "overview", "alerts", "status", "vms", "host", "cluster", "storage", "apps", "flux"}
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
	if len(pd.Plugins) != 11 {
		t.Fatalf("core.plugins lists %d plugins, want 11", len(pd.Plugins))
	}
}

func TestDemoPluginsAreHealthy(t *testing.T) {
	_, sup, _ := demoSupervisor(t, nil)
	infos := sup.Infos()
	if len(infos) != 11 {
		t.Fatalf("%d plugins, want 11", len(infos))
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

// A plugin disabled in demo mode publishes nothing: its topic is absent and its page goes.
func TestDemoDisabledPluginPublishesNothing(t *testing.T) {
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "hardware", Enabled: &off, Config: &config.HardwareConfig{}})
	s, _, h := demoSupervisor(t, set)
	s.publish(h)
	if _, ok := h.Latest("", model.TopicHost); ok {
		t.Error("the host topic is published although hardware is disabled")
	}
	if _, ok := h.Latest("", model.TopicVMs); !ok {
		t.Error("the vms topic is missing")
	}
}
