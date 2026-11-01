package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// The core mounting of plugin streams (design 1.4), through the real chain: request id, host
// check, CSRF, auth, mux, the route guard, the supervisor's router, the stream pipeline.

const tailPath = "/api/v1/plugins/rt/tail?ns=media&pod=plex-0"

// recordsOf returns the audit records of kind k.
func recordsOf(recs []audit.Record, k audit.Kind) []audit.Record {
	var out []audit.Record
	for _, r := range recs {
		if r.Kind == k {
			out = append(out, r)
		}
	}
	return out
}

func TestPluginStreamPerUserCapIs429AuditedAndTheSlotComesBack(t *testing.T) {
	h := newHarness(t)
	h.tails = newStreamLimiter(1, 10)

	first := h.openStream(t, tailPath, "alice", nil)
	if first.status() != http.StatusOK {
		t.Fatalf("first open = %d", first.status())
	}
	first.expect("retry: 3000")

	second := h.openStream(t, tailPath, "alice", nil)
	if second.status() != http.StatusTooManyRequests || second.resp.Header.Get("Retry-After") == "" {
		t.Fatalf("second open = %d, Retry-After %q; want 429 with a hint", second.status(), second.resp.Header.Get("Retry-After"))
	}
	var e model.APIError
	if err := json.Unmarshal([]byte(second.errorBody()), &e); err != nil || e.Code != model.ErrCodeTooManyStreams || e.RequestID == "" {
		t.Errorf("429 body = %+v (%v), want a too_many_streams envelope with the request id", e, err)
	}

	// The refusal is audited, once, as a denial; it is not an open.
	recs := h.audit(t)
	denials := recordsOf(recs, audit.KindDenial)
	if len(denials) != 1 || denials[0].Reason != obs.AuthStreamLimit.String() || denials[0].Actor.User != "alice" {
		t.Errorf("denials = %+v, want one stream_limit for alice", denials)
	}
	if opens := recordsOf(recs, audit.KindEvent); len(opens) != 1 {
		t.Errorf("open records = %d, want 1 (the refused open must not be audited as an open)", len(opens))
	}
	if v, ok := seriesValue(t, h.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "stream_limit"}); !ok || v != 1 {
		t.Errorf("stream_limit failures = %v (present %v), want 1", v, ok)
	}

	// The cap is per user.
	bob := h.openStream(t, tailPath, "bob", nil)
	if bob.status() != http.StatusOK {
		t.Errorf("bob's open = %d, want 200", bob.status())
	}

	// When alice's client leaves, her slot comes back.
	first.close()
	h.eventually(t, "alice's slot to be released", func() bool { return h.openTails() == 1 })
	again := h.openStream(t, tailPath, "alice", nil)
	if again.status() != http.StatusOK {
		t.Fatalf("open after the first left = %d, want 200", again.status())
	}
	again.expect("retry: 3000")
}

func TestPluginStreamTotalCapIs503(t *testing.T) {
	h := newHarness(t)
	h.tails = newStreamLimiter(5, 1)

	first := h.openStream(t, tailPath, "alice", nil)
	first.expect("retry: 3000")
	second := h.openStream(t, tailPath, "bob", nil)
	if second.status() != http.StatusServiceUnavailable || second.resp.Header.Get("Retry-After") == "" {
		t.Fatalf("open past the total = %d, want 503 with Retry-After", second.status())
	}
	var e model.APIError
	if err := json.Unmarshal([]byte(second.errorBody()), &e); err != nil || e.Code != model.ErrCodeTooManyStreams {
		t.Errorf("503 body = %+v (%v)", e, err)
	}
	if d := recordsOf(h.audit(t), audit.KindDenial); len(d) != 1 || d[0].Reason != "stream_limit" || d[0].Actor.User != "bob" {
		t.Errorf("denials = %+v, want one stream_limit for bob", d)
	}
}

func TestPluginStreamEndsAtTailMaxAndJustCloses(t *testing.T) {
	h := newHarness(t)
	h.tailMax = 50 * time.Millisecond

	s := h.openStream(t, tailPath, "alice", nil)
	if s.status() != http.StatusOK {
		t.Fatalf("open = %d", s.status())
	}
	// Nothing but the retry hint: the browser sees a closed stream and its EventSource reconnects.
	lines := s.all()
	for _, l := range lines {
		if l != "retry: 3000" && l != "" {
			t.Errorf("line %q at the end of a stream that ran out of time: it just closes", l)
		}
	}
	h.eventually(t, "the slot to be released", func() bool { return h.openTails() == 0 })
}

func TestPluginStreamHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.heartbeat = 20 * time.Millisecond
	s := h.openStream(t, tailPath, "alice", nil)
	s.expect("retry: 3000")
	s.expect(": ping")
	s.expect(": ping")
}

func TestPluginStreamIsCountedAsATail(t *testing.T) {
	h := newHarness(t)
	s := h.openStream(t, tailPath, "alice", nil)
	s.expect("retry: 3000")
	if v, ok := seriesValue(t, h.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); !ok || v != 1 {
		t.Errorf("open tails = %v (present %v), want 1", v, ok)
	}
	if v, _ := seriesValue(t, h.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamEvents}); v != 0 {
		t.Errorf("open event streams = %v, want 0", v)
	}
	s.close()
	h.eventually(t, "the gauge to go back to 0", func() bool {
		v, _ := seriesValue(t, h.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamTail})
		return v == 0
	})
}

// A deadline recorder: the write deadlines the stream sets through http.ResponseController.
func TestPluginStreamWritesUnderADeadlineAndTheLastOneIsResetByDone(t *testing.T) {
	h := newHarness(t)
	h.sseWrite = 3 * time.Second
	h.heartbeat = time.Hour // no heartbeat: every write below is one of ours
	h.tail = func(ctx context.Context, _ *http.Request, _ auth.Principal, s plugin.Stream) error {
		if err := s.Begin(); err != nil {
			return err
		}
		if err := s.Event("line", "1", map[string]string{"msg": "hello"}); err != nil {
			return err
		}
		select { // the handler outlives its last write
		case <-ctx.Done():
		case <-time.After(80 * time.Millisecond):
		}
		return nil
	}

	d := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest(http.MethodGet, tailPath, nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
	start := time.Now()
	h.Handler().ServeHTTP(d, r)
	end := time.Now()

	body := d.Body.String()
	writes := strings.Count(body, "\n\n") // the retry hint and the event
	if writes != 2 || !strings.Contains(body, "id: 1\nevent: line\ndata: {\"msg\":\"hello\"}\n\n") {
		t.Fatalf("body = %q, want the retry hint and one event", body)
	}
	if len(d.write) != writes+1 {
		t.Fatalf("write deadlines = %d, want one per write (%d) and the closing one", len(d.write), writes)
	}
	for i, dl := range d.write {
		if dl.Before(start.Add(3*time.Second)) || dl.After(end.Add(3*time.Second)) {
			t.Errorf("write deadline %d is %v after the start, want the configured 3s", i, dl.Sub(start))
		}
	}
	// done() resets the deadline after the handler's idle time, so the closing chunk of
	// net/http is bounded by a fresh deadline, not by the one of the last event.
	if gap := d.write[len(d.write)-1].Sub(d.write[len(d.write)-2]); gap < 50*time.Millisecond {
		t.Errorf("the last deadline is only %v after the event's: done() did not reset it", gap)
	}
}

func TestPluginStreamAuditsTheOpenOnce(t *testing.T) {
	h := newHarness(t)
	var atEntry []audit.Record
	h.tail = func(ctx context.Context, _ *http.Request, _ auth.Principal, s plugin.Stream) error {
		atEntry = auditRecords(t, h.dir) // the record exists before the handler's first event
		if err := s.Begin(); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	s := h.openStream(t, tailPath, "alice", map[string]string{"X-Request-Id": "tail-42"})
	s.expect("retry: 3000")

	if len(atEntry) != 1 {
		t.Fatalf("records when the handler starts = %+v, want the open", atEntry)
	}
	recs := h.audit(t)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want exactly one per open", recs)
	}
	got := recs[0]
	want := audit.Record{
		Kind: audit.KindEvent, Action: "logs.tail", Plugin: "rt", Target: audit.Target{Kind: "Pod", Namespace: "media", Name: "plex-0"},
		RequestID: "tail-42", Actor: audit.Actor{Type: "proxy", User: "alice", Surface: "ui"}, AuthMode: "proxy",
	}
	if got.Kind != want.Kind || got.Action != want.Action || got.Plugin != want.Plugin || got.Target != want.Target ||
		got.RequestID != want.RequestID || got.Actor != want.Actor || got.AuthMode != want.AuthMode || got.Result != audit.ResultOK || got.Peer != "127.0.0.1" {
		t.Errorf("record = %+v, want %+v", got, want)
	}

	// A second open is a second record.
	h.openStream(t, tailPath, "alice", nil).expect("retry: 3000")
	if n := len(h.audit(t)); n != 2 {
		t.Errorf("records after two opens = %d, want 2", n)
	}
}

func TestPluginStreamOfAnotherScopeIsAuditedAsAPlainOpen(t *testing.T) {
	h := newHarness(t, harnessRoutes(func(h *harness, r plugin.Routes) {
		r.Stream("/feed", h.serveTail, plugin.StreamSpec{Summary: "Feed", Scope: "read", Target: podTarget, Namespace: nsQuery})
	}))
	h.openStream(t, "/api/v1/plugins/rt/feed?ns=media&pod=plex-0", "alice", nil).expect("retry: 3000")
	recs := h.audit(t)
	if len(recs) != 1 || recs[0].Action != "stream.open" || recs[0].Plugin != "rt" {
		t.Errorf("records = %+v, want one stream.open of rt", recs)
	}
}

// A denied namespace answers 403, not 429, even when no slot is free, writes no open record, and
// is itself audited once per peer and minute.
func TestPluginStreamDeniedNamespaceIs403BeforeAnySlotAndAuditedOncePerMinute(t *testing.T) {
	h := newHarness(t, harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{"kube-system"}}))
	h.tails = newStreamLimiter(0, 0) // every slot taken, for everybody

	denied := "/api/v1/plugins/rt/tail?ns=kube-system&pod=coredns"
	for i := range 2 {
		s := h.openStream(t, denied, "alice", nil)
		if s.status() != http.StatusForbidden {
			t.Fatalf("denied namespace #%d = %d, want 403 (not the 429 of the full limiter)", i, s.status())
		}
		var e model.APIError
		if err := json.Unmarshal([]byte(s.errorBody()), &e); err != nil || e.Code != model.ErrCodeForbidden || e.RequestID == "" {
			t.Errorf("403 body = %+v (%v)", e, err)
		}
	}
	recs := h.audit(t)
	if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != obs.AuthNamespaceDenied.String() || recs[0].Peer != "127.0.0.1" {
		t.Fatalf("records after two denials in a minute = %+v, want one namespace_denied", recs)
	}
	if v, _ := seriesValue(t, h.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "namespace_denied"}); v != 2 {
		t.Errorf("namespace_denied counted %v, want 2 (the log keeps one a minute, the metric counts every refusal)", v)
	}
	if v, _ := seriesValue(t, h.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "stream_limit"}); v != 0 {
		t.Errorf("stream_limit counted %v, the denial must come before the slot", v)
	}

	h.clk.advance(time.Minute + time.Second)
	h.openStream(t, denied, "alice", nil)
	if n := len(h.audit(t)); n != 2 {
		t.Errorf("records after the window = %d, want 2", n)
	}

	// An allowed namespace meets the full limiter: 429.
	if got := h.openStream(t, tailPath, "alice", nil).status(); got != http.StatusTooManyRequests {
		t.Errorf("allowed namespace with no slot = %d, want 429", got)
	}
}

