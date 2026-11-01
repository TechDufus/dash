package victorialogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

func defaults(url string) *pluginconf.VictoriaLogs {
	c := pluginconf.DefaultVictoriaLogs()
	c.URL = url
	return &c
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "victorialogs" || m.Name != "VictoriaLogs" || m.Version != "1.0.0" || m.Description == "" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Topics) != 0 || len(m.Actions) != 0 || len(m.Series) != 0 || len(m.Requires) != 0 {
		t.Errorf("the plugin publishes nothing and has no action: %+v", m)
	}
	if _, ok := m.Config().(*pluginconf.VictoriaLogs); !ok {
		t.Errorf("Config() = %T", m.Config())
	}
}

func TestConfiguredNeedsAURL(t *testing.T) {
	p := &Plugin{}
	if p.Configured(defaults("")) || !p.Configured(defaults("http://logs.example.com:9428")) {
		t.Error("Configured must follow the URL")
	}
	if !(&Plugin{demo: true}).Configured(defaults("")) {
		t.Error("the twin needs no URL")
	}
}

func TestValidateRejectsUnsafeFieldNamesAndALimitOfZero(t *testing.T) {
	p := &Plugin{}
	if err := p.Validate(defaults("http://x")); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for _, mod := range []func(*pluginconf.VictoriaLogs){
		func(c *pluginconf.VictoriaLogs) { c.Fields.Pod = `pod" or _msg:*` },
		func(c *pluginconf.VictoriaLogs) { c.Fields.Namespace = "" },
		func(c *pluginconf.VictoriaLogs) { c.Fields.Container = "a b" },
		func(c *pluginconf.VictoriaLogs) { c.Limit = 0 },
	} {
		c := defaults("http://x")
		mod(c)
		if err := p.Validate(c); err == nil {
			t.Errorf("%+v accepted", c.Fields)
		}
	}
	if err := p.Validate("not a config"); err == nil {
		t.Error("a foreign config type was accepted")
	}
}

// Visibility: rule (a). The plugin has no topic, so this asserts that its row turns visible when
// the URL appears, and that Start brings it up healthy against a real client.
func TestVisibilityRuleA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	plugintest.AssertTopicsReachOpenStream(t, New,
		plugin.InstanceConfig{Config: defaults("")},
		plugin.InstanceConfig{Config: defaults(srv.URL)})
}

// A configured plugin runs healthy and its twin runs with no configuration at all.
func TestStatusAndDemoTwin(t *testing.T) {
	p := New().(*Plugin)
	if got := p.Status().State; got != plugin.StateStarting {
		t.Errorf("before Start: %s", got)
	}
	twin, ok := New().(plugin.Demo)
	if !ok {
		t.Fatal("the plugin has no demo twin")
	}
	tw := twin.Demo()
	if tw.Manifest().ID != "victorialogs" {
		t.Errorf("twin manifest = %+v", tw.Manifest())
	}
	if _, ok := tw.(plugin.Router); !ok {
		t.Error("the twin serves no routes")
	}
}

func TestAuthorizeSetsBasicOrBearerPerRequest(t *testing.T) {
	newReq := func() *http.Request { return httptest.NewRequest(http.MethodGet, "http://x/", nil) }

	var a pluginconf.Auth
	a.Basic.Username, a.Basic.Password = "reader", secret.New("pw", "")
	r := newReq()
	authorize(a)(r)
	if u, pw, ok := r.BasicAuth(); !ok || u != "reader" || pw != "pw" {
		t.Errorf("basic = %v %v %v", u, pw, ok)
	}

	b := pluginconf.Auth{BearerToken: secret.New("tok-1", "")}
	r = newReq()
	authorize(b)(r)
	if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
		t.Errorf("bearer = %q", got)
	}
	b.BearerToken.Update("tok-2") // a rotation applies at the next request
	r = newReq()
	authorize(b)(r)
	if got := r.Header.Get("Authorization"); got != "Bearer tok-2" {
		t.Errorf("rotated bearer = %q", got)
	}

	r = newReq()
	authorize(pluginconf.Auth{})(r)
	if r.Header.Get("Authorization") != "" {
		t.Error("no credentials configured, yet a header was sent")
	}
}

