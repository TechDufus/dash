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
	"github.com/TechDufus/dash/internal/topics"
)

func TestPublishesEveryTopic(t *testing.T) {
	h := hub.New()
	New(Config(config.Defaults())).publish(h)
	for _, topic := range []model.Topic{
		model.TopicOverview, model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux,
		model.TopicStorage, model.TopicAlerts, model.TopicStatus, model.TopicApps,
	} {
		ev, ok := h.Latest(topic)
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
	h := hub.New()
	New(Config(config.Defaults())).publish(h)
	snap, _, cancel := h.Subscribe()
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
