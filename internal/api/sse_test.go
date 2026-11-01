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
	expect(t, lines, "event: core.overview") // snapshot
	if _, err := s.d.Hub.Publish(model.TopicOverview, map[string]string{"a": "2"}, []model.SourceStatus{}); err != nil {
		t.Fatal(err)
	}
	expect(t, lines, `event: core.overview`) // the live event follows its own event line
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
	_, lines := sseClient(t, h, "/api/stream") // alice's only slot
	expect(t, lines, "retry: 3000")
	if got := streamStatus(t, h, "/api/stream", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("second stream for alice = %d, want 429", got)
	}
	// The cap is per user: bob still gets his own.
	if got := streamStatus(t, h, "/api/stream", "bob"); got != http.StatusOK {
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
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/stream", nil)
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
	t0 := fixedNow.Add(-30 * time.Second) // T, the newest time the client saw from container A
	type line struct {
		container string
		at        time.Duration // relative to T
		msg       string
	}
	// ids builds the expected Last-Event-ID/id value.
	ids := func(m map[string]time.Duration) string {
		p := resumePoints{}
		for c, d := range m {
			p[c] = t0.Add(d)
		}
		return p.String()
	}
	a1 := line{"A", -time.Second, "a1"}
	plain := []string{"a1"}
	tests := []struct {
		name       string
		header     string // Last-Event-ID; empty = not sent
		lines      []line
		wantMsgs   []string
		wantLastID string
		wantOffset time.Duration
	}{
		{"no header is a plain tail", "", []line{a1, {"B", 0, "b1"}},
			[]string{"a1", "b1"}, ids(map[string]time.Duration{"A": -time.Second, "B": 0}), 0},
		{"each container resumes from its own point",
			ids(map[string]time.Duration{"A": 0, "B": -10 * time.Second}),
			[]line{{"B", -5 * time.Second, "b-late"}, {"A", -time.Second, "a-old"}, {"A", time.Second, "a-new"}, {"C", -20 * time.Second, "c-new"}},
			[]string{"b-late", "a-new", "c-new"},
			ids(map[string]time.Duration{"A": time.Second, "B": -5 * time.Second, "C": -20 * time.Second}),
			45 * time.Second}, // from B, the oldest point: 40s + 5s
		{"a quiet container keeps its point in later ids",
			ids(map[string]time.Duration{"A": 0, "Q": -3 * time.Second}),
			[]line{{"A", time.Second, "a-new"}},
			[]string{"a-new"},
			ids(map[string]time.Duration{"A": time.Second, "Q": -3 * time.Second}),
			38 * time.Second},
		{"the empty container name is a valid key",
			ids(map[string]time.Duration{"": 0}),
			[]line{{"", -time.Second, "old"}, {"", time.Second, "new"}},
			[]string{"new"}, ids(map[string]time.Duration{"": time.Second}), 35 * time.Second},
		{"container names are escaped",
			ids(map[string]time.Duration{"a b,c=d": 0}),
			[]line{{"a b,c=d", -time.Second, "old"}, {"a b,c=d", time.Second, "new"}},
			[]string{"new"}, ids(map[string]time.Duration{"a b,c=d": time.Second}), 35 * time.Second},
		{"the resume window is capped",
			ids(map[string]time.Duration{"A": -time.Hour}),
			[]line{a1}, plain, ids(map[string]time.Duration{"A": -time.Second}), time.Minute},
	}
	var tooMany []string // one entry past the cap is never written by dash, so it is not a valid id
	for i := 0; i <= maxResumePoints; i++ {
		tooMany = append(tooMany, fmt.Sprintf("c%02d=%s", i, t0.Format(time.RFC3339Nano)))
	}
	// Anything malformed means no resume at all: no filter and no start offset.
	for _, bad := range []string{
		strings.Join(tooMany, ","),
		"garbage",
		t0.Format(time.RFC3339Nano), // a bare timestamp, no container
		"A=garbage",
		"A=" + t0.Format(time.RFC3339Nano) + ",B=garbage", // one bad entry spoils the id
		"A=" + t0.Format(time.RFC3339Nano) + ",,B=" + t0.Format(time.RFC3339Nano),
		"A=" + t0.Format(time.RFC3339Nano) + ",A=" + t0.Format(time.RFC3339Nano),
		"%zz=" + t0.Format(time.RFC3339Nano),
	} {
		tests = append(tests, struct {
			name       string
			header     string
			lines      []line
			wantMsgs   []string
			wantLastID string
			wantOffset time.Duration
		}{"malformed id " + bad, bad, []line{{"A", -time.Second, "a1"}}, plain, ids(map[string]time.Duration{"A": -time.Second}), 0})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := &fakeLogs{}
			logs.tail = func(emit func(model.LogLine) error) error {
				for _, l := range tc.lines {
					if err := emit(model.LogLine{Time: t0.Add(l.at), Pod: "plex-0", Container: l.container, Msg: l.msg}); err != nil {
						return err
					}
				}
				return nil // the stream ends, which closes the response
			}
			s := newServer(func(d *Deps) { d.VLogs = logs })
			hdr := map[string]string{}
			if tc.header != "" {
				hdr["Last-Event-ID"] = tc.header
			}
			_, lines := sseClientWith(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0", hdr)
			var msgs []string
			var lastID string
			for l := range lines {
				if id, ok := strings.CutPrefix(l, "id: "); ok {
					lastID = id
				}
				if d, ok := strings.CutPrefix(l, "data: "); ok {
					var ll model.LogLine
					if err := json.Unmarshal([]byte(d), &ll); err != nil {
						t.Fatal(err)
					}
					msgs = append(msgs, ll.Msg)
				}
			}
			if strings.Join(msgs, ",") != strings.Join(tc.wantMsgs, ",") {
				t.Errorf("delivered %v, want %v", msgs, tc.wantMsgs)
			}
			if lastID != tc.wantLastID {
				t.Errorf("last id = %q, want %q", lastID, tc.wantLastID)
			}
			if logs.q.StartOffset != tc.wantOffset {
				t.Errorf("start offset = %v, want %v", logs.q.StartOffset, tc.wantOffset)
			}
		})
	}
}