// ---- routes ----

// fakeSource is the upstream client of a test.
type fakeSource struct {
	mu    sync.Mutex
	lines []model.LogLine
	q     upstream.LogQuery
	err   error
	tail  func(emit func(model.LogLine) error) error
}

func (f *fakeSource) Query(_ context.Context, q upstream.LogQuery) ([]model.LogLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.q = q
	return f.lines, f.err
}

func (f *fakeSource) Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error {
	f.mu.Lock()
	f.q = q
	tail := f.tail
	f.mu.Unlock()
	if tail != nil {
		return tail(emit)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeSource) query() upstream.LogQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.q
}

// fakeStream is the plugin.Stream of a test: Event encodes as the core does and records what it
// wrote.
type fakeStream struct {
	began    bool
	beginErr error
	events   []sseEvent
}

type sseEvent struct {
	name, id string
	line     model.LogLine
	raw      string
}

func (s *fakeStream) Begin() error {
	s.began = true
	return s.beginErr
}

func (s *fakeStream) Event(event, id string, data any) error {
	if !s.began {
		return errors.New("stream not begun")
	}
	b, err := jsonOf(data)
	if err != nil {
		return err
	}
	l, _ := data.(model.LogLine)
	s.events = append(s.events, sseEvent{name: event, id: id, line: l, raw: string(b)})
	return nil
}

func (s *fakeStream) msgs() []string {
	var out []string
	for _, e := range s.events {
		out = append(out, e.line.Msg)
	}
	return out
}

func (s *fakeStream) lastID() string {
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i].id != "" {
			return s.events[i].id
		}
	}
	return ""
}

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)

// started is a plugin whose running state serves src, as Start would build it.
func started(src source, scrub func(string) string) *Plugin {
	if scrub == nil {
		scrub = func(s string) string { return s }
	}
	p := &Plugin{}
	p.st.Store(&running{src: src, fields: upstream.DefaultLogFields(), scrub: scrub, now: func() time.Time { return testNow }})
	return p
}

func tailReq(header map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/logs/tail?ns=media&pod=plex-0", nil)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	return r
}

func TestRoutesAreDeclaredWithTheNamespaceAndTheLogsScope(t *testing.T) {
	routes := plugin.CollectRoutes(New().(*Plugin))
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want logs and logs/tail", len(routes))
	}
	byPattern := map[string]plugin.RouteInfo{}
	for _, ri := range routes {
		byPattern[ri.Pattern] = ri
		if ri.Spec.Scope != "logs" {
			t.Errorf("%s scope = %q, want logs", ri.Pattern, ri.Spec.Scope)
		}
		for _, target := range []string{"/x?ns=media", "/x?namespace=media"} {
			if got := ri.Spec.Namespace(httptest.NewRequest(http.MethodGet, target, nil)); got != "media" {
				t.Errorf("%s namespace of %s = %q", ri.Pattern, target, got)
			}
		}
		if got := ri.Spec.Namespace(httptest.NewRequest(http.MethodGet, "/x", nil)); got != "" {
			t.Errorf("%s namespace without a parameter = %q", ri.Pattern, got)
		}
	}
	if byPattern["/logs"].Handler == nil || byPattern["/logs"].Stream != nil {
		t.Errorf("/logs must be a plain GET: %+v", byPattern["/logs"])
	}
	tail := byPattern["/logs/tail"]
	if tail.Stream == nil || tail.StreamHandler == nil || tail.Stream.Scope != "logs" {
		t.Fatalf("/logs/tail must be a stream with the logs scope: %+v", tail)
	}
	kind, ns, name := tail.Stream.Target(httptest.NewRequest(http.MethodGet, "/x?ns=media&pod=plex-0", nil))
	if kind != "Pod" || ns != "media" || name != "plex-0" {
		t.Errorf("audit target = %s %s/%s, want Pod media/plex-0", kind, ns, name)
	}
}

