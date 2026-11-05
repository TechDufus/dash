package api

import (
	"testing"
	"time"
)

// A full table is swept of the buckets that refilled, never of one still drained: evicting a
// drained bucket would hand its token a fresh burst. A new key is refused while every bucket in
// the table is still live.
func TestTokenBucketSweepKeepsDrainedBuckets(t *testing.T) {
	l := newTokenBucket(60, 60, 2) // one request a second, bursts of 60, two keys
	t0 := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for range 60 {
		if ok, _ := l.allow("drained", t0); !ok {
			t.Fatal("the burst was refused")
		}
	}
	if ok, _ := l.allow("idle", t0); !ok {
		t.Fatal("a second key was refused")
	}
	// 10 s later "idle" has refilled and "drained" holds 10: the sweep drops only "idle".
	later := t0.Add(10 * time.Second)
	if ok, _ := l.allow("new", later); !ok {
		t.Fatal("a new key was refused although a refilled bucket could go")
	}
	allowed := 0
	for range 60 {
		if ok, _ := l.allow("drained", later); ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("the drained key got %d requests after the sweep, want the 10 it refilled", allowed)
	}
	// Drain "new" as well: both buckets are live, so another new key is refused rather than
	// evicting one.
	for range 59 {
		l.allow("new", later)
	}
	if ok, _ := l.allow("third", later.Add(6*time.Second)); ok {
		t.Error("a new key evicted a live bucket")
	}
}
