package plugin

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// pubHost is a Host that only counts what reaches Publish.
type pubHost struct {
	Host
	topics []string
}

func (h *pubHost) Publish(topic string, _ any, _ []model.SourceStatus) {
	h.topics = append(h.topics, topic)
}

type secretObj struct {
	Ref  string `json:"ref"`
	Name string `json:"name"`
}

type storePayload struct {
	Pods    []model.Pod `json:"pods"`
	Secrets []secretObj `json:"secrets"`
}

func storeWith(t *testing.T) *ObjectStore {
	t.Helper()
	s := &ObjectStore{}
	inner := &pubHost{}
	h := s.Wrap(inner)
	h.Publish("cluster", storePayload{
		Pods: []model.Pod{
			{Ref: "kubernetes:Pod/media/web-1", Namespace: "media", Name: "web-1", Phase: "Running", Health: model.HealthOK},
			{Ref: "kubernetes:Pod/media/db-0", Namespace: "media", Name: "db-0", Phase: "Pending", Health: model.HealthCrit, Restarts: 4},
			{Ref: "kubernetes:Pod/ops/web-2", Namespace: "ops", Name: "web-2", Phase: "Running", Health: model.HealthOK},
		},
		Secrets: []secretObj{{Ref: "kubernetes:Secret/media/token", Name: "token"}},
	}, nil)
	h.Publish("nodes", []model.Node{{Ref: "kubernetes:Node/cp-1", Name: "cp-1", Ready: true}}, nil)
	if fmt.Sprint(inner.topics) != "[cluster nodes]" {
		t.Fatalf("the wrapped host published %v", inner.topics)
	}
	return s
}

func refsOf(hits []model.SearchHit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.Ref)
	}
	return out
}

func TestObjectStoreSearchesThePublishedObjects(t *testing.T) {
	s := storeWith(t)
	for _, tc := range []struct {
		query string
		kinds []string
		limit int
		want  string
	}{
		{"", nil, 25, "[kubernetes:Pod/media/web-1 kubernetes:Pod/media/db-0 kubernetes:Pod/ops/web-2 kubernetes:Node/cp-1]"},
		{"", nil, 2, "[kubernetes:Pod/media/web-1 kubernetes:Pod/media/db-0]"},
		{"WEB", nil, 25, "[kubernetes:Pod/media/web-1 kubernetes:Pod/ops/web-2]"},
		{"web media", nil, 25, "[kubernetes:Pod/media/web-1]"},
		{"node", nil, 25, "[kubernetes:Node/cp-1]"},
		{"token", nil, 25, "[]"},
		{"secret", nil, 25, "[]"},
		{"", nil, 0, "[]"},
		// The kinds filter runs before the limit: the one node is found past three pods.
		{"", []string{"node"}, 1, "[kubernetes:Node/cp-1]"},
		{"", []string{"Pod", "VM"}, 25, "[kubernetes:Pod/media/web-1 kubernetes:Pod/media/db-0 kubernetes:Pod/ops/web-2]"},
		{"web", []string{"Node"}, 25, "[]"},
	} {
		if got := fmt.Sprint(refsOf(s.Search(tc.query, tc.kinds, tc.limit))); got != tc.want {
			t.Errorf("Search(%q, %v, %d) = %s, want %s", tc.query, tc.kinds, tc.limit, got, tc.want)
		}
	}
	hit := s.Search("db-0", nil, 1)[0]
	if hit.Kind != "Pod" || hit.Title != "db-0" || hit.Summary != "health crit, phase Pending" {
		t.Errorf("hit = %+v", hit)
	}
}

func TestObjectStoreGetsAnObjectAndRefusesSecrets(t *testing.T) {
	s := storeWith(t)
	v, err := s.Get(context.Background(), "kubernetes:Pod/media/db-0")
	if pod, ok := v.(model.Pod); err != nil || !ok || pod.Restarts != 4 {
		t.Fatalf("Get = %#v, %v", v, err)
	}
	for _, ref := range []string{"kubernetes:Secret/media/token", "kubernetes:Pod/media/gone", "nonsense"} {
		if _, err := s.Get(context.Background(), ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", ref, err)
		}
		if _, err := s.StoreContext(ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("StoreContext(%q) = %v, want ErrNotFound", ref, err)
		}
	}
}

func TestObjectStoreKeepsOnlyTheLatestPayload(t *testing.T) {
	s := storeWith(t)
	s.Wrap(&pubHost{}).Publish("cluster", storePayload{Pods: []model.Pod{{Ref: "kubernetes:Pod/a/b", Name: "b"}}}, nil)
	if got := fmt.Sprint(refsOf(s.Search("", nil, 25))); got != "[kubernetes:Pod/a/b kubernetes:Node/cp-1]" {
		t.Errorf("after a second publish: %s", got)
	}
}

func TestObjectStorePartHasTheFactsAndLinks(t *testing.T) {
	s := &ObjectStore{}
	s.Wrap(&pubHost{}).Publish("alerts", model.AlertsData{Alerts: []model.Alert{{
		Ref: "alertmanager:Alert/f1", Name: "Down", Severity: "critical", Labels: map[string]string{"pod": "p", "namespace": "n"},
		StartsAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}}, nil)
	p, err := s.StoreContext("alertmanager:Alert/f1")
	if err != nil {
		t.Fatal(err)
	}
	facts := map[string]string{}
	for _, f := range p.Facts {
		facts[f.Name] = f.Value
	}
	if p.Kind != "Alert" || p.Title != "Down" || facts["severity"] != "critical" || facts["labels.pod"] != "p" ||
		facts["startsAt"] != "2026-01-02T03:04:05Z" || facts["ref"] != "" {
		t.Errorf("part = %+v", p)
	}
	if p.Events == nil || p.Related == nil || p.Series == nil || p.Conditions == nil || p.Links == nil {
		t.Errorf("a list is nil: %+v", p)
	}
}

func TestNewestEventsKeepsTheNewestTwenty(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var evs []model.Event
	for i := range 30 {
		evs = append(evs, model.Event{Reason: fmt.Sprint(i), LastSeen: t0.Add(time.Duration(i) * time.Minute)})
	}
	got := NewestEvents(evs)
	if len(got) != model.ContextMaxEvents || got[0].Reason != "29" || got[19].Reason != "10" {
		t.Errorf("NewestEvents = %d events, first %q, last %q", len(got), got[0].Reason, got[len(got)-1].Reason)
	}
	if NewestEvents(nil) == nil {
		t.Error("NewestEvents(nil) is nil")
	}
}

func TestParseRef(t *testing.T) {
	for ref, want := range map[string]string{
		"kubernetes:Pod/media/web-1":    "kubernetes Pod media/web-1",
		"proxmox:VM/101":                "proxmox VM 101",
		"Kubernetes:Pod/a":              "",
		"kubernetes:Pod/":               "",
		"kubernetes:/a":                 "",
		"kubernetes:Pod/a\nb":           "",
		"k:Pod/a":                       "",
		"kubernetes:Pod/" + longID(600): "",
	} {
		p, k, id, ok := model.ParseRef(ref)
		got := ""
		if ok {
			got = p + " " + k + " " + id
		}
		if got != want {
			t.Errorf("ParseRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

func longID(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
