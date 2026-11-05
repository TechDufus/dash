package mcp

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// fakeClock drives a debouncer: now is set by the test, and timers run when the test says.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []func()
	waits  []time.Duration
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(d time.Duration, f func()) *time.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timers, c.waits = append(c.timers, f), append(c.waits, d)
	return nil
}
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// runTimers runs the timers that are due by now, in order.
func (c *fakeClock) runTimers() {
	c.mu.Lock()
	timers := c.timers
	c.timers = nil
	c.mu.Unlock()
	for _, f := range timers {
		f()
	}
}

// One update per topic per 5 s: the first change fires at once, a burst within the window fires
// once at its end, and topics do not hold each other back.
func TestDebouncerFiresAtMostOncePerWindow(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var fired []string
	d := newDebouncer(5*time.Second, clk.Now, clk.After, func(k string) { fired = append(fired, fmt.Sprintf("%s@%s", k, clk.Now().Format("05"))) })

	d.changed("a") // fires now
	clk.advance(time.Second)
	d.changed("a") // pending, due 4 s later
	clk.advance(time.Second)
	d.changed("a") // already pending
	d.changed("b") // another topic: fires now
	if !slices.Equal(fired, []string{"a@00", "b@02"}) || !slices.Equal(clk.waits, []time.Duration{4 * time.Second}) {
		t.Fatalf("fired %v, waits %v", fired, clk.waits)
	}
	clk.advance(3 * time.Second)
	clk.runTimers() // the burst's one update, at the end of the window
	clk.advance(time.Second)
	d.changed("a") // 1 s after the last update: pending again
	if !slices.Equal(fired, []string{"a@00", "b@02", "a@05"}) || len(clk.waits) != 2 || clk.waits[1] != 4*time.Second {
		t.Fatalf("fired %v, waits %v", fired, clk.waits)
	}
	clk.advance(10 * time.Second)
	clk.runTimers()
	d.changed("b") // long after its last update: at once
	if len(fired) != 5 {
		t.Errorf("fired %v", fired)
	}
}

type topicsBackend struct {
	Backend
	topics []TopicRef
}

func (b topicsBackend) Topics(string) []TopicRef { return b.topics }

// A subscription is only to a global topic the caller may read, and needs the read scope.
func TestCheckSubscriptions(t *testing.T) {
	s := &Server{o: Options{Backend: topicsBackend{topics: []TopicRef{{Name: "core.overview"}, {Name: "core.approvals", Scoped: true}}}}}
	reader := caller{p: auth.Principal{Kind: auth.KindToken, Audience: "mcp", Scopes: []string{"read"}}}
	logsOnly := caller{p: auth.Principal{Kind: auth.KindToken, Audience: "mcp", Scopes: []string{"logs"}}}
	for _, tc := range []struct {
		c    caller
		uris []string
		want string
	}{
		{reader, []string{"dash://topics/core.overview"}, ""},
		{reader, nil, ""},
		{reader, []string{"dash://topics/core.approvals"}, "user-scoped"},
		{reader, []string{"dash://topics/nope.nothing"}, "unknown resource"},
		{reader, []string{"https://example.com/x"}, "unknown resource"},
		{logsOnly, []string{"dash://topics/core.overview"}, "read scope"},
	} {
		err := s.checkSubscriptions(tc.c, tc.uris)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%v %v: %v, want %q", tc.c.p.Scopes, tc.uris, err, tc.want)
		}
	}
}

func TestResolvePointer(t *testing.T) {
	doc := map[string]any{"a/b": map[string]any{"~c": []any{"x", "y"}}, "n": nil}
	for ptr, want := range map[string]string{
		"":             "map",
		"/a~1b/~0c/1":  "y",
		"/a~1b/~0c/2":  "error",
		"/a~1b/~0c/01": "error",
		"/missing":     "error",
		"nope":         "error",
	} {
		v, err := resolvePointer(doc, ptr)
		got := fmt.Sprint(v)
		if err != nil {
			got = "error"
		} else if ptr == "" {
			got = "map"
		}
		if got != want {
			t.Errorf("resolvePointer(%q) = %s, want %s", ptr, got, want)
		}
	}
}

func TestDownsampleAveragesIntoSixtyBuckets(t *testing.T) {
	var pts [][2]float64
	for i := range 336 {
		pts = append(pts, [2]float64{float64(i), float64(i)})
	}
	got := downsample(pts, maxSeriesPoints)
	if len(got) != 60 || got[59][0] != 335 || got[0][1] != 2 { // the first bucket is 0..5
		t.Errorf("%d points, first %v, last %v", len(got), got[0], got[len(got)-1])
	}
	if s, ok := Summarize(model.SeriesLine{Points: pts}); !ok || s.Min != 0 || s.Max != 335 || s.Last != 335 || s.Avg != 167.5 {
		t.Errorf("summary %+v", s)
	}
}