// A tail remembers at most maxResumePoints containers; the oldest point is evicted first.
func TestLogsTailResumeCapsPoints(t *testing.T) {
	t0 := fixedNow.Add(-30 * time.Second)
	seed := resumePoints{}
	for i := range maxResumePoints {
		seed[fmt.Sprintf("c%02d", i)] = t0.Add(time.Duration(i-20) * time.Second) // c00 is the oldest
	}
	logs := &fakeLogs{}
	logs.tail = func(emit func(model.LogLine) error) error {
		return emit(model.LogLine{Time: t0.Add(time.Second), Container: "x", Msg: "new"})
	}
	s := newServer(func(d *Deps) { d.VLogs = logs })
	_, lines := sseClientWith(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0", map[string]string{"Last-Event-ID": seed.String()})
	var lastID string
	for l := range lines {
		if id, ok := strings.CutPrefix(l, "id: "); ok {
			lastID = id
		}
	}
	got, ok := parseResumePoints(lastID)
	if !ok {
		t.Fatalf("id %q does not parse", lastID)
	}
	if len(got) != maxResumePoints {
		t.Errorf("%d points in the id, want %d", len(got), maxResumePoints)
	}
	if _, ok := got["c00"]; ok {
		t.Error("the oldest point c00 was kept")
	}
	if _, ok := got["x"]; !ok {
		t.Error("the new container x is missing")
	}
	if want := 55 * time.Second; logs.q.StartOffset != want { // from c00: 50s + 5s
		t.Errorf("start offset = %v, want %v", logs.q.StartOffset, want)
	}
}

func TestResumePoints(t *testing.T) {
	ts := time.Date(2026, 10, 30, 12, 0, 0, 5, time.UTC)
	p := resumePoints{"b": ts, "": ts.Add(time.Second), "a b": ts}
	const want = "=2026-10-30T12:00:01.000000005Z,a+b=2026-10-30T12:00:00.000000005Z,b=2026-10-30T12:00:00.000000005Z"
	if got := p.String(); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	back, ok := parseResumePoints(want)
	if !ok || len(back) != 3 || !back["a b"].Equal(ts) || !back[""].Equal(ts.Add(time.Second)) {
		t.Errorf("round trip = %v, %v", back, ok)
	}
	// note keeps the newest time per container and never goes backwards.
	q := resumePoints{}
	q.note("a", ts)
	q.note("a", ts.Add(-time.Second))
	if !q["a"].Equal(ts) {
		t.Errorf("note moved a back to %v", q["a"])
	}
	// Over the cap, the entry with the oldest time goes, whichever container it is.
	for i := range maxResumePoints {
		q.note(fmt.Sprintf("c%02d", i), ts.Add(time.Duration(i+1)*time.Second))
	}
	if len(q) != maxResumePoints {
		t.Fatalf("%d points, want %d", len(q), maxResumePoints)
	}
	if _, ok := q["a"]; ok {
		t.Error("the oldest point a was kept")
	}
	if _, ok := q["c15"]; !ok {
		t.Error("the newest point c15 was evicted")
	}
}
