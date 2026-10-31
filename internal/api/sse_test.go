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

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
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
	resp, lines := sseClient(t, s.Handler(), "/api/stream")
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", resp.Header)
	}

	expect(t, lines, "retry: 3000")
	expect(t, lines, "event: overview") // snapshot
	if _, err := s.d.Hub.Publish(model.TopicOverview, map[string]string{"a": "2"}, []model.SourceStatus{}); err != nil {
		t.Fatal(err)
	}
	expect(t, lines, `event: overview`) // the live event follows its own event line
	expectContaining(t, lines, `"a":"2"`)
	expect(t, lines, ": ping") // heartbeats keep coming
}

func TestLogsTail(t *testing.T) {
	logs := &fakeLogs{}
	logs.tail = func(emit func(model.LogLine) error) error {
		return emit(model.LogLine{Time: fixedNow, Pod: "plex-0", Container: "plex", Msg: "hello"})
	}
	s := newServer(func(d *Deps) { d.VLogs = logs })
	s.heartbeat = 20 * time.Millisecond
	_, lines := sseClient(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0")
	expect(t, lines, "retry: 3000")
	expect(t, lines, "event: line")
	for l := range lines {
		if strings.HasPrefix(l, "data: ") {
			if !strings.Contains(l, `"msg":"hello"`) || !strings.Contains(l, `"pod":"plex-0"`) {
				t.Errorf("data = %s", l)
			}
			break
		}
	}
	if logs.q.Namespace != "media" || logs.q.Pod != "plex-0" {
		t.Errorf("query = %+v", logs.q)
	}
}

func TestVMDetailErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
	}{
		{topics.ErrNotFound, 404},
		{context.DeadlineExceeded, 502},
		{nil, 200},
	} {
		s := newServer(func(d *Deps) { d.Data = fakeData{vmErr: tc.err} })
		if w := (req{path: "/api/vms/100"}).do(s.Handler()); w.Code != tc.code {
			t.Errorf("err %v: code = %d, want %d", tc.err, w.Code, tc.code)
		}
	}
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
	resp, lines := sseClient(t, h, "/api/stream") // alice's slot
	expect(t, lines, "retry: 3000")
	if got := streamStatus(t, h, "/api/stream", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("second stream for alice = %d, want 429", got)
	}
	// Closing the stream frees the slot once the server notices the disconnect.
	resp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := streamStatus(t, h, "/api/stream", "alice")
		if got == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream after close = %d, want 200 (slot not released)", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLogsTailEndsAfterMax(t *testing.T) {
	s := newServer(nil) // the default fakeLogs blocks until its context ends
	s.tailMax = 50 * time.Millisecond
	_, lines := sseClient(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0")
	expect(t, lines, "retry: 3000")
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("tail still open long after tailMax")
		}
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
	e, err := startSSE(d)
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

// slowAM delays CreateSilence to model an action that outlasts the body deadline.
type slowAM struct {
	*fakeAM
	delay time.Duration
}

func (a slowAM) CreateSilence(ctx context.Context, s upstream.SilenceSpec) (string, error) {
	select {
	case <-time.After(a.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return a.fakeAM.CreateSilence(ctx, s)
}

func TestDecodeBodyDeadline(t *testing.T) {
	t.Run("stalled body gets 400 and the action does not run", func(t *testing.T) {
		am := &fakeAM{}
		s := newServer(func(d *Deps) { d.AM = am })
		s.bodyTimeout = 200 * time.Millisecond
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()
		conn, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second)) // the client's own timeout
		fmt.Fprintf(conn, "POST /api/silences HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Preferred-Username: alice\r\n"+
			"X-Dash-Csrf: 1\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"matchers\":",
			testHost, testOrigin)
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
		if am.created != 0 {
			t.Errorf("action ran %d times", am.created)
		}
	})

	t.Run("complete body whose action outlasts the deadline still succeeds", func(t *testing.T) {
		am := &fakeAM{}
		s := newServer(func(d *Deps) { d.AM = slowAM{fakeAM: am, delay: 500 * time.Millisecond} })
		s.bodyTimeout = 200 * time.Millisecond
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()
		body, _ := json.Marshal(silenceBody(nil))
		r, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/silences", bytes.NewReader(body))
		r.Host = testHost
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		r.Header.Set("X-Dash-Csrf", "1")
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK || am.created != 1 {
			t.Errorf("status = %d, created = %d, want 200 and 1", resp.StatusCode, am.created)
		}
	})
}

func TestLogsTailResume(t *testing.T) {
	t0 := fixedNow.Add(-30 * time.Second)
	at := func(d time.Duration) string { return t0.Add(d).UTC().Format(time.RFC3339Nano) }
	tests := []struct {
		name       string
		lastID     string // Last-Event-ID header; empty = not sent
		wantOffset time.Duration
		wantIDs    []string // ids of the delivered lines, in order
	}{
		{"no header is unchanged", "", 0, []string{at(-time.Second), at(0), at(time.Second), at(2 * time.Second)}},
		{"resume after t0", at(0), 35 * time.Second, []string{at(time.Second), at(2 * time.Second)}},
		{"resume window is capped", fixedNow.Add(-time.Hour).Format(time.RFC3339Nano), time.Minute, []string{at(-time.Second), at(0), at(time.Second), at(2 * time.Second)}},
		{"unparseable header is ignored", "garbage", 0, []string{at(-time.Second), at(0), at(time.Second), at(2 * time.Second)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := &fakeLogs{}
			logs.tail = func(emit func(model.LogLine) error) error {
				for _, d := range []time.Duration{-time.Second, 0, time.Second, 2 * time.Second} {
					if err := emit(model.LogLine{Time: t0.Add(d), Pod: "plex-0", Msg: "m"}); err != nil {
						return err
					}
				}
				return nil // the stream ends, which closes the response
			}
			s := newServer(func(d *Deps) { d.VLogs = logs })
			hdr := map[string]string{}
			if tc.lastID != "" {
				hdr["Last-Event-ID"] = tc.lastID
			}
			_, lines := sseClientWith(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0", hdr)
			var ids []string
			for l := range lines {
				if id, ok := strings.CutPrefix(l, "id: "); ok {
					ids = append(ids, id)
				}
			}
			if strings.Join(ids, ",") != strings.Join(tc.wantIDs, ",") {
				t.Errorf("ids = %v, want %v", ids, tc.wantIDs)
			}
			if logs.q.StartOffset != tc.wantOffset {
				t.Errorf("start offset = %v, want %v", logs.q.StartOffset, tc.wantOffset)
			}
		})
	}
}
