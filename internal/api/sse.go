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

// done clears the write deadline. http.Server has no WriteTimeout, so a stale deadline would
// carry over to the next request on a kept-alive connection.
func (e *sseWriter) done() {
	e.mu.Lock()
	defer e.mu.Unlock()
	_ = e.rc.SetWriteDeadline(time.Time{})
}

func (e *sseWriter) event(name string, data []byte) error {
	return e.write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, data))
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
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		return e.event("line", b)
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("log tail ended", "error", err)
	}
}
