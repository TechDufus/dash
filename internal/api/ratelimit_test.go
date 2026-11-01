package api

import (
	"testing"
	"time"
)

func TestRateLimiterSlidingWindow(t *testing.T) {
	l := newRateLimiter(3, time.Minute, 2)
	t0 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		if ok, _ := l.allow("alice", t0.Add(time.Duration(i)*10*time.Second)); !ok {
			t.Fatalf("event %d refused", i)
		}
	}
	ok, retry := l.allow("alice", t0.Add(30*time.Second))
	if ok || retry != 30*time.Second {
		t.Fatalf("4th event = %v, retry %v; want refused until the first leaves the window in 30s", ok, retry)
	}
	// A refused event is not recorded: the window frees exactly when the first one leaves.
	if ok, _ := l.allow("alice", t0.Add(time.Minute+time.Second)); !ok {
		t.Error("refused after the first event left the window")
	}
	if ok, _ := l.allow("alice", t0.Add(time.Minute+time.Second)); ok {
		t.Error("the window holds 3 events again, a 4th must be refused")
	}

	// Another key has its own window; a full table refuses a new key rather than evicting.
	if ok, _ := l.allow("bob", t0); !ok {
		t.Fatal("bob refused")
	}
	if ok, retry := l.allow("carol", t0.Add(time.Minute+time.Second)); !ok {
		t.Fatalf("carol refused (%v) although bob's window has ended and can be dropped", retry)
	}
	if ok, _ := l.allow("dave", t0.Add(time.Minute+2*time.Second)); ok {
		t.Error("dave admitted past the key bound while alice and carol are live")
	}
}