func TestLogsQueryParametersAndAlwaysAnArray(t *testing.T) {
	src := &fakeSource{}
	p := started(src, nil)
	w := httptest.NewRecorder()
	p.logs(w, httptest.NewRequest(http.MethodGet, "/logs?ns=media&pod=plex-0&container=plex&since=1h&q=err", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code = %d type = %q", w.Code, w.Header().Get("Content-Type"))
	}
	if w.Body.String() != "[]\n" {
		t.Errorf("an empty result must be [], got %q", w.Body.String())
	}
	want := upstream.LogQuery{Namespace: "media", Pod: "plex-0", Container: "plex", Text: "err", Since: "1h"}
	if src.query() != want {
		t.Errorf("query = %+v, want %+v", src.query(), want)
	}

	// namespace is the alias of ns.
	w = httptest.NewRecorder()
	p.logs(w, httptest.NewRequest(http.MethodGet, "/logs?namespace=other", nil))
	if got := src.query().Namespace; got != "other" {
		t.Errorf("namespace alias gave %q", got)
	}
}

func TestLogsErrorsGoThroughFail(t *testing.T) {
	src := &fakeSource{err: upstream.ErrInvalid}
	p := started(src, nil)
	w := httptest.NewRecorder()
	p.logs(w, httptest.NewRequest(http.MethodGet, "/logs?ns=media", nil))
	// Outside the API plugin.Fail answers a plain 502 and never the error's text; the core's
	// mapping (400 for ErrInvalid, the category for the rest) is tested through the API.
	if w.Code != http.StatusBadGateway {
		t.Errorf("code = %d", w.Code)
	}

	// A stopped instance answers 503 as the plugin wrote it.
	p.st.Store(nil)
	w = httptest.NewRecorder()
	p.logs(w, httptest.NewRequest(http.MethodGet, "/logs?ns=media", nil))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), model.ErrCodeUpstreamUnavailable) {
		t.Errorf("stopped: %d %s", w.Code, w.Body)
	}
}