func TestPluginStreamHandlerErrorBeforeBeginIsTheStandardEnvelope(t *testing.T) {
	const secretText = "tok3n-s3cr3t"
	reg := secret.NewRegistry()
	reg.Add(secret.New(secretText, ""))
	captureLog(t, reg)
	cases := []struct {
		name   string
		err    error
		status int
		code   string
		msg    string // the exact message
	}{
		{"an ActionError as written", &plugin.ActionError{Status: http.StatusConflict, Code: model.ErrCodeConflict, Message: "another tail is running"},
			http.StatusConflict, model.ErrCodeConflict, "another tail is running"},
		{"invalid input", fmt.Errorf("bad %s: %w", secretText, upstream.ErrInvalid), http.StatusBadRequest, model.ErrCodeInvalidInput, ""},
		{"anything else: the category only", errors.New(`Get "https://vlogs.internal.example.com:9428/select/logsql/tail": dial tcp 192.0.2.7:9428: connect: connection refused; token ` + secretText),
			http.StatusBadGateway, model.ErrCodeUpstreamUnavailable, "rt: unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.d.Secrets = reg
			h.tail = func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { return tc.err }
			w := h.get(tailPath)
			e := wantError(t, w, tc.status, tc.code)
			if id := w.Header().Get("X-Request-Id"); id == "" || e.RequestID != id {
				t.Errorf("requestId %q in the body, header %q", e.RequestID, id)
			}
			if tc.msg != "" && e.Error != tc.msg {
				t.Errorf("message = %q, want %q", e.Error, tc.msg)
			}
			body := bodyWithoutRequestID(w)
			for _, leak := range []string{secretText, "example.com", "192.0.2.7", "9428", "vlogs.internal"} {
				if strings.Contains(body, leak) {
					t.Errorf("body carries %q: %s", leak, body)
				}
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want the JSON of an error", ct)
			}
			// The open was audited: policy had passed when the handler failed.
			if n := len(recordsOf(h.audit(t), audit.KindEvent)); n != 1 {
				t.Errorf("open records = %d, want 1", n)
			}
		})
	}
}

