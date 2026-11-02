package plugin

import (
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// FirstSeen gives attention items that have no time of their own a stable Since: the moment the
// item was first seen with that ID, kept for as long as it keeps appearing. The supervisor owns
// one per plugin instance and stamps the lists of its AttentionSource with it (Contributors), so
// the clocks outlive a restart of the plugin and no plugin has to opt in. The zero value is ready
// to use.
type FirstSeen struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewFirstSeen returns an empty FirstSeen.
func NewFirstSeen() *FirstSeen { return &FirstSeen{} }

// Reset forgets every sighting: the next call starts every clock over.
func (f *FirstSeen) Reset() {
	f.mu.Lock()
	f.seen = nil
	f.mu.Unlock()
}

// Stamp returns items with Since filled in for every item whose Since is zero: the time of the
// first call (with that ID) that has been followed by no call without it. An item that already
// has a Since keeps it. An ID that is missing from this call is forgotten, so an item that
// recovers and fails again starts over at now. items itself is not modified.
func (f *FirstSeen) Stamp(now time.Time, items []model.AttentionItem) []model.AttentionItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]time.Time{}
	}
	out := make([]model.AttentionItem, len(items))
	copy(out, items)
	live := make(map[string]bool, len(out))
	for i := range out {
		id := out[i].ID
		live[id] = true
		if !out[i].Since.IsZero() {
			continue
		}
		t, ok := f.seen[id]
		if !ok {
			t = now
			f.seen[id] = t
		}
		out[i].Since = t
	}
	for id := range f.seen {
		if !live[id] {
			delete(f.seen, id)
		}
	}
	return out
}
