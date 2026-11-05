package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// sseWriter serialises writes to one SSE response (events and the heartbeat goroutine).
type sseWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration // deadline of each write
}

// startSSE sends the SSE headers, the retry hint and flushes them. Each write must finish within
// timeout.
func startSSE(w http.ResponseWriter, timeout time.Duration) (*sseWriter, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	e := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: timeout}
	return e, e.write("retry: 3000\n\n")
}

func (e *sseWriter) write(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A stalled client must not pin the handler: each write gets its own deadline.
	if err := e.rc.SetWriteDeadline(time.Now().Add(e.timeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if _, err := e.w.Write([]byte(s)); err != nil {
		return err
	}
	return e.rc.Flush()
}

// done gives net/http's closing chunk a fresh deadline. The handler may end long after the last
// write (a tail hitting its cap between heartbeats), so the previous deadline can already have
// passed; leaving none would let a client that stopped reading hold the connection. net/http
// clears the write deadline itself once the request is finished.
func (e *sseWriter) done() {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.rc.SetWriteDeadline(time.Now().Add(e.timeout))
}

func (e *sseWriter) event(name string, data []byte) error {
	return e.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, data))
}

// eventID is event with an SSE id, which the browser echoes as Last-Event-ID when it reconnects.
func (e *sseWriter) eventID(name, id string, data []byte) error {
	return e.write(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", id, name, data))
}

// pings sends a heartbeat comment every interval until ctx ends or a write fails. Before each
// one it asks alive (nil: always alive); when that says no it returns true without writing, and
// the caller ends the stream.
func (e *sseWriter) pings(ctx context.Context, interval time.Duration, alive func() bool) (revoked bool) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if alive != nil && !alive() {
				return true
			}
			if e.write(": ping\n\n") != nil {
				return false
			}
		}
	}
}

// sessionAlive reports whether the session or the token a stream was opened with still signs its
// request in: a logout ("Log out" bumps the user's epoch, "Log out everywhere" the global one),
// an expiry or a token's revocation (contract D13) must end a stream that is already open, not
// only the next request. Only streams of session and token principals are checked; proxy, none,
// dev, demo and the anonymous viewer have no credential to lose. The answer comes from the same
// authenticator or resolver as authn, with the request's own cookie or token (at the audience it
// was opened with: a plugin's router has cut the path), so the stream ends at the first
// heartbeat after the revocation.
func (s *Server) sessionAlive(r *http.Request) bool {
	opened := principalFrom(r.Context())
	switch opened.Kind {
	case auth.KindSession:
		p, _, fail := s.authenticate(r)
		return fail == nil && p.Kind == auth.KindSession && p.User == opened.User
	case auth.KindToken:
		token, ok := auth.Bearer(r)
		if !ok || s.d.Tokens == nil {
			return false
		}
		p, err := s.d.Tokens.Resolve(token, opened.Audience)
		return err == nil && p.TokenID == opened.TokenID
	}
	return true
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	want, err := topicFilter(r.URL.Query().Get("topics"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
		return
	}
	release, ok := s.events.acquire(w, userFrom(r.Context()))
	if !ok {
		s.deny(r, obs.AuthStreamLimit)
		return
	}
	defer release()
	defer s.d.Metrics.StreamOpened(obs.StreamEvents)()
	snapshot, ch, cancel := s.d.Hub.Subscribe(userFrom(r.Context()), want)
	defer cancel()
	e, err := startSSE(w, s.sseWrite)
	defer e.done()
	if err != nil {
		return
	}
	for _, ev := range snapshot {
		if e.event(ev.Topic, ev.Data) != nil {
			return
		}
	}
	t := time.NewTicker(s.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok { // dropped as a slow client; the browser reconnects and gets full state
				return
			}
			if e.event(ev.Topic, ev.Data) != nil {
				return
			}
		case <-t.C:
			if !s.sessionAlive(r) {
				return // the browser reconnects, gets 401, and signs in again
			}
			if e.write(": ping\n\n") != nil {
				return
			}
		}
	}
}
