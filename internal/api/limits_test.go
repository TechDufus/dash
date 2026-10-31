package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
