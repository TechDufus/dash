package api

// Tests that exist only for the legacy log routes (GET /api/logs, GET /api/logs/tail). The
// victorialogs plugin replaces them in P3.5, which deletes this file together with the routes
// (and the VLogs dep: fakeLogs is what newServer puts there). The generic stream behaviour they
// used to carry (caps, tailMax, write deadline, heartbeat, audit, metrics) is tested through a
// plugin stream in plugin_stream_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/upstream"
)

type fakeLogs struct {
	lines []model.LogLine
	q     upstream.LogQuery
	err   error
	tail  func(emit func(model.LogLine) error) error
}

func (f *fakeLogs) Query(_ context.Context, q upstream.LogQuery) ([]model.LogLine, error) {
	f.q = q
	return f.lines, f.err
}

func (f *fakeLogs) Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error {
	f.q = q
	if f.tail != nil {
		return f.tail(emit)
	}
	<-ctx.Done()
	return nil
}

func TestDetailsAndLogs(t *testing.T) {
	logs := &fakeLogs{}
	s := newServer(func(d *Deps) { d.VLogs = logs })
	h := s.Handler()

	if w := (req{path: "/api/logs?ns=media&pod=plex-0&since=1h&q=err"}).do(h); w.Code != 200 {
		t.Errorf("code = %d, want 200", w.Code)
	}
	if want := (upstream.LogQuery{Namespace: "media", Pod: "plex-0", Text: "err", Since: "1h"}); logs.q != want {
		t.Errorf("log query = %+v, want %+v", logs.q, want)
	}

	t.Run("logs are always an array", func(t *testing.T) {
		w := req{path: "/api/logs?namespace=media"}.do(h)
		if w.Body.String() != "[]\n" {
			t.Errorf("body = %q", w.Body.String())
		}
	})
	t.Run("invalid logs query is 400, upstream failure 502", func(t *testing.T) {
		logs.err = upstream.ErrInvalid
		if w := (req{path: "/api/logs?ns=media"}).do(h); w.Code != 400 {
			t.Errorf("code = %d, want 400", w.Code)
		}
		logs.err = context.DeadlineExceeded
		if w := (req{path: "/api/logs?ns=media"}).do(h); w.Code != 502 {
			t.Errorf("code = %d, want 502", w.Code)
		}
	})
	t.Run("tail validates before streaming", func(t *testing.T) {
		w := req{path: "/api/logs/tail?pod=x"}.do(h) // namespace missing
		if w.Code != 400 || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("code = %d type = %q", w.Code, w.Header().Get("Content-Type"))
		}
	})
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

// Log lines are application output: whatever a pod prints, a credential never reaches the
// browser, as an exact value, in an encoded form, or as a dash token (P1.4 acceptance).
func TestLogsAPIScrubsSecretsAndTokens(t *testing.T) {
	reg := canaryRegistry()
	var msgs []string
	for i, form := range canaryForms() {
		msgs = append(msgs, fmt.Sprintf("line %d token=%s end", i, form))
	}
	msgs = append(msgs,
		"pat dash_pat_abcDEF123_secretPART456 end",
		"setup dash_setup_Zm9vYmFyYmF6 end",
		"access dash_at_abc.def end",
		"sealed enc:v1:1a2b3c4d:AAAAAAAAAAAAAAAA end",
		"plain line stays",
	)
	var lines []model.LogLine
	for i, m := range msgs {
		lines = append(lines, model.LogLine{Time: fixedNow.Add(time.Duration(i) * time.Second), Pod: "p", Container: "c", Msg: m})
	}
	forbidden := append(canaryForms(), "dash_pat_abcDEF123", "secretPART456", "dash_setup_Zm9v", "dash_at_abc", "enc:v1:1a2b3c4d", "AAAAAAAAAAAAAAAA")

	check := func(t *testing.T, name, out string) {
		t.Helper()
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Errorf("%s carries %q:\n%s", name, f, out)
			}
		}
		if !strings.Contains(out, "plain line stays") || strings.Count(out, "[redacted]") < len(msgs)-1 {
			t.Errorf("%s: want every secret line redacted and the plain one kept:\n%s", name, out)
		}
	}

	t.Run("query", func(t *testing.T) {
		logs := &fakeLogs{lines: lines}
		s := newServer(func(d *Deps) { d.VLogs, d.Secrets = logs, reg })
		w := req{path: "/api/logs?ns=media&pod=p"}.do(s.Handler())
		if w.Code != 200 {
			t.Fatalf("code = %d", w.Code)
		}
		check(t, "/api/logs", w.Body.String())
	})
	t.Run("tail", func(t *testing.T) {
		logs := &fakeLogs{tail: func(emit func(model.LogLine) error) error {
			for _, l := range lines {
				if err := emit(l); err != nil {
					return err
				}
			}
			return nil
		}}
		s := newServer(func(d *Deps) { d.VLogs, d.Secrets = logs, reg })
		_, stream := sseClient(t, s.Handler(), "/api/logs/tail?ns=media&pod=p")
		var out strings.Builder
		for l := range stream {
			out.WriteString(l + "\n")
		}
		check(t, "/api/logs/tail", out.String())
	})
}

// The legacy tail has its own gauge while it is open.
func TestLegacyLogsTailIsCountedWithMetricsOn(t *testing.T) {
	m := obs.New("test", RoutePatterns(), nil)
	s := newServer(func(d *Deps) { d.Metrics = m })
	_, lines := sseClient(t, s.Handler(), "/api/logs/tail?ns=media&pod=plex-0")
	expect(t, lines, "retry: 3000")
	ss := scrapeMetrics(t, s)
	if got, ok := seriesValue(t, ss, "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); !ok || got != 1 {
		t.Errorf("open log tails = %v (present %v), want 1", got, ok)
	}
}
