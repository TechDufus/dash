package obs

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	pmodel "github.com/prometheus/common/model"

	"github.com/TechDufus/dash/internal/model"
)

// sample is one scraped series.
type sample struct {
	name   string
	labels map[string]string
	value  float64
}

// scrape reads what GET /metrics answers, as the series it holds.
func scrape(t *testing.T, m *Metrics) []sample {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 {
		t.Fatalf("/metrics: %d", w.Code)
	}
	p := expfmt.NewTextParser(pmodel.UTF8Validation)
	fams, err := p.TextToMetricFamilies(w.Body)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, w.Body)
	}
	var out []sample
	for name, f := range fams {
		for _, mt := range f.Metric {
			s := sample{name: name, labels: map[string]string{}}
			for _, l := range mt.Label {
				s.labels[l.GetName()] = l.GetValue()
			}
			switch {
			case mt.Counter != nil:
				s.value = mt.Counter.GetValue()
			case mt.Gauge != nil:
				s.value = mt.Gauge.GetValue()
			case mt.Histogram != nil:
				s.value = float64(mt.Histogram.GetSampleCount())
			}
			out = append(out, s)
		}
	}
	return out
}

// find is the series called name whose labels include want; ok is false when there is none.
func find(ss []sample, name string, want map[string]string) (sample, bool) {
	for _, s := range ss {
		if s.name != name {
			continue
		}
		match := true
		for k, v := range want {
			match = match && s.labels[k] == v
		}
		if match {
			return s, true
		}
	}
	return sample{}, false
}

func value(t *testing.T, ss []sample, name string, want map[string]string) float64 {
	t.Helper()
	s, ok := find(ss, name, want)
	if !ok {
		t.Fatalf("no series %s%v in %v", name, want, ss)
	}
	return s.value
}

func TestNoDefaultGoCollectors(t *testing.T) {
	m := New("dev", nil, nil)
	m.AuthFailure(AuthCSRF)
	for _, s := range scrape(t, m) {
		if !strings.HasPrefix(s.name, "dash_") {
			t.Errorf("series %q is not dash's own: the registry must not hold Go or process collectors", s.name)
		}
	}
}

func TestBuildInfo(t *testing.T) {
	ss := scrape(t, New("v1.2.3", nil, nil))
	if v := value(t, ss, "dash_build_info", map[string]string{"version": "v1.2.3", "go_version": runtime.Version()}); v != 1 {
		t.Errorf("dash_build_info = %v, want 1", v)
	}
}

func TestNilMetricsIsInert(t *testing.T) {
	var m *Metrics
	m.AuthFailure(AuthCSRF)
	m.Action("a", "b", ActionOK)
	m.StreamOpened(StreamEvents)()
	w, done := m.TrackAction(httptest.NewRecorder(), "a", "b")
	if w == nil {
		t.Fatal("TrackAction returned a nil writer")
	}
	done()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if got := m.Instrument(next); fmt.Sprintf("%p", got) != fmt.Sprintf("%p", next) {
		t.Error("Instrument wrapped a handler with metrics off")
	}
	rt := http.RoundTripper(http.DefaultTransport)
	if m.Upstream("prometheus", rt) != rt || m.Upstream("prometheus", nil) != nil {
		t.Error("Upstream wrapped a transport with metrics off")
	}
}

