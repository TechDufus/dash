package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
)

func TestStreamLimiter(t *testing.T) {
	l := newStreamLimiter(1, 2)

	w := httptest.NewRecorder()
	releaseAlice, ok := l.acquire(w, "alice")
	if !ok {
		t.Fatal("alice: first acquire refused")
	}

	w = httptest.NewRecorder()
	if _, ok := l.acquire(w, "alice"); ok {
		t.Fatal("alice: second acquire allowed")
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "5" {
		t.Errorf("alice over cap: code = %d, Retry-After = %q", w.Code, w.Header().Get("Retry-After"))
	}
	if msg := decodeErr(t, w); msg == "" {
		t.Error("alice over cap: empty error message")
	}

	w = httptest.NewRecorder()
	if _, ok := l.acquire(w, "bob"); !ok {
		t.Fatal("bob refused")
	}

	w = httptest.NewRecorder()
	if _, ok := l.acquire(w, "carol"); ok {
		t.Fatal("carol allowed past the global cap")
	}
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Errorf("carol over cap: code = %d, Retry-After = %q", w.Code, w.Header().Get("Retry-After"))
	}

	releaseAlice()
	w = httptest.NewRecorder()
	releaseAlice2, ok := l.acquire(w, "alice")
	if !ok {
		t.Fatal("alice refused after release")
	}

	// A second release of the same slot must not free another one.
	releaseAlice2()
	releaseAlice2()
	w = httptest.NewRecorder()
	if _, ok := l.acquire(w, "alice"); !ok {
		t.Fatal("alice refused after her release")
	}
	w = httptest.NewRecorder()
	if _, ok := l.acquire(w, "carol"); ok {
		t.Error("double release freed an extra slot")
	}
}

func TestNewUsesDefaultLimitsWhenZero(t *testing.T) {
	want := config.Defaults().Limits
	s := New(Deps{Cfg: config.Config{PublicURL: testOrigin}, Hub: hub.New(), Auth: authtest.Proxy()})

	if s.events.perUser != want.StreamsPerUser || s.events.max != want.Streams {
		t.Errorf("events limiter = %d/%d, want %d/%d", s.events.perUser, s.events.max, want.StreamsPerUser, want.Streams)
	}
	if s.tails.perUser != want.TailsPerUser || s.tails.max != want.Tails {
		t.Errorf("tails limiter = %d/%d, want %d/%d", s.tails.perUser, s.tails.max, want.TailsPerUser, want.Tails)
	}
	if s.tailMax != want.TailMaxDuration || s.sseWrite != want.SSEWriteTimeout || s.bodyTimeout != want.BodyReadTimeout {
		t.Errorf("durations = %v/%v/%v, want %v/%v/%v", s.tailMax, s.sseWrite, s.bodyTimeout,
			want.TailMaxDuration, want.SSEWriteTimeout, want.BodyReadTimeout)
	}

	// A server built without limits still serves a stream.
	resp, _ := sseClient(t, s.Handler(), "/api/v1/stream")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/api/v1/stream with zero limits = %d, want 200", resp.StatusCode)
	}

	// An explicit cap is kept; the rest is filled.
	got := withDefaultLimits(config.Limits{Streams: 5})
	wantPartial := want
	wantPartial.Streams = 5
	if got != wantPartial {
		t.Errorf("withDefaultLimits(Streams: 5) = %+v, want %+v", got, wantPartial)
	}
}
