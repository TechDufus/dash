// Package hub keeps the latest Envelope per topic and fans changes out to SSE clients.
package hub

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// subscriberBuffer is each client's channel capacity; a client that falls behind is dropped.
const subscriberBuffer = 32

// Event is one published topic state; Data is the marshalled model.Envelope.
type Event struct {
	Topic string
	Data  []byte
}

// Hub stores the latest envelope per topic.
type Hub struct {
	mu     sync.Mutex
	latest map[string]Event
	hashes map[string][32]byte
	subs   map[*subscriber]struct{}
	now    func() time.Time
}

type subscriber struct {
	ch chan Event
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{
		latest: map[string]Event{},
		hashes: map[string][32]byte{},
		subs:   map[*subscriber]struct{}{},
		now:    time.Now,
	}
}

// Publish marshals data into an Envelope and publishes it when data or the sources' ok/error
// changed since the last publish of that topic (checkedAt and updatedAt are not compared, so a
// quiet topic stays quiet). It reports whether anything was published.
func (h *Hub) Publish(topic model.Topic, data any, sources []model.SourceStatus) (bool, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return false, err
	}
	if sources == nil {
		sources = []model.SourceStatus{}
	}
	sum := sha256.New()
	sum.Write(raw)
	for _, s := range sources {
		sum.Write([]byte{0})
		sum.Write([]byte(s.Name))
		if s.OK {
			sum.Write([]byte{1})
		} else {
			sum.Write([]byte{2})
		}
		sum.Write([]byte(s.Error))
	}
	var digest [32]byte
	copy(digest[:], sum.Sum(nil))

	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, ok := h.hashes[string(topic)]; ok && prev == digest {
		return false, nil
	}
	body, err := json.Marshal(model.Envelope{
		Topic:     string(topic),
		UpdatedAt: h.now().UTC(),
		Sources:   sources,
		Data:      raw,
	})
	if err != nil {
		return false, err
	}
	ev := Event{Topic: string(topic), Data: body}
	h.hashes[string(topic)] = digest
	h.latest[string(topic)] = ev
	for s := range h.subs {
		select {
		case s.ch <- ev:
		default:
			// Too slow: close so the client reconnects and receives the full state.
			delete(h.subs, s)
			close(s.ch)
		}
	}
	return true, nil
}

// Latest returns the stored envelope of a topic.
func (h *Hub) Latest(topic model.Topic) (Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ev, ok := h.latest[string(topic)]
	return ev, ok
}

// Subscribe returns the current state of every topic (in model topic order) and a channel of
// later changes. The channel is closed if the subscriber overflows; call cancel when done.
func (h *Hub) Subscribe() (snapshot []Event, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	order := []model.Topic{
		model.TopicOverview, model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux,
		model.TopicStorage, model.TopicAlerts, model.TopicStatus, model.TopicApps,
	}
	for _, t := range order {
		if ev, ok := h.latest[string(t)]; ok {
			snapshot = append(snapshot, ev)
		}
	}
	s := &subscriber{ch: make(chan Event, subscriberBuffer)}
	h.subs[s] = struct{}{}
	return snapshot, s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[s]; ok {
			delete(h.subs, s)
			close(s.ch)
		}
	}
}