func TestTailStreamsLinesAndEndsWhenTheContextDoes(t *testing.T) {
	src := &fakeSource{}
	src.tail = func(emit func(model.LogLine) error) error {
		return emit(model.LogLine{Time: testNow, Pod: "plex-0", Container: "plex", Msg: "hello"})
	}
	p := started(src, nil)
	s := &fakeStream{}
	if err := p.tail(context.Background(), tailReq(nil), authAnon, s); err != nil {
		t.Fatal(err)
	}
	if !s.began || len(s.events) != 1 || s.events[0].name != "line" || s.events[0].line.Msg != "hello" {
		t.Fatalf("stream = %+v", s)
	}
	if q := src.query(); q.Namespace != "media" || q.Pod != "plex-0" {
		t.Errorf("query = %+v", q)
	}

	// With the default fake the upstream holds the stream until ctx ends; the end is a nil
	// return, not an error, whether the client left or the maximum duration passed.
	block := started(&fakeSource{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := block.tail(ctx, tailReq(nil), authAnon, &fakeStream{}); err != nil {
		t.Errorf("a tail ended by its context returned %v", err)
	}
}

func TestTailValidatesBeforeBegin(t *testing.T) {
	p := started(&fakeSource{}, nil)
	s := &fakeStream{}
	r := httptest.NewRequest(http.MethodGet, "/logs/tail?pod=x", nil) // namespace missing
	err := p.tail(context.Background(), r, authAnon, s)
	if !errors.Is(err, upstream.ErrInvalid) || s.began {
		t.Errorf("err = %v, began = %v: want ErrInvalid before the stream starts", err, s.began)
	}
	s = &fakeStream{}
	r = httptest.NewRequest(http.MethodGet, "/logs/tail?ns=media&since=2d", nil)
	if err := p.tail(context.Background(), r, authAnon, s); !errors.Is(err, upstream.ErrInvalid) || s.began {
		t.Errorf("bad since: err = %v, began = %v", err, s.began)
	}
	// A stopped instance is a refusal before Begin too, as the plugin wrote it.
	p.st.Store(nil)
	var ae *plugin.ActionError
	if err := p.tail(context.Background(), tailReq(nil), authAnon, &fakeStream{}); !errors.As(err, &ae) || ae.Status != http.StatusServiceUnavailable {
		t.Errorf("stopped tail err = %v", err)
	}
}

// The stream's Validate is what answers a bad query 400 before the core takes a slot or writes
// the audit record: it follows the configured field mapping and never touches the upstream.
func TestTailSpecValidateRefusesABadQuery(t *testing.T) {
	src := &fakeSource{}
	p := started(src, nil)
	var spec *plugin.StreamSpec
	for _, ri := range plugin.CollectRoutes(p) {
		if ri.Stream != nil {
			spec = ri.Stream
		}
	}
	if spec == nil || spec.Validate == nil {
		t.Fatalf("the tail must set Validate: %+v", spec)
	}
	for _, target := range []string{"/logs/tail?pod=x", "/logs/tail?ns=Media", "/logs/tail?ns=media&since=2d"} {
		if err := spec.Validate(httptest.NewRequest(http.MethodGet, target, nil)); !errors.Is(err, upstream.ErrInvalid) {
			t.Errorf("Validate(%s) = %v, want ErrInvalid", target, err)
		}
	}
	if err := spec.Validate(tailReq(nil)); err != nil {
		t.Errorf("Validate of a good query = %v", err)
	}
	// A stopped instance is the handler's 503, not the validator's.
	p.st.Store(nil)
	if err := spec.Validate(httptest.NewRequest(http.MethodGet, "/logs/tail?pod=x", nil)); err != nil {
		t.Errorf("Validate while stopped = %v, want nil", err)
	}
}

func TestTailUpstreamFailureAfterBeginIsReturnedForTheCoreToLog(t *testing.T) {
	boom := errors.New("upstream went away")
	src := &fakeSource{tail: func(func(model.LogLine) error) error { return boom }}
	s := &fakeStream{}
	if err := started(src, nil).tail(context.Background(), tailReq(nil), authAnon, s); !errors.Is(err, boom) || !s.began {
		t.Errorf("err = %v began = %v", err, s.began)
	}
}

func TestTailResume(t *testing.T) {
	t0 := testNow.Add(-30 * time.Second) // T, the newest time the client saw from container A
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
	type tcase struct {
		name       string
		header     string // Last-Event-ID; empty = not sent
		lines      []line
		wantMsgs   []string
		wantLastID string
		wantOffset time.Duration
	}
	tests := []tcase{
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
		tests = append(tests, tcase{"malformed id " + bad, bad, []line{{"A", -time.Second, "a1"}}, plain, ids(map[string]time.Duration{"A": -time.Second}), 0})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{}
			src.tail = func(emit func(model.LogLine) error) error {
				for _, l := range tc.lines {
					if err := emit(model.LogLine{Time: t0.Add(l.at), Pod: "plex-0", Container: l.container, Msg: l.msg}); err != nil {
						return err
					}
				}
				return nil // the stream ends, which closes the response
			}
			hdr := map[string]string{}
			if tc.header != "" {
				hdr["Last-Event-ID"] = tc.header
			}
			s := &fakeStream{}
			if err := started(src, nil).tail(context.Background(), tailReq(hdr), authAnon, s); err != nil {
				t.Fatal(err)
			}
			if strings.Join(s.msgs(), ",") != strings.Join(tc.wantMsgs, ",") {
				t.Errorf("delivered %v, want %v", s.msgs(), tc.wantMsgs)
			}
			if got := s.lastID(); got != tc.wantLastID {
				t.Errorf("last id = %q, want %q", got, tc.wantLastID)
			}
			if got := src.query().StartOffset; got != tc.wantOffset {
				t.Errorf("start offset = %v, want %v", got, tc.wantOffset)
			}
		})
	}
}

