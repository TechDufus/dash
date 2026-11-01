package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	pmodel "github.com/prometheus/common/model"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// authReasonLabels are the label values of dash_auth_failures_total's reason.
func authReasonLabels() []string {
	out := make([]string, len(obs.AuthReasons))
	for i, r := range obs.AuthReasons {
		out[i] = r.String()
	}
	return out
}

// metricsEnv is the action fixture ("aa": restart, scale, ping) with metrics on.
func metricsEnv(t *testing.T, ics ...plugin.InstanceConfig) (*actionEnv, *obs.Metrics) {
	t.Helper()
	if len(ics) == 0 {
		ics = []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}
	}
	e := newActionEnvWith(t, nil, ics)
	m := obs.New("test", RoutePatterns(), e.sup.Infos)
	e.Server.d.Metrics = m
	return e, m
}

// seriesOf is one scraped series: its family, labels and value.
type seriesOf struct {
	name   string
	labels map[string]string
	value  float64
}

// scrapeMetrics GETs /metrics from the health handler and parses it.
func scrapeMetrics(t *testing.T, s *Server) []seriesOf {
	t.Helper()
	w := httptest.NewRecorder()
	s.HealthHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics = %d", w.Code)
	}
	p := expfmt.NewTextParser(pmodel.UTF8Validation)
	fams, err := p.TextToMetricFamilies(w.Body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out []seriesOf
	for name, f := range fams {
		for _, m := range f.Metric {
			s := seriesOf{name: name, labels: map[string]string{}}
			for _, l := range m.Label {
				s.labels[l.GetName()] = l.GetValue()
			}
			switch {
			case m.Counter != nil:
				s.value = m.Counter.GetValue()
			case m.Gauge != nil:
				s.value = m.Gauge.GetValue()
			case m.Histogram != nil:
				s.value = float64(m.Histogram.GetSampleCount())
			}
			out = append(out, s)
		}
	}
	return out
}

func seriesValue(t *testing.T, ss []seriesOf, name string, want map[string]string) (float64, bool) {
	t.Helper()
	for _, s := range ss {
		if s.name != name {
			continue
		}
		ok := true
		for k, v := range want {
			ok = ok && s.labels[k] == v
		}
		if ok {
			return s.value, true
		}
	}
	return 0, false
}

func TestMetricsOffIs404(t *testing.T) {
	s := newServer(nil)
	s.MarkServing()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		s.HealthHandler().ServeHTTP(w, httptest.NewRequest(method, "/metrics", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s /metrics with metrics off = %d, want 404", method, w.Code)
		}
	}
	// The probes are untouched.
	if w := readyz(s.HealthHandler(), "", kubeletPeer); w.Code != 200 {
		t.Errorf("/readyz = %d", w.Code)
	}
}

func TestMetricsAreOnTheHealthListenerOnly(t *testing.T) {
	e, _ := metricsEnv(t)
	w := req{path: "/metrics"}.do(e.Handler())
	if strings.Contains(w.Body.String(), "dash_") {
		t.Errorf("the API listener served metrics:\n%s", w.Body)
	}
	w = httptest.NewRecorder()
	e.HealthHandler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /metrics = %d, want 405", w.Code)
	}
}

