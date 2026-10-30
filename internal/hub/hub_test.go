package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

func src(ok bool, err string) []model.SourceStatus {
	return []model.SourceStatus{{Name: "x", OK: ok, Error: err, CheckedAt: time.Now()}}
}

func TestPublishDedupe(t *testing.T) {
	h := New()
	tests := []struct {
		name     string
		data     any
		sources  []model.SourceStatus
		wantSent bool
	}{
		{"first publish", map[string]int{"a": 1}, src(true, ""), true},
		{"same data, new checkedAt", map[string]int{"a": 1}, src(true, ""), false},
		{"data changed", map[string]int{"a": 2}, src(true, ""), true},
		{"source failed", map[string]int{"a": 2}, src(false, "boom"), true},
		{"same failure", map[string]int{"a": 2}, src(false, "boom"), false},
	}
	for _, tc := range tests {
		sent, err := h.Publish(model.TopicHost, tc.data, tc.sources)
		if err != nil || sent != tc.wantSent {
			t.Errorf("%s: sent=%v err=%v, want sent=%v", tc.name, sent, err, tc.wantSent)
		}
	}
	ev, ok := h.Latest(model.TopicHost)
	if !ok {
		t.Fatal("no latest event")
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil || env.Topic != "host" || string(env.Data) != `{"a":2}` || len(env.Sources) != 1 || env.Sources[0].OK {
		t.Errorf("envelope = %+v err=%v", env, err)
	}
}

func TestSubscribeSnapshotAndLive(t *testing.T) {
	h := New()
	h.Publish(model.TopicApps, 1, nil)
	h.Publish(model.TopicOverview, 2, nil)
	snap, ch, cancel := h.Subscribe()
	defer cancel()
	if len(snap) != 2 || snap[0].Topic != "overview" || snap[1].Topic != "apps" {
		t.Fatalf("snapshot must follow topic order, got %+v", snap)
	}
	h.Publish(model.TopicHost, 3, nil)
	select {
	case ev := <-ch:
		if ev.Topic != "host" {
			t.Errorf("live topic = %s", ev.Topic)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := New()
	_, ch, cancel := h.Subscribe()
	defer cancel()
	for i := range subscriberBuffer + 1 {
		h.Publish(model.TopicHost, i, nil)
	}
	n := 0
	for range ch { // closed by the hub after the buffer overflowed
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("drained %d events, want %d", n, subscriberBuffer)
	}
	// A second cancel after the hub closed the channel must be harmless.
	cancel()
}
