package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// sseClient opens path on a real server and delivers its lines on a channel.
func sseClient(t *testing.T, h http.Handler, path string) (*http.Response, <-chan string) {
	t.Helper()
	return sseClientWith(t, h, path, nil)
}

// sseClientWith is sseClient with extra request headers.
func sseClientWith(t *testing.T, h http.Handler, path string, header map[string]string) (*http.Response, <-chan string) {
	t.Helper()
	ts := httptest.NewServer(h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ts.Close() })
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
	for k, v := range header {
		r.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return resp, lines
}

// expect reads lines until one equals want, failing after a timeout.
func expect(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before %q", want)
			}
			if l == want {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

// expectContaining reads lines until one contains substr.
func expectContaining(t *testing.T, lines <-chan string, substr string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended before a line containing %q", substr)
			}
			if strings.Contains(l, substr) {
				return
			}
		case <-timeout:
			t.Fatalf("timed out waiting for a line containing %q", substr)
		}
	}
}

func TestStream(t *testing.T) {
	s := newServer(nil)
	s.heartbeat = 20 * time.Millisecond
	if _, err := s.d.Hub.Publish(model.TopicOverview, map[string]string{"a": "1"}, []model.SourceStatus{}); err != nil {
		t.Fatal(err)
	}
	resp, lines := sseClient(t, s.Handler(), "/api/v1/stream")
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", resp.Header)
	}

	expect(t, lines, "retry: 3000")
	expect(t, lines, "event: core.overview") // snapshot
	if _, err := s.d.Hub.Publish(model.TopicOverview, map[string]string{"a": "2"}, []model.SourceStatus{}); err != nil {
		t.Fatal(err)
	}
	expect(t, lines, `event: core.overview`) // the live event follows its own event line
	expectContaining(t, lines, `"a":"2"`)
	expect(t, lines, ": ping") // heartbeats keep coming
}