func TestMetricsOnHasEverySeries(t *testing.T) {
	e, m := metricsEnv(t)
	h := e.Handler()

	if w := (req{path: "/api/v1/me"}).do(h); w.Code != 200 {
		t.Fatalf("me = %d", w.Code)
	}
	if w := e.post("ping", nil); w.Code != http.StatusAccepted {
		t.Fatalf("ping = %d", w.Code)
	}
	if w := (req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", raw: true}).do(h); w.Code != http.StatusForbidden {
		t.Fatalf("without the CSRF headers = %d, want 403", w.Code)
	}
	if w := (req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}).do(h); w.Code != http.StatusUnauthorized {
		t.Fatalf("without a user = %d, want 401", w.Code)
	}
	// One open stream, and one call to an upstream.
	_, lines := sseClient(t, h, "/api/v1/stream")
	expect(t, lines, "retry: 3000")
	up := m.Upstream("prometheus", fakeRT(http.StatusOK))
	ureq, _ := http.NewRequest(http.MethodGet, "http://192.0.2.10:9090/api/v1/query", nil)
	resp, err := up.RoundTrip(ureq)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	ss := scrapeMetrics(t, e.Server)
	for _, name := range []string{
		"dash_build_info", "dash_http_requests_total", "dash_http_request_duration_seconds", "dash_sse_streams",
		"dash_plugin_state", "dash_upstream_requests_total", "dash_upstream_request_duration_seconds",
		"dash_actions_total", "dash_auth_failures_total",
	} {
		if _, ok := seriesValue(t, ss, name, nil); !ok {
			t.Errorf("no %s series", name)
		}
	}
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"dash_http_requests_total", map[string]string{"route": "GET /api/v1/me", "code": "200"}, 1},
		{"dash_http_requests_total", map[string]string{"route": "POST /api/v1/plugins/{id}/actions/{name}", "code": "202"}, 1},
		{"dash_http_requests_total", map[string]string{"route": obs.RouteOther, "code": "401"}, 0}, // the 401 comes from the auth layer: no route
		{"dash_actions_total", map[string]string{"plugin": "aa", "action": "ping", "outcome": "ok"}, 1},
		{"dash_auth_failures_total", map[string]string{"reason": "csrf"}, 1},
		{"dash_auth_failures_total", map[string]string{"reason": "unauthenticated"}, 1},
		{"dash_sse_streams", map[string]string{"kind": "events"}, 1},
		{"dash_plugin_state", map[string]string{"plugin": "aa", "state": "healthy"}, 1},
		{"dash_plugin_state", map[string]string{"plugin": "aa", "state": "error"}, 0},
		{"dash_upstream_requests_total", map[string]string{"plugin": "prometheus", "outcome": "ok"}, 1},
		{"dash_upstream_request_duration_seconds", map[string]string{"plugin": "prometheus"}, 1},
	} {
		got, ok := seriesValue(t, ss, c.name, c.labels)
		if !ok || (c.want > 0 && got != c.want) {
			t.Errorf("%s%v = %v (present %v), want %v", c.name, c.labels, got, ok, c.want)
		}
	}
}

// The two legacy Kubernetes actions are counted under the plugin and action they will have, a
// plugin action under its own, and a plugin stream has the tail gauge: with metrics on each
// shows up under the name and outcome of what happened.
func TestLegacyActionsAndTailAreCountedWithMetricsOn(t *testing.T) {
	var fail error
	pl := newHarness(t)
	m := obs.New("test", RoutePatterns(), pl.sup.Infos)
	pl.act = func(string, any) (any, error) { return nil, fail }
	s, _, _ := serverWithFakes(func(d *Deps) { pl.attach(d); d.Audit, d.Metrics = openAudit(t, "", nil), m })
	h := s.Handler()
	type call struct {
		plugin, action string
		r              req
		err            bool
	}
	var want []struct{ plugin, action, outcome string }
	for _, c := range []call{
		{"kubernetes", "restart", req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}}, false},
		{"flux", "reconcile", req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "ghost"}}, false},
		{"aa", "silence.create", req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.create", body: silenceIn{Alertname: "HighTemp"}}, false},
		{"aa", "silence.expire", req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.expire", body: expireIn{ID: "abc"}}, true},
		{"aa", "silence.expire", req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.expire", body: expireIn{ID: "abc"}}, false},
	} {
		fail = nil
		if c.err {
			fail = errors.New("boom")
		}
		w := c.r.do(h)
		want = append(want, struct{ plugin, action, outcome string }{c.plugin, c.action, obs.ActionOutcomeOf(w.Code)})
	}

	_, lines := sseClient(t, h, "/api/v1/plugins/rt/tail?ns=media&pod=plex-0")
	expect(t, lines, "retry: 3000")

	ss := scrapeMetrics(t, s)
	seen := map[string]float64{}
	for _, w := range want {
		seen[w.plugin+"/"+w.action+"/"+w.outcome]++
	}
	for key, n := range seen {
		parts := strings.Split(key, "/")
		got, ok := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": parts[0], "action": parts[1], "outcome": parts[2]})
		if !ok || got != n {
			t.Errorf("dash_actions_total %s = %v (present %v), want %v", key, got, ok, n)
		}
	}
	if got, ok := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "aa", "action": "silence.expire", "outcome": obs.ActionOK}); !ok || got != 1 {
		t.Errorf("the expire that succeeded is counted %v (present %v), want 1 under ok", got, ok)
	}
	if got, ok := seriesValue(t, ss, "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); !ok || got != 1 {
		t.Errorf("open plugin tails = %v (present %v), want 1", got, ok)
	}
	if got, _ := seriesValue(t, ss, "dash_sse_streams", map[string]string{"kind": obs.StreamEvents}); got != 0 {
		t.Errorf("open event streams = %v, want 0", got)
	}
}