// A tail remembers at most maxResumePoints containers; the oldest point is evicted first.
func TestTailResumeCapsPoints(t *testing.T) {
	t0 := testNow.Add(-30 * time.Second)
	seed := resumePoints{}
	for i := range maxResumePoints {
		seed[fmt.Sprintf("c%02d", i)] = t0.Add(time.Duration(i-20) * time.Second) // c00 is the oldest
	}
	src := &fakeSource{}
	src.tail = func(emit func(model.LogLine) error) error {
		return emit(model.LogLine{Time: t0.Add(time.Second), Container: "x", Msg: "new"})
	}
	s := &fakeStream{}
	if err := started(src, nil).tail(context.Background(), tailReq(map[string]string{"Last-Event-ID": seed.String()}), authAnon, s); err != nil {
		t.Fatal(err)
	}
	got, ok := parseResumePoints(s.lastID())
	if !ok {
		t.Fatalf("id %q does not parse", s.lastID())
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
	if want := 55 * time.Second; src.query().StartOffset != want { // from c00: 50s + 5s
		t.Errorf("start offset = %v, want %v", src.query().StartOffset, want)
	}
}

// A line without a time has nothing to resume from: it carries no id.
func TestTailLineWithoutATimeHasNoID(t *testing.T) {
	src := &fakeSource{tail: func(emit func(model.LogLine) error) error {
		return emit(model.LogLine{Container: "c", Msg: "no clock"})
	}}
	s := &fakeStream{}
	if err := started(src, nil).tail(context.Background(), tailReq(nil), authAnon, s); err != nil {
		t.Fatal(err)
	}
	if len(s.events) != 1 || s.events[0].id != "" {
		t.Errorf("events = %+v", s.events)
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

// ---- scrubbing ----

// canary is a secret with the characters json.Marshal escapes (& < > and "), so scrubbing the
// encoded payload instead of the text would miss it.
const canary = `s3&cr"et<token>`

// Log lines are application output: whatever a pod prints, a credential never reaches the
// browser, as an exact value, in an encoded form, or as a dash token. The plugin scrubs each Msg
// before it builds the payload.
func TestLogsAndTailScrubBeforeEncoding(t *testing.T) {
	reg := secret.NewRegistry()
	reg.Add(secret.New(canary, ""))
	msgs := []string{
		"token=" + canary + " end",
		"pat dash_pat_abcDEF123_secretPART456 end",
		"setup dash_setup_Zm9vYmFyYmF6 end",
		"plain line stays",
	}
	var lines []model.LogLine
	for i, m := range msgs {
		lines = append(lines, model.LogLine{Time: testNow.Add(time.Duration(i) * time.Second), Pod: "p", Container: "c", Msg: m})
	}
	forbidden := []string{canary, `s3\u0026cr\"et\u003ctoken\u003e`, "dash_pat_abcDEF123", "secretPART456", "dash_setup_Zm9v"}
	check := func(t *testing.T, where, out string) {
		t.Helper()
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Errorf("%s carries %q:\n%s", where, f, out)
			}
		}
		if !strings.Contains(out, "plain line stays") || strings.Count(out, "[redacted]") < len(msgs)-1 {
			t.Errorf("%s: want every secret line redacted and the plain one kept:\n%s", where, out)
		}
	}

	t.Run("query", func(t *testing.T) {
		p := started(&fakeSource{lines: lines}, reg.ScrubLogLine)
		w := httptest.NewRecorder()
		p.logs(w, httptest.NewRequest(http.MethodGet, "/logs?ns=media&pod=p", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d", w.Code)
		}
		check(t, "logs", w.Body.String())
	})
	t.Run("tail", func(t *testing.T) {
		src := &fakeSource{tail: func(emit func(model.LogLine) error) error {
			for _, l := range lines {
				if err := emit(l); err != nil {
					return err
				}
			}
			return nil
		}}
		s := &fakeStream{}
		if err := started(src, reg.ScrubLogLine).tail(context.Background(), tailReq(nil), authAnon, s); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, e := range s.events {
			out.WriteString(e.raw + "\n")
		}
		check(t, "tail", out.String())
	})
}

// The plugin's optional interfaces, checked at compile time.
var (
	_ plugin.Router     = (*Plugin)(nil)
	_ plugin.Demo       = (*Plugin)(nil)
	_ plugin.Validator  = (*Plugin)(nil)
	_ plugin.Configured = (*Plugin)(nil)
)

var authAnon auth.Principal

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }
