package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// sseWriter serialises writes to one SSE response (events and the heartbeat goroutine).
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	rc *http.ResponseController
}

// startSSE sends the SSE headers, the retry hint and flushes them.
func startSSE(w http.ResponseWriter) (*sseWriter, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	e := &sseWriter{w: w, rc: http.NewResponseController(w)}
	return e, e.write("retry: 3000\n\n")
}

func (e *sseWriter) write(s string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// A stalled client must not pin the handler: each write gets its own deadline.
	if err := e.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
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
	_ = e.rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
}

func (e *sseWriter) event(name string, data []byte) error {
	return e.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, data))
}

// eventID is event with an SSE id, which the browser echoes as Last-Event-ID when it reconnects.
func (e *sseWriter) eventID(name, id string, data []byte) error {
	return e.write(fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", id, name, data))
}

// pings sends a heartbeat comment every interval until ctx ends or a write fails.
func (e *sseWriter) pings(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if e.write(": ping\n\n") != nil {
				return
			}
		}
	}
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	release, ok := s.events.acquire(w, userFrom(r.Context()))
	if !ok {
		return
	}
	defer release()
	snapshot, ch, cancel := s.d.Hub.Subscribe()
	defer cancel()
	e, err := startSSE(w)
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
			if e.write(": ping\n\n") != nil {
				return
			}
		}
	}
}

func (s *Server) logsTail(w http.ResponseWriter, r *http.Request) {
	q := logQueryFrom(r)
	if _, err := upstream.BuildQuery(q, true); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// A reconnecting EventSource sends the id of the last line it saw. Replay from just before
	// it, never further back than maxTailResume, and drop what the client already has.
	last, resumed := time.Time{}, false
	if t, err := time.Parse(time.RFC3339Nano, r.Header.Get("Last-Event-ID")); err == nil {
		last, resumed = t, true
		q.StartOffset = min(max(s.now().Sub(t)+5*time.Second, 0), maxTailResume)
	}
	release, ok := s.tails.acquire(w, userFrom(r.Context()))
	if !ok {
		return
	}
	defer release()
	e, err := startSSE(w)
	defer e.done()
	if err != nil {
		return
	}
	// When the deadline ends the tail, the browser's EventSource reconnects by itself.
	ctx, cancel := context.WithTimeout(r.Context(), s.tailMax)
	// The heartbeat writes to w, so it must be finished before the handler returns.
	pingsDone := make(chan struct{})
	go func() {
		defer close(pingsDone)
		e.pings(ctx, s.heartbeat)
	}()
	defer func() {
		cancel()
		<-pingsDone
	}()
	err = s.d.VLogs.Tail(ctx, q, func(l model.LogLine) error {
		if resumed && !l.Time.After(last) { // already delivered before the reconnect
			return nil
		}
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		if l.Time.IsZero() {
			return e.event("line", b)
		}
		return e.eventID("line", l.Time.UTC().Format(time.RFC3339Nano), b)
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("log tail ended", "error", err)
	}
}
