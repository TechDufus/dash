package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/topics"
)

// sseClient opens path on a real server and delivers its lines on a channel.
func sseClient(t *testing.T, h http.Handler, path string) (*http.Response, <-chan string) {
	t.Helper()
	ts := httptest.NewServer(h)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ts.Close() })
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
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
	_, lines := sseClient(t, h, "/api/stream") // alice's slot, held until cleanup
	expect(t, lines, "retry: 3000")
	if got := streamStatus(t, h, "/api/stream", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("second stream for alice = %d, want 429", got)
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

// deadlineWriter records the deadlines set through http.ResponseController.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	write, read []time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.write = append(d.write, t)
	return nil
}

func (d *deadlineWriter) SetReadDeadline(t time.Time) error {
	d.read = append(d.read, t)
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
	if last := d.write[len(d.write)-1]; !last.IsZero() {
		t.Errorf("deadline after done = %v, want zero", last)
	}

	var v struct{}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	if !decodeBody(d, r, &v) {
		t.Fatal("decodeBody failed")
	}
	if len(d.read) != 2 || d.read[0].IsZero() || !d.read[1].IsZero() {
		t.Errorf("read deadlines = %v, want a non-zero one then a zero one", d.read)
	}
}