func TestRouteLabelIsThePatternNotThePath(t *testing.T) {
	routes := []string{"GET /api/v1/items/{id}", "GET /api/v1/files/{rest...}", "/api/", "/"}
	m := New("dev", routes, nil)
	mux := http.NewServeMux()
	for _, p := range routes {
		mux.HandleFunc(p, m.Mark(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "teapot") {
				w.WriteHeader(http.StatusTeapot)
			}
		}))
	}
	// The inner handler sees a copy of the request, as the auth layer makes one.
	h := m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/refused") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r.Clone(r.Context()))
	}))
	for _, path := range []string{
		"/api/v1/items/1", "/api/v1/items/alice@example.com", "/api/v1/items/2",
		"/api/v1/files/a/b/c/secret-token-123", "/api/v1/files/teapot",
		"/api/unknown/evil.example", "/index.html", "/assets/app-9f8e7d.js", "/",
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	// A method no specific pattern allows falls to the "/api/" catch-all; a request refused
	// before the mux has no route at all.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/v1/items/9", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/refused/by/alice", nil))

	ss := scrape(t, m)
	for pattern, want := range map[string]float64{
		"GET /api/v1/items/{id}":      3,
		"GET /api/v1/files/{rest...}": 2,
		"/api/":                       2, // the unknown path, and the DELETE no GET pattern takes
		RouteStatic:                   3,
	} {
		var n float64
		for _, s := range ss {
			if s.name == "dash_http_requests_total" && s.labels["route"] == pattern {
				n += s.value
			}
		}
		if n != want {
			t.Errorf("route %q counted %v requests, want %v", pattern, n, want)
		}
	}
	if got := value(t, ss, "dash_http_requests_total", map[string]string{"route": "GET /api/v1/files/{rest...}", "code": "418"}); got != 1 {
		t.Errorf("the 418 = %v, want 1", got)
	}
	if got := value(t, ss, "dash_http_requests_total", map[string]string{"route": RouteOther, "code": "403"}); got != 1 {
		t.Errorf("other = %v, want the one request refused before the mux", got)
	}
	for _, s := range ss {
		for k, v := range s.labels {
			if k == "route" && !strings.HasPrefix(s.name, "dash_http_") {
				t.Errorf("%s has a route label", s.name)
			}
			if k == "route" && !(v == RouteOther || v == RouteStatic || slices.Contains(routes, v)) {
				t.Errorf("route label %q is not a registered pattern", v)
			}
		}
	}
	if _, ok := find(ss, "dash_http_request_duration_seconds", map[string]string{"route": "/api/"}); !ok {
		t.Error("no duration series for /api/")
	}
}

func TestInstrumentKeepsStreamingWorking(t *testing.T) {
	m := New("dev", []string{"GET /s"}, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s", m.Mark(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			t.Errorf("Flush through the wrapper: %v", err)
		}
		if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("SetWriteDeadline through the wrapper: %v", err)
		}
		_, _ = w.Write([]byte("x"))
	}))
	srv := httptest.NewServer(m.Instrument(mux))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/s")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if v := value(t, scrape(t, m), "dash_http_requests_total", map[string]string{"route": "GET /s", "code": "200"}); v != 1 {
		t.Errorf("count = %v", v)
	}
}

// flushFails is a writer whose flush fails, as a stream's does after its write deadline.
type flushFails struct{ http.ResponseWriter }

func (flushFails) FlushError() error { return errors.New("i/o timeout") }

// A stream's flush error must reach the handler through the metrics wrapper: it is how an SSE
// writer learns a client has stalled.
func TestInstrumentPassesFlushErrorsOn(t *testing.T) {
	m := New("dev", []string{"GET /s"}, nil)
	var got error
	h := m.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		got = http.NewResponseController(w).Flush()
	}))
	h.ServeHTTP(flushFails{httptest.NewRecorder()}, httptest.NewRequest(http.MethodGet, "/s", nil))
	if got == nil || got.Error() != "i/o timeout" {
		t.Errorf("Flush through the wrapper = %v, want the underlying error", got)
	}
}

func TestStreamGauge(t *testing.T) {
	m := New("dev", nil, nil)
	if v := value(t, scrape(t, m), "dash_sse_streams", map[string]string{"kind": StreamTail}); v != 0 {
		t.Errorf("before any stream: %v, want a series at 0", v)
	}
	a, b := m.StreamOpened(StreamEvents), m.StreamOpened(StreamEvents)
	tail := m.StreamOpened(StreamTail)
	m.StreamOpened("alice")() // not a kind: not counted
	ss := scrape(t, m)
	if value(t, ss, "dash_sse_streams", map[string]string{"kind": StreamEvents}) != 2 || value(t, ss, "dash_sse_streams", map[string]string{"kind": StreamTail}) != 1 {
		t.Errorf("open streams: %v", ss)
	}
	a()
	a() // closing twice frees one slot only
	tail()
	b()
	ss = scrape(t, m)
	if value(t, ss, "dash_sse_streams", map[string]string{"kind": StreamEvents}) != 0 || value(t, ss, "dash_sse_streams", map[string]string{"kind": StreamTail}) != 0 {
		t.Errorf("after closing: %v", ss)
	}
	for _, s := range ss {
		if s.name == "dash_sse_streams" && !slices.Contains(StreamKinds, s.labels["kind"]) {
			t.Errorf("kind %q", s.labels["kind"])
		}
	}
}

func TestPluginStateGaugeHasOneHotState(t *testing.T) {
	var mu sync.Mutex
	infos := []model.PluginInfo{{ID: "aa", State: model.PluginHealthy}, {ID: "bb", State: model.PluginBlocked}, {ID: "cc", State: "not-a-state"}}
	m := New("dev", nil, func() []model.PluginInfo { mu.Lock(); defer mu.Unlock(); return slices.Clone(infos) })

	check := func(want map[string]model.PluginState) {
		t.Helper()
		ss := scrape(t, m)
		perPlugin := map[string]int{}
		for _, s := range ss {
			if s.name != "dash_plugin_state" {
				continue
			}
			perPlugin[s.labels["plugin"]]++
			hot := want[s.labels["plugin"]] == model.PluginState(s.labels["state"])
			if (s.value == 1) != hot || (s.value != 0 && s.value != 1) {
				t.Errorf("%v = %v, want hot=%v", s.labels, s.value, hot)
			}
		}
		if len(perPlugin) != len(want) {
			t.Errorf("plugins in the scrape = %v, want %d", perPlugin, len(want))
		}
		for p, n := range perPlugin {
			if n != len(PluginStates) {
				t.Errorf("plugin %s has %d series, want one per state (%d)", p, n, len(PluginStates))
			}
		}
	}
	check(map[string]model.PluginState{"aa": model.PluginHealthy, "bb": model.PluginBlocked, "cc": ""})

	mu.Lock()
	infos = []model.PluginInfo{{ID: "aa", State: model.PluginError}}
	mu.Unlock()
	check(map[string]model.PluginState{"aa": model.PluginError}) // bb and cc are gone, aa moved
}