func TestPluginStreamHandlerErrorAfterBeginJustClosesTheStream(t *testing.T) {
	h := newHarness(t)
	h.tail = func(_ context.Context, _ *http.Request, _ auth.Principal, s plugin.Stream) error {
		if err := s.Begin(); err != nil {
			return err
		}
		if err := s.Event("line", "", map[string]string{"msg": "one"}); err != nil {
			return err
		}
		return errors.New(`Get "https://vlogs.internal.example.com:9428/x": boom`)
	}
	s := h.openStream(t, tailPath, "alice", nil)
	if s.status() != http.StatusOK {
		t.Fatalf("status = %d, want the 200 the stream began with", s.status())
	}
	lines := s.all()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"msg":"one"`) {
		t.Errorf("lines = %q, want the event", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, "boom") || strings.Contains(l, "vlogs.internal") || strings.Contains(l, `"code"`) {
			t.Errorf("line %q: an error after Begin must not reach the stream", l)
		}
	}
	h.eventually(t, "the slot to be released", func() bool { return h.openTails() == 0 })
}

func TestPluginStreamHandlerSeesLastEventIDAndThePrincipal(t *testing.T) {
	h := newHarness(t)
	type seen struct {
		lastID string
		user   string
		role   auth.Role
		query  string
	}
	got := make(chan seen, 1)
	h.tail = func(ctx context.Context, r *http.Request, p auth.Principal, s plugin.Stream) error {
		got <- seen{r.Header.Get("Last-Event-ID"), p.User, p.Role, r.URL.Query().Get("pod")}
		if err := s.Begin(); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
	h.openStream(t, tailPath, "alice", map[string]string{"Last-Event-ID": "A=2026-10-30T12:00:00Z"}).expect("retry: 3000")
	select {
	case s := <-got:
		if s.lastID != "A=2026-10-30T12:00:00Z" || s.user != "alice" || s.role != auth.RoleAdmin || s.query != "plex-0" {
			t.Errorf("handler saw %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never ran")
	}
}

func TestPluginStreamEventWritesIDLines(t *testing.T) {
	h := newHarness(t)
	h.tail = func(_ context.Context, _ *http.Request, _ auth.Principal, s plugin.Stream) error {
		if err := s.Event("line", "x", 1); err == nil {
			t.Error("Event before Begin succeeded")
		}
		if err := s.Begin(); err != nil {
			return err
		}
		if err := s.Begin(); err == nil {
			t.Error("a second Begin succeeded")
		}
		if err := s.Event("line", "id-1", map[string]int{"n": 1}); err != nil {
			return err
		}
		return s.Event("line", "", map[string]int{"n": 2}) // no id: no id line
	}
	got := h.openStream(t, tailPath, "alice", nil).all()
	want := []string{
		"retry: 3000", "",
		"id: id-1", "event: line", `data: {"n":1}`, "",
		"event: line", `data: {"n":2}`, "",
	}
	if !slices.Equal(got, want) {
		t.Errorf("lines = %q\nwant    %q", got, want)
	}
}

// The core does not scrub what a plugin writes with Event: the plugin scrubs text before it builds
// the payload (Host.ScrubLog). Scrubbing the encoded JSON would miss a secret json.Marshal escapes.
func TestPluginStreamIsNotScrubbedByTheCoreButThePluginsHostScrubCatchesEscapedSecrets(t *testing.T) {
	const tricky = `pa&ss"w<rd>1`
	reg := secret.NewRegistry()
	reg.Add(secret.New(tricky, ""))

	h := newHarness(t, harnessScrub(reg.ScrubLogLine))
	h.d.Secrets = reg
	line := "token=" + tricky + " end"

	// What the plugin does right: scrub the text, then build the payload.
	h.tail = func(_ context.Context, _ *http.Request, _ auth.Principal, s plugin.Stream) error {
		if err := s.Begin(); err != nil {
			return err
		}
		host := h.rt.Host()
		if err := s.Event("line", "", map[string]string{"msg": host.ScrubLog(line)}); err != nil {
			return err
		}
		// What it must not do: the core's Event does not scrub, so this reaches the client as it is.
		return s.Event("line", "", map[string]string{"msg": line})
	}
	lines := h.openStream(t, tailPath, "alice", nil).all()
	data := sseData(t, lines)
	if len(data) != 2 {
		t.Fatalf("events = %v", lines)
	}
	if got := data[0]["msg"].(string); got != "token=[redacted] end" {
		t.Errorf("the plugin-side scrub left %q", got)
	}
	if got := data[1]["msg"].(string); got != line {
		t.Errorf("the core changed an event it must not touch: %q", got)
	}

	// Why the plugin scrubs before encoding: the encoded JSON no longer holds the secret.
	encoded, _ := json.Marshal(map[string]string{"msg": line})
	if strings.Contains(string(encoded), tricky) {
		t.Fatalf("the test secret survives json.Marshal unescaped: %s", encoded)
	}
	if after := reg.ScrubLogLine(string(encoded)); strings.Contains(after, "[redacted]") {
		t.Errorf("scrubbing the encoded JSON caught an escaped secret: %s", after)
	}
	if got := reg.ScrubLogLine(line); strings.Contains(got, tricky) {
		t.Errorf("ScrubLogLine missed the raw secret: %q", got)
	}

	// The same on a plain GET route: scrub the text, then encode.
	h.items = func(*http.Request) (any, error) {
		return []map[string]string{{"msg": h.rt.Host().ScrubLog(line)}}, nil
	}
	body := h.get("/api/v1/plugins/rt/items?ns=media").Body.String()
	if strings.Contains(body, tricky) || strings.Contains(body, `pa\u0026ss`) || !strings.Contains(body, "token=[redacted] end") {
		t.Errorf("GET body = %s", body)
	}
}

