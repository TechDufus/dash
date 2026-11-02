package plugin_test

import (
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

func TestFirstSeenKeepsTheFirstSighting(t *testing.T) {
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var fs plugin.FirstSeen // the zero value works
	items := []model.AttentionItem{{ID: "vm:1"}}
	if got := fs.Stamp(t0, items)[0].Since; !got.Equal(t0) {
		t.Fatalf("first sighting = %v, want now", got)
	}
	if !items[0].Since.IsZero() {
		t.Error("Stamp modified its argument")
	}
	if got := fs.Stamp(t0.Add(10*time.Minute), items)[0].Since; !got.Equal(t0) {
		t.Errorf("since moved to %v, must stay %v", got, t0)
	}
}

func TestFirstSeenForgetsWhatIsGone(t *testing.T) {
	t0 := time.Unix(1000, 0)
	fs := plugin.NewFirstSeen()
	fs.Stamp(t0, []model.AttentionItem{{ID: "a"}, {ID: "b"}})
	// b recovers; a stays.
	got := fs.Stamp(t0.Add(time.Minute), []model.AttentionItem{{ID: "a"}})
	if !got[0].Since.Equal(t0) {
		t.Errorf("a = %v, want %v", got[0].Since, t0)
	}
	// b fails again: it starts over.
	got = fs.Stamp(t0.Add(2*time.Minute), []model.AttentionItem{{ID: "a"}, {ID: "b"}})
	if !got[0].Since.Equal(t0) || !got[1].Since.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("after the recovery: a = %v, b = %v", got[0].Since, got[1].Since)
	}
	// An empty call forgets everything.
	fs.Stamp(t0.Add(3*time.Minute), nil)
	if got := fs.Stamp(t0.Add(4*time.Minute), []model.AttentionItem{{ID: "a"}}); !got[0].Since.Equal(t0.Add(4 * time.Minute)) {
		t.Errorf("a after an empty list = %v", got[0].Since)
	}
}

func TestFirstSeenKeepsANaturalTime(t *testing.T) {
	t0 := time.Unix(5000, 0)
	natural := t0.Add(-time.Hour)
	fs := plugin.NewFirstSeen()
	got := fs.Stamp(t0, []model.AttentionItem{{ID: "n", Since: natural}, {ID: "z"}})
	if !got[0].Since.Equal(natural) || !got[1].Since.Equal(t0) {
		t.Errorf("natural = %v, stamped = %v", got[0].Since, got[1].Since)
	}
	// The natural item does not leave a stamp behind: when it later has none, it starts at the
	// time it is first seen without one.
	got = fs.Stamp(t0.Add(time.Minute), []model.AttentionItem{{ID: "n"}})
	if !got[0].Since.Equal(t0.Add(time.Minute)) {
		t.Errorf("n without a time = %v", got[0].Since)
	}
}

func TestFirstSeenIsSafeForConcurrentUse(t *testing.T) {
	fs := plugin.NewFirstSeen()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				fs.Stamp(time.Unix(int64(i), 0), []model.AttentionItem{{ID: "x"}, {ID: "y"}})
			}
		}()
	}
	wg.Wait()
}
