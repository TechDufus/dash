// Package hub keeps the latest Envelope per topic and fans changes out to SSE clients.
package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// subscriberBuffer is each client's channel capacity; a client that falls behind is dropped.
const subscriberBuffer = 32

// etagLen is the ETag's length in hex characters: the first 8 bytes of the content digest.
const etagLen = 16

// Event is one published topic state; Data is the marshalled model.Envelope.
type Event struct {
	Topic string
	Data  []byte
	ETag  string // first 16 hex chars of the content digest (data and sources, not times); no quotes
	User  string // "" for global topics
}

// key addresses one stored copy: a global topic has user "".
type key struct{ user, topic string }

// Hub stores the latest envelope per topic, and per user for user-scoped topics.
type Hub struct {
	mu     sync.Mutex
	latest map[key]Event
	hashes map[key][32]byte
	subs   map[*subscriber]struct{}
	order  func() []model.Topic
	now    func() time.Time
}

type subscriber struct {
	user string
	want func(topic string) bool // nil = every topic
	ch   chan Event
}

// accepts reports whether the subscriber receives ev: a global event reaches everyone, a scoped
// one only its owner, and either only for topics the subscriber asked for.
func (s *subscriber) accepts(ev Event) bool {
	if ev.User != "" && ev.User != s.user {
		return false
	}
	return s.want == nil || s.want(ev.Topic)
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{
		latest: map[key]Event{},
		hashes: map[key][32]byte{},
		subs:   map[*subscriber]struct{}{},
		now:    time.Now,
	}
}

// SetOrder sets the order in which Subscribe lists its snapshot, typically the plugin registry's
// display order. Topics it does not list follow in name order; nil restores name order. order is
// called without the hub's lock held, so it may take its own locks.
func (h *Hub) SetOrder(order func() []model.Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.order = order
}

// SetClock sets the clock that stamps each envelope's updatedAt; nil restores the wall clock.
// Demo mode's fixed clock (DASH_DEMO_CLOCK) uses it.
func (h *Hub) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = now
}

// Publish marshals data into an Envelope and publishes it, to every subscriber, when data or the
// sources' ok/error changed since the last publish of that topic (checkedAt and updatedAt are not
// compared, so a quiet topic stays quiet). It reports whether anything was published.
func (h *Hub) Publish(topic model.Topic, data any, sources []model.SourceStatus) (bool, error) {
	return h.publish("", topic, data, sources)
}

// PublishFor is Publish for a user-scoped topic: only that user's subscribers receive it, and
// storage and dedup are per user.
func (h *Hub) PublishFor(user string, topic model.Topic, data any, sources []model.SourceStatus) (bool, error) {
	if user == "" {
		return false, errors.New("hub: PublishFor needs a user; use Publish for a global topic")
	}
	return h.publish(user, topic, data, sources)
}

func (h *Hub) publish(user string, topic model.Topic, data any, sources []model.SourceStatus) (bool, error) {
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

	k := key{user, string(topic)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, ok := h.hashes[k]; ok && prev == digest {
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
	ev := Event{Topic: string(topic), Data: body, ETag: hex.EncodeToString(digest[:])[:etagLen], User: user}
	h.hashes[k] = digest
	h.latest[k] = ev
	h.fanOut(ev)
	return true, nil
}

// fanOut delivers ev to every subscriber that accepts it and drops the ones that fell behind. The
// caller holds h.mu.
func (h *Hub) fanOut(ev Event) {
	for s := range h.subs {
		if !s.accepts(ev) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Too slow: close so the client reconnects and receives the full state.
			delete(h.subs, s)
			close(s.ch)
		}
	}
}

// Resend delivers the stored latest event of topic again to the current subscribers: the global
// copy first, then every user's own copy, each only to the subscribers that accept it (their
// topic filter and user scope). Data, digest and ETag are untouched and nothing is deduplicated,
// so a client that dropped its listener for the topic gets the state it missed. It runs under
// the hub's lock, so it cannot overtake a concurrent Publish. It reports false when the topic
// has no stored event.
func (h *Hub) Resend(topic model.Topic) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var evs []Event
	for k, ev := range h.latest {
		if k.topic == string(topic) {
			evs = append(evs, ev)
		}
	}
	slices.SortFunc(evs, func(a, b Event) int { return strings.Compare(a.User, b.User) })
	for _, ev := range evs {
		h.fanOut(ev)
	}
	return len(evs) > 0
}

// Remove forgets a topic: its global state, every user's copy and their dedup digests, so a new
// subscriber gets nothing for it and the next identical publish is sent. No event goes to
// current subscribers (a disabled plugin's pages are dropped from core.plugins instead).
func (h *Hub) Remove(topic model.Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.latest {
		if k.topic == string(topic) {
			delete(h.latest, k)
			delete(h.hashes, k)
		}
	}
}

// Latest returns the stored envelope of a topic as user sees it: the user's own scoped copy if
// there is one, else the global one.
func (h *Hub) Latest(user string, topic model.Topic) (Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if user != "" {
		if ev, ok := h.latest[key{user, string(topic)}]; ok {
			return ev, true
		}
	}
	ev, ok := h.latest[key{"", string(topic)}]
	return ev, ok
}

// Subscribe returns the current state of every topic that want accepts and user may see (in
// SetOrder order), and a channel of later changes. want == nil accepts all topics. The channel is
// closed if the subscriber overflows; call cancel when done.
func (h *Hub) Subscribe(user string, want func(topic string) bool) (snapshot []Event, ch <-chan Event, cancel func()) {
	h.mu.Lock()
	orderFn := h.order
	h.mu.Unlock()
	var order []model.Topic
	if orderFn != nil {
		order = orderFn() // outside the lock: the registry may be locked by a caller of Publish
	}

	s := &subscriber{user: user, want: want, ch: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	defer h.mu.Unlock()
	visible := map[string]Event{}
	for k, ev := range h.latest {
		// A user's own copy hides the global one of the same topic, as Latest does.
		if s.accepts(ev) && (k.user != "" || visible[k.topic].User == "") {
			visible[k.topic] = ev
		}
	}
	for _, t := range order {
		if ev, ok := visible[string(t)]; ok {
			snapshot = append(snapshot, ev)
			delete(visible, string(t))
		}
	}
	rest := make([]string, 0, len(visible))
	for t := range visible {
		rest = append(rest, t)
	}
	slices.Sort(rest)
	for _, t := range rest {
		snapshot = append(snapshot, visible[t])
	}
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