// A panic before the first byte is the recovered 500 of every route.
func TestPluginStreamPanicBeforeBeginAnswers500(t *testing.T) {
	h := newHarness(t)
	h.tail = func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { panic("kaboom in a stream") }
	w := h.get(tailPath)
	wantInternal(t, "stream", w)
	h.eventually(t, "the slot to be released after the panic", func() bool { return h.openTails() == 0 })
	if v, _ := seriesValue(t, h.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); v != 0 {
		t.Errorf("open tails after a panic = %v", v)
	}
}

// The role check is the first step of the pipeline, ahead of the namespace policy and the slot.
func TestPluginRouteGuardRefusesAPrincipalBelowViewerWithoutTakingASlot(t *testing.T) {
	h := newHarness(t)
	infos := h.sup.RouteInfos("rt")
	i := slices.IndexFunc(infos, func(ri plugin.RouteInfo) bool { return ri.Stream != nil })
	if i < 0 {
		t.Fatal("rt has no stream")
	}
	mr := plugin.MountedRoute{Instance: "rt", Info: infos[i]}
	r := httptest.NewRequest(http.MethodGet, tailPath, nil) // no principal in the context
	w := httptest.NewRecorder()
	var mu sync.Mutex
	called := false
	h.Server.serveRoute(w, r, mr, func(http.ResponseWriter, *http.Request) { mu.Lock(); called = true; mu.Unlock() })
	wantError(t, w, http.StatusForbidden, model.ErrCodeForbidden)
	if called || h.openTails() != 0 || len(recordsOf(h.audit(t), audit.KindEvent)) != 0 {
		t.Errorf("called %v, open tails %d", called, h.openTails())
	}
}

// A stream ends with the plugin instance that serves it: a reconfiguration (here a new deny
// list) or a disable stops the plugin, and an open tail must not run on for tailMax against it.
func TestPluginStreamEndsWhenItsPluginIsReconfigured(t *testing.T) {
	h := newHarness(t)
	h.tailMax = time.Hour
	s := h.openStream(t, tailPath, "alice", nil)
	s.expect("retry: 3000")

	h.set.Set(plugin.InstanceConfig{Name: "rt", ReadDenyNamespaces: []string{"media"}})
	h.sup.Reload()
	s.all() // fails if the stream is still open after a few seconds
	h.eventually(t, "the slot to be released", func() bool { return h.openTails() == 0 })
	if w := h.get(tailPath); w.Code != http.StatusForbidden {
		t.Errorf("a new open for the denied namespace = %d, want 403", w.Code)
	}
}

func TestPluginStreamEndsWhenItsPluginIsDisabled(t *testing.T) {
	h := newHarness(t)
	h.tailMax = time.Hour
	s := h.openStream(t, tailPath, "alice", nil)
	s.expect("retry: 3000")

	h.set.Set(plugin.InstanceConfig{Name: "rt", Enabled: new(false)})
	h.sup.Reload()
	s.all()
	h.eventually(t, "the slot to be released", func() bool { return h.openTails() == 0 })
	if w := h.get(tailPath); w.Code != http.StatusNotFound {
		t.Errorf("a new open of the disabled plugin = %d, want 404", w.Code)
	}
}