// fakeRT answers every request with status.
type fakeRT int

func (f fakeRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: int(f), Body: http.NoBody}, nil
}

// legacyActionPairs are the (plugin, action) pairs of the integration routes that are not plugin
// actions yet. It is written out here on purpose: the test checks obs against the contract.
var legacyActionPairs = [][2]string{
	{"kubernetes", "restart"}, {"flux", "reconcile"}, {"alertmanager", "silence.create"}, {"alertmanager", "silence.expire"},
}

func TestMetricLabelValuesComeFromFixedSets(t *testing.T) {
	e, m := metricsEnv(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, DenyNamespaces: []string{"kube-system"}})
	h := e.Handler()

	// Everything a client controls, hostile: paths, users, hosts, methods, action names.
	hostile := []req{
		{path: "/api/v1/topics/mallory-secret-topic"},
		{path: "/api/v1/topics/another-mallory"},
		{path: "/api/v1/plugins/mallory/actions/drop-table", method: http.MethodPost, body: "{}"},
		{path: "/api/v1/plugins/aa/actions/mallory-evil", method: http.MethodPost, body: "{}"},
		{path: "/api/v1/plugins/aa/mallory/deeper/than/any/route"},
		{path: "/x/mallory/evil.example"},
		{path: "/assets/mallory-123.js"},
		{path: "/api/mallory"},
		{path: "/api/v1/me", method: http.MethodPut},
		{path: "/api/v1/me", host: "evil.example"},
		{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": "mallory"}},
		{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}},
		{path: "/api/v1/plugins/aa/actions/ping", method: http.MethodPost, raw: true, header: map[string]string{"X-Forwarded-Preferred-Username": "mallory"}},
		{path: "/api/vms/9999"},
	}
	for _, r := range hostile {
		r.do(h)
	}
	// Denied namespace, and the rate limit.
	if w := e.post("restart", restartIn{Namespace: "kube-system", Name: "coredns"}); w.Code != http.StatusForbidden {
		t.Fatalf("denied namespace = %d", w.Code)
	}
	e.post("scale", map[string]any{"replicas": -1}) // invalid, before the limit is used up
	for range actionRateLimit + 1 {
		e.post("ping", nil)
	}
	for _, other := range []string{"prometheus", "mallory", ""} {
		m.Upstream(other, fakeRT(500)) // an unknown upstream is not wrapped
	}

	ss := scrapeMetrics(t, e.Server)
	infos := e.sup.Infos()
	allowed := map[string][]string{
		"route":   append(RoutePatterns(), obs.RouteOther, obs.RouteStatic),
		"kind":    obs.StreamKinds,
		"plugin":  obs.UpstreamPlugins,
		"reason":  authReasonLabels(),
		"outcome": nil, // depends on the series
	}
	var pluginIDs []string
	var states []string
	for _, s := range obs.PluginStates {
		states = append(states, string(s))
	}
	actionPairs := slices.Clone(legacyActionPairs)
	actionPairs = append(actionPairs, [2]string{"other", "other"})
	for _, p := range infos {
		pluginIDs = append(pluginIDs, p.ID)
		for _, a := range p.Actions {
			actionPairs = append(actionPairs, [2]string{p.ID, a.Name})
		}
	}

	seen := map[string]bool{}
	for _, s := range ss {
		if !strings.HasPrefix(s.name, "dash_") {
			t.Errorf("series %q is not dash's", s.name)
		}
		seen[s.name] = true
		for k, v := range s.labels {
			if strings.Contains(v, "mallory") || strings.Contains(v, "evil.example") || strings.Contains(v, "9999") {
				t.Errorf("%s{%s=%q}: a request's own text became a label value", s.name, k, v)
			}
		}
		switch s.name {
		case "dash_http_requests_total", "dash_http_request_duration_seconds":
			if !slices.Contains(allowed["route"], s.labels["route"]) {
				t.Errorf("%s route %q is not a registered pattern", s.name, s.labels["route"])
			}
			if c, ok := s.labels["code"]; ok {
				if n, err := strconv.Atoi(c); err != nil || n < 100 || n > 599 {
					t.Errorf("code %q is not an HTTP status", c)
				}
			}
		case "dash_sse_streams":
			if !slices.Contains(allowed["kind"], s.labels["kind"]) {
				t.Errorf("stream kind %q", s.labels["kind"])
			}
		case "dash_plugin_state":
			if !slices.Contains(pluginIDs, s.labels["plugin"]) || !slices.Contains(states, s.labels["state"]) {
				t.Errorf("plugin state labels %v", s.labels)
			}
		case "dash_upstream_requests_total", "dash_upstream_request_duration_seconds":
			if !slices.Contains(allowed["plugin"], s.labels["plugin"]) {
				t.Errorf("upstream %q", s.labels["plugin"])
			}
			if o, ok := s.labels["outcome"]; ok && !slices.Contains(obs.UpstreamOutcomes, o) {
				t.Errorf("upstream outcome %q", o)
			}
		case "dash_actions_total":
			if !slices.Contains(actionPairs, [2]string{s.labels["plugin"], s.labels["action"]}) {
				t.Errorf("action %q of %q is neither a mounted action nor a legacy one", s.labels["action"], s.labels["plugin"])
			}
			if !slices.Contains(obs.ActionOutcomes, s.labels["outcome"]) {
				t.Errorf("action outcome %q", s.labels["outcome"])
			}
		case "dash_auth_failures_total":
			if !slices.Contains(allowed["reason"], s.labels["reason"]) {
				t.Errorf("auth reason %q", s.labels["reason"])
			}
		case "dash_build_info":
		default:
			t.Errorf("unexpected series %q", s.name)
		}
	}

	// Different topics, one pattern.
	if v, ok := seriesValue(t, ss, "dash_http_requests_total", map[string]string{"route": "GET /api/v1/topics/{topic}"}); !ok || v != 2 {
		t.Errorf("two topic requests = %v (present %v), want one series of 2", v, ok)
	}
	// What the sets cover happened: a denied namespace, a rate limit, an invalid input.
	for _, o := range []string{"denied", "rate_limited", "invalid", "ok"} {
		found := false
		for _, s := range ss {
			found = found || (s.name == "dash_actions_total" && s.labels["outcome"] == o && s.value > 0)
		}
		if !found {
			t.Errorf("no action counted as %q", o)
		}
	}
	for _, name := range []string{"dash_http_requests_total", "dash_actions_total", "dash_auth_failures_total", "dash_plugin_state"} {
		if !seen[name] {
			t.Errorf("no %s series after the traffic", name)
		}
	}
}

func TestMetricsCountPluginStateChanges(t *testing.T) {
	e, _ := metricsEnv(t)
	hot := func() string {
		for _, s := range scrapeMetrics(t, e.Server) {
			if s.name == "dash_plugin_state" && s.labels["plugin"] == "aa" && s.value == 1 {
				return s.labels["state"]
			}
		}
		return ""
	}
	if got := hot(); got != string(model.PluginHealthy) {
		t.Fatalf("state = %q, want healthy", got)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.sup.Reload()
	e.eventually(t, "the metric to follow the plugin to disabled", func() bool { return hot() == string(model.PluginDisabled) })
}