func TestUpstreamOutcomeCollapsesHTTPCodes(t *testing.T) {
	for code, want := range map[int]string{
		200: UpstreamOK, 204: UpstreamOK, 302: UpstreamOK,
		401: UpstreamAuth, 403: UpstreamAuth,
		400: UpstreamHTTP4xx, 404: UpstreamHTTP4xx, 418: UpstreamHTTP4xx, 429: UpstreamHTTP4xx, 499: UpstreamHTTP4xx,
		500: UpstreamHTTP5xx, 502: UpstreamHTTP5xx, 503: UpstreamHTTP5xx, 599: UpstreamHTTP5xx,
	} {
		if got := UpstreamOutcomeOf(&http.Response{StatusCode: code}, nil); got != want {
			t.Errorf("status %d = %q, want %q", code, got, want)
		}
	}
	timeout := &net.DNSError{IsTimeout: true}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{timeout, UpstreamTimeout},
		{context.DeadlineExceeded, UpstreamTimeout},
		{x509.UnknownAuthorityError{}, UpstreamTLS},
		{errors.New("tls: handshake failure"), UpstreamTLS},
		{errors.New("dial tcp 192.0.2.1:9090: connect: connection refused"), UpstreamUnreachable},
		{errors.New("something nobody foresaw"), UpstreamUnreachable},
	} {
		if got := UpstreamOutcomeOf(nil, tc.err); got != tc.want {
			t.Errorf("error %v = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// rtFunc is a RoundTripper from a func.
type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpstreamTransportCountsEveryRoundTrip(t *testing.T) {
	m := New("dev", nil, nil)
	status, fail := 200, error(nil)
	rt := m.Upstream("gatus", rtFunc(func(*http.Request) (*http.Response, error) {
		if fail != nil {
			return nil, fail
		}
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	}))
	call := func() {
		req, _ := http.NewRequest(http.MethodGet, "http://192.0.2.10:8080/api/v1/endpoints/statuses?user=alice", nil)
		resp, err := rt.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	call()
	call()
	status = 503
	call()
	status, fail = 200, context.DeadlineExceeded
	call()
	fail = context.Canceled // the caller went away: not an upstream outcome
	call()

	ss := scrape(t, m)
	want := map[string]float64{UpstreamOK: 2, UpstreamHTTP5xx: 1, UpstreamTimeout: 1}
	for _, o := range UpstreamOutcomes {
		if got := value(t, ss, "dash_upstream_requests_total", map[string]string{"plugin": "gatus", "outcome": o}); got != want[o] {
			t.Errorf("gatus %s = %v, want %v", o, got, want[o])
		}
	}
	if got := value(t, ss, "dash_upstream_request_duration_seconds", map[string]string{"plugin": "gatus"}); got != 4 {
		t.Errorf("duration observations = %v, want 4 (the cancelled call is not one)", got)
	}
	for _, p := range UpstreamPlugins { // every upstream has its series from the start
		if _, ok := find(ss, "dash_upstream_requests_total", map[string]string{"plugin": p, "outcome": UpstreamOK}); !ok {
			t.Errorf("no series for upstream %s", p)
		}
	}

	inner := http.RoundTripper(rtFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("x") }))
	if got := m.Upstream("not-an-upstream", inner); fmt.Sprintf("%p", got) != fmt.Sprintf("%p", inner) {
		t.Error("a transport for an unknown upstream was wrapped: it would add a plugin label value")
	}
	if m.Upstream("proxmox", nil) == nil {
		t.Error("a nil transport for a known upstream must become http.DefaultTransport, wrapped")
	}
}

func TestActionLabels(t *testing.T) {
	m := New("dev", nil, func() []model.PluginInfo {
		return []model.PluginInfo{{ID: "aa", Actions: []model.ActionInfo{{Name: "restart"}}}, {ID: "bb"}}
	})
	m.Action("aa", "restart", ActionOK)
	m.Action("aa", "restart", "weird")      // outcome outside the set
	m.Action("aa", "drop table", ActionOK)  // not an action of aa
	m.Action("bb", "restart", ActionDenied) // bb has no actions
	m.Action("alice", "restart", ActionOK)  // not a plugin
	m.Action("kubernetes", "restart", ActionOK)
	m.Action("alertmanager", "silence.expire", ActionInvalid)

	ss := scrape(t, m)
	for want, n := range map[[3]string]float64{
		{"aa", "restart", ActionOK}:                       1,
		{"aa", "restart", ActionError}:                    1,
		{"other", "other", ActionOK}:                      2,
		{"other", "other", ActionDenied}:                  1,
		{"kubernetes", "restart", ActionOK}:               1,
		{"alertmanager", "silence.expire", ActionInvalid}: 1,
	} {
		if got := value(t, ss, "dash_actions_total", map[string]string{"plugin": want[0], "action": want[1], "outcome": want[2]}); got != n {
			t.Errorf("%v = %v, want %v", want, got, n)
		}
	}
}

func TestTrackActionOutcomeFromStatus(t *testing.T) {
	m := New("dev", nil, nil)
	for status, want := range map[int]string{
		200: ActionOK, 202: ActionOK, 400: ActionInvalid, 422: ActionInvalid, 401: ActionDenied, 403: ActionDenied,
		429: ActionRateLimited, 404: ActionError, 409: ActionError, 500: ActionError, 502: ActionError, 503: ActionError,
	} {
		if got := ActionOutcomeOf(status); got != want {
			t.Errorf("status %d = %q, want %q", status, got, want)
		}
	}
	rec := httptest.NewRecorder()
	w, done := m.TrackAction(rec, "kubernetes", "restart")
	w.WriteHeader(http.StatusTooManyRequests)
	done()
	w, done = m.TrackAction(httptest.NewRecorder(), "flux", "reconcile")
	_, _ = w.Write([]byte("{}")) // no explicit status: 200
	done()
	ss := scrape(t, m)
	if value(t, ss, "dash_actions_total", map[string]string{"plugin": "kubernetes", "action": "restart", "outcome": ActionRateLimited}) != 1 ||
		value(t, ss, "dash_actions_total", map[string]string{"plugin": "flux", "action": "reconcile", "outcome": ActionOK}) != 1 {
		t.Errorf("series: %v", ss)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("the wrapper changed the response: %d", rec.Code)
	}
}

// A reason is one of the package's values, so a user name or a request line cannot become a
// label (the type admits nothing else); the zero value counts as nothing.
func TestAuthFailureReasons(t *testing.T) {
	m := New("dev", nil, nil)
	for _, r := range AuthReasons {
		m.AuthFailure(r)
	}
	m.AuthFailure(AuthCSRF)
	m.AuthFailure(AuthReason{})
	ss := scrape(t, m)
	n := 0
	for _, s := range ss {
		if s.name != "dash_auth_failures_total" {
			continue
		}
		n++
		if !slices.ContainsFunc(AuthReasons, func(r AuthReason) bool { return r.String() == s.labels["reason"] }) {
			t.Errorf("reason %q is not in the fixed set", s.labels["reason"])
		}
	}
	if n != len(AuthReasons) {
		t.Errorf("%d series, want %d", n, len(AuthReasons))
	}
	if value(t, ss, "dash_auth_failures_total", map[string]string{"reason": AuthCSRF.String()}) != 2 {
		t.Error("csrf not counted twice")
	}
}