// streamStatus opens path on a real server as user and returns the response status.
func streamStatus(t *testing.T, h http.Handler, path, user string) int {
	t.Helper()
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", user)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestStreamCapPerUser(t *testing.T) {
	s := newServer(nil)
	s.events = newStreamLimiter(1, 10)
	h := s.Handler()
	resp, lines := sseClient(t, h, "/api/v1/stream") // alice's slot
	expect(t, lines, "retry: 3000")
	if got := streamStatus(t, h, "/api/v1/stream", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("second stream for alice = %d, want 429", got)
	}
	// Closing the stream frees the slot once the server notices the disconnect.
	resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := streamStatus(t, h, "/api/v1/stream", "alice")
		if got == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream after close = %d, want 200 (slot not released)", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// limitsFromConfig resolves a config file that sets server.limits.
func limitsFromConfig(t *testing.T, limits string) config.Config {
	t.Helper()
	file := "server:\n  publicURL: https://dash.example.com\n  limits:\n" + limits
	res, err := config.Resolve(config.Sources{File: &config.File{Path: "config.yaml", Data: []byte(file)}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Config
}

func TestStreamCapFromConfig(t *testing.T) {
	cfg := limitsFromConfig(t, "    streamsPerUser: 1\n")
	h := newServer(func(d *Deps) { d.Cfg = cfg }).Handler()
	_, lines := sseClient(t, h, "/api/v1/stream") // alice's only slot
	expect(t, lines, "retry: 3000")
	if got := streamStatus(t, h, "/api/v1/stream", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("second stream for alice = %d, want 429", got)
	}
	// The cap is per user: bob still gets his own.
	if got := streamStatus(t, h, "/api/v1/stream", "bob"); got != http.StatusOK {
		t.Errorf("stream for bob = %d, want 200", got)
	}
}

func TestServerTakesLimitsFromConfig(t *testing.T) {
	cfg := limitsFromConfig(t, "    tailMaxDuration: 7m\n    sseWriteTimeout: 3s\n    bodyReadTimeout: 4s\n    tailsPerUser: 1\n    tails: 2\n")
	s := newServer(func(d *Deps) { d.Cfg = cfg })
	if s.tailMax != 7*time.Minute || s.sseWrite != 3*time.Second || s.bodyTimeout != 4*time.Second {
		t.Errorf("tailMax, sseWrite, bodyTimeout = %v, %v, %v", s.tailMax, s.sseWrite, s.bodyTimeout)
	}
	// tailsPerUser 1, tails 2: alice fills her slot, bob the second, nobody else fits.
	acquire := func(user string) (bool, int) {
		w := httptest.NewRecorder()
		_, ok := s.tails.acquire(w, user)
		return ok, w.Code
	}
	if ok, _ := acquire("alice"); !ok {
		t.Error("first tail for alice was refused")
	}
	if ok, _ := acquire("bob"); !ok {
		t.Error("first tail for bob was refused")
	}
	if ok, code := acquire("alice"); ok || code != http.StatusTooManyRequests {
		t.Errorf("second tail for alice = %v, %d; want refused with 429", ok, code)
	}
	if ok, code := acquire("carol"); ok || code != http.StatusServiceUnavailable {
		t.Errorf("tail past the total = %v, %d; want refused with 503", ok, code)
	}
}

// deadlineWriter records the write deadlines set through http.ResponseController.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	write []time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.write = append(d.write, t)
	return nil
}

func TestSSEWriteDeadline(t *testing.T) {
	d := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	e, err := startSSE(d, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.event("x", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if len(d.write) != 2 {
		t.Fatalf("write deadlines = %d, want one per write", len(d.write))
	}
	now := time.Now()
	for i, dl := range d.write {
		if dl.Before(now.Add(9*time.Second)) || dl.After(now.Add(11*time.Second)) {
			t.Errorf("write deadline %d is %v from now, want about 10s", i, dl.Sub(now))
		}
	}
	e.done()
	if len(d.write) != 3 {
		t.Fatalf("write deadlines after done = %d, want 3", len(d.write))
	}
	// The closing chunk gets a fresh, bounded deadline; net/http clears it afterwards.
	if last := d.write[2]; last.Before(now.Add(9*time.Second)) || last.After(now.Add(11*time.Second)) {
		t.Errorf("deadline after done is %v from now, want about 10s", last.Sub(now))
	}
}

// The configured server.limits.sseWriteTimeout, not a built-in, is the deadline /api/stream
// puts on each of its writes, and the closing chunk's deadline is bounded the same way (P2.11).
func TestStreamUsesTheConfiguredWriteTimeout(t *testing.T) {
	cfg := limitsFromConfig(t, "    sseWriteTimeout: 3s\n")
	s := newServer(func(d *Deps) { d.Cfg = cfg })
	s.heartbeat = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/stream", nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
	d := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	s.Handler().ServeHTTP(d, r)
	end := time.Now()

	if len(d.write) < 3 {
		t.Fatalf("write deadlines = %d, want one per write plus the closing one", len(d.write))
	}
	for i, dl := range d.write {
		if dl.Before(start.Add(3*time.Second)) || dl.After(end.Add(3*time.Second)) {
			t.Errorf("write deadline %d is %v after the start, want the configured 3s", i, dl.Sub(start))
		}
	}
}

// The read deadline of a request body, through a plugin action: the generic mechanism of every
// POST the API takes.
func TestDecodeBodyDeadline(t *testing.T) {
	const path = "/api/v1/plugins/aa/actions/silence.create"
	t.Run("stalled body gets 400 and the action does not run", func(t *testing.T) {
		h := newHarness(t)
		h.bodyTimeout = 200 * time.Millisecond
		ts := h.server(t)
		conn, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second)) // the client's own timeout
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Preferred-Username: alice\r\n"+
			"X-Dash-Csrf: 1\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"alertname\":",
			path, testHost, testOrigin)
		start := time.Now()
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("no response to a stalled body after %v: %v", time.Since(start), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("400 after %v, want about the 200ms body deadline", el)
		}
		if n := len(h.acts()); n != 0 {
			t.Errorf("action ran %d times", n)
		}
	})

	t.Run("complete body whose action outlasts the deadline still succeeds", func(t *testing.T) {
		h := newHarness(t)
		h.act = func(string, any) (any, error) {
			time.Sleep(500 * time.Millisecond)
			return map[string]string{"silenceId": silenceID}, nil
		}
		h.bodyTimeout = 200 * time.Millisecond
		ts := h.server(t)
		body, _ := json.Marshal(silenceIn{Alertname: "HighTemp"})
		r, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
		r.Host = testHost
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		r.Header.Set("X-Dash-Csrf", "1")
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		resp, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if n := len(h.acts()); resp.StatusCode != http.StatusOK || n != 1 {
			t.Errorf("status = %d, acts = %d, want 200 and 1", resp.StatusCode, n)
		}
	})
}
