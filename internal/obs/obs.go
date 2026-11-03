// Package obs is dash's own observability: Prometheus metrics about its requests, streams,
// plugins, upstream calls and actions (design K.27, epic P2.12).
//
// The metrics live in a private registry, so no Go or process collector and no global state
// comes along unasked. Every label value comes from a fixed set, so what a client sends (a path,
// a user, a host) can never create a series: a route is a registered ServeMux pattern, an
// upstream is one of five names, an outcome and a reason are enumerated. A value outside its
// set is counted as "other" (route, action) or dropped (the enumerations), never recorded.
//
// A nil *Metrics is the "metrics off" state: every method is safe to call and does nothing, and
// Upstream, Instrument and Mark hand back what they were given.
package obs

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// Route labels besides the registered patterns.
const (
	RouteStatic = "static" // the "/" catch-all that serves the SPA
	RouteOther  = "other"  // refused before the mux, or no route matched
)

// Kinds of SSE stream.
const (
	StreamEvents = "events" // /api/v1/stream
	StreamTail   = "tail"   // a plugin stream (the victorialogs log tail)
)

// Outcomes of an action.
const (
	ActionOK          = "ok"
	ActionError       = "error"
	ActionDenied      = "denied"
	ActionInvalid     = "invalid"
	ActionRateLimited = "rate_limited"
)

// AuthReason is why a request was refused, a value of dash_auth_failures_total's reason label.
// It is a struct with an unexported field, so the only values are the ones below: code that
// refuses a request (Server.deny) cannot name a reason the metric does not know, and the zero
// value is counted as nothing.
type AuthReason struct{ name string }

// String is the label value, and the reason recorded in the audit log.
func (r AuthReason) String() string { return r.name }

// The reasons Server.deny records.
var (
	AuthUnauthenticated = AuthReason{"unauthenticated"}
	AuthCSRF            = AuthReason{"csrf"}
	AuthForbidden       = AuthReason{"forbidden"}
	AuthRateLimit       = AuthReason{"rate_limit"}
	AuthNamespaceDenied = AuthReason{"namespace_denied"}
	AuthStreamLimit     = AuthReason{"stream_limit"}

	// AuthUntrustedPeer is an identity header from a peer outside auth.proxy.trustedProxies;
	// AuthAmbiguousIdentity is an identity header sent as more than one line.
	AuthUntrustedPeer     = AuthReason{"untrusted_peer"}
	AuthAmbiguousIdentity = AuthReason{"ambiguous_identity"}

	// AuthLoginFailed is a refused login or setup attempt (wrong password, unknown account, wrong
	// setup token); AuthLoginThrottled one refused by the login backoff without being tried.
	// Declared for the local mode; they join AuthReasons with the commit that counts them.
	AuthLoginFailed    = AuthReason{"login_failed"}
	AuthLoginThrottled = AuthReason{"login_throttled"}
)

// Outcomes of an upstream call.
const (
	UpstreamOK              = "ok"
	UpstreamTimeout         = upstream.CategoryTimeout
	UpstreamTLS             = upstream.CategoryTLS
	UpstreamAuth            = upstream.CategoryAuth
	UpstreamHTTP4xx         = "http_4xx"
	UpstreamHTTP5xx         = "http_5xx"
	UpstreamInvalidResponse = upstream.CategoryInvalidResponse
	UpstreamUnreachable     = upstream.CategoryUnreachable
)

// Fixed sets of label values. They are the contract of the series, and the tests compare what
// a scrape returns against them.
var (
	// UpstreamPlugins are the upstreams whose clients are wrapped.
	UpstreamPlugins = []string{"prometheus", "alertmanager", "victorialogs", "gatus", "proxmox"}
	// UpstreamOutcomes is every value of dash_upstream_requests_total's outcome label.
	UpstreamOutcomes = []string{UpstreamOK, UpstreamTimeout, UpstreamTLS, UpstreamAuth, UpstreamHTTP4xx, UpstreamHTTP5xx, UpstreamInvalidResponse, UpstreamUnreachable}
	// ActionOutcomes is every value of dash_actions_total's outcome label.
	ActionOutcomes = []string{ActionOK, ActionError, ActionDenied, ActionInvalid, ActionRateLimited}
	// AuthReasons is every value of dash_auth_failures_total's reason label.
	AuthReasons = []AuthReason{AuthUnauthenticated, AuthCSRF, AuthForbidden, AuthRateLimit, AuthNamespaceDenied, AuthStreamLimit, AuthUntrustedPeer, AuthAmbiguousIdentity}
	// StreamKinds is every value of dash_sse_streams' kind label.
	StreamKinds = []string{StreamEvents, StreamTail}
	// PluginStates is every value of dash_plugin_state's state label.
	PluginStates = []model.PluginState{
		model.PluginDisabled, model.PluginUnavailable, model.PluginUnconfigured, model.PluginBlocked,
		model.PluginStarting, model.PluginHealthy, model.PluginDegraded, model.PluginError,
	}
)

// Metrics is the set of dash's own series.
type Metrics struct {
	reg     *prometheus.Registry
	routes  map[string]bool // registered patterns, with "/" as RouteStatic
	plugins func() []model.PluginInfo

	httpReqs  *prometheus.CounterVec
	httpDur   *prometheus.HistogramVec
	streams   *prometheus.GaugeVec
	upReqs    *prometheus.CounterVec
	upDur     *prometheus.HistogramVec
	actions   *prometheus.CounterVec
	authFails *prometheus.CounterVec
}

// New builds the metrics. routes are the ServeMux patterns the API registers (the route table);
// plugins lists the plugin instances at scrape time and may be nil.
func New(version string, routes []string, plugins func() []model.PluginInfo) *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry(), routes: map[string]bool{RouteStatic: true, RouteOther: true}, plugins: plugins}
	for _, r := range routes {
		if r == "/" {
			r = RouteStatic
		}
		m.routes[r] = true
	}

	m.httpReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dash_http_requests_total", Help: "Requests to the API listener by route pattern and status code.",
	}, []string{"route", "code"})
	m.httpDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "dash_http_request_duration_seconds", Help: "Time to serve a request by route pattern; for a stream, how long it stayed open.",
		Buckets: prometheus.DefBuckets,
	}, []string{"route"})
	m.streams = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dash_sse_streams", Help: "Open server-sent event streams.",
	}, []string{"kind"})
	m.upReqs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dash_upstream_requests_total", Help: "Requests to upstream services by outcome.",
	}, []string{"plugin", "outcome"})
	m.upDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "dash_upstream_request_duration_seconds", Help: "Time until an upstream answered (response headers), or failed.",
		Buckets: prometheus.DefBuckets,
	}, []string{"plugin"})
	m.actions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dash_actions_total", Help: "Action requests by plugin, action and outcome.",
	}, []string{"plugin", "action", "outcome"})
	m.authFails = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "dash_auth_failures_total", Help: "Requests refused, by reason.",
	}, []string{"reason"})
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "dash_build_info", Help: "dash's version and the Go version it was built with; always 1.",
	}, []string{"version", "go_version"})
	build.WithLabelValues(version, runtime.Version()).Set(1)

	m.reg.MustRegister(build, m.httpReqs, m.httpDur, m.streams, m.upReqs, m.upDur, m.actions, m.authFails,
		&pluginStates{desc: prometheus.NewDesc("dash_plugin_state",
			"1 for the state a plugin instance is in, 0 for the other states.", []string{"plugin", "state"}, nil), infos: plugins})

	// The series with a small fixed label space exist from the start, at zero, so a rate() has
	// a baseline and a dashboard can tell "none yet" from "not measured".
	for _, k := range StreamKinds {
		m.streams.WithLabelValues(k)
	}
	for _, p := range UpstreamPlugins {
		m.upDur.WithLabelValues(p)
		for _, o := range UpstreamOutcomes {
			m.upReqs.WithLabelValues(p, o)
		}
	}
	for _, r := range AuthReasons {
		m.authFails.WithLabelValues(r.String())
	}
	return m
}

// Handler serves the registry in the Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// routeKey is the context key of the routeHolder Instrument puts on a request.
type routeKey struct{}

// routeHolder carries the pattern the mux matched from Mark, deep in the chain, back out to
// Instrument. A copy of the request made on the way in (the auth layer makes one) keeps the
// same context, so the holder is shared.
type routeHolder struct{ pattern string }

// Instrument counts and times every request that passes through next: dash_http_*. Put it
// outermost. The route is what Mark recorded, or RouteOther when no route handled the request.
func (m *Metrics) Instrument(next http.Handler) http.Handler {
	if m == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold := &routeHolder{}
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), routeKey{}, hold)))
		route := m.routeLabel(hold.pattern)
		m.httpReqs.WithLabelValues(route, codeLabel(sw.status())).Inc()
		m.httpDur.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// Mark wraps a route's handler, the innermost layer around the mux: it is the one place that
// sees r.Pattern after dispatch.
func (m *Metrics) Mark(h http.HandlerFunc) http.HandlerFunc {
	if m == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if hold, ok := r.Context().Value(routeKey{}).(*routeHolder); ok {
			hold.pattern = r.Pattern
		}
		h(w, r)
	}
}

// routeLabel maps a matched pattern to its label; anything not registered is RouteOther.
func (m *Metrics) routeLabel(pattern string) string {
	if pattern == "/" {
		return RouteStatic
	}
	if m.routes[pattern] {
		return pattern
	}
	return RouteOther
}

// codeLabel is the status as text; a code outside 100-599 cannot come from net/http and is "other".
func codeLabel(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return strconv.Itoa(code)
}

// statusWriter records the status code. It keeps the optional interfaces handlers use:
// http.ResponseController finds SetWriteDeadline and Flush through Unwrap.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) { // 1xx are informational
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() { _ = w.FlushError() }

// FlushError is what http.ResponseController.Flush calls first: it hands the error of the
// underlying flush (an expired write deadline, a closed connection) to the caller instead of
// swallowing it, so an SSE writer sees a stalled client at once.
func (w *statusWriter) FlushError() error {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// status is the code the handler answered with; a handler that wrote nothing answers 200.
func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// StreamOpened counts an open stream of the given kind and returns the func that ends it
// (idempotent). An unknown kind is not counted.
func (m *Metrics) StreamOpened(kind string) (closed func()) {
	if m == nil || !slices.Contains(StreamKinds, kind) {
		return func() {}
	}
	g := m.streams.WithLabelValues(kind)
	g.Inc()
	var once sync.Once
	return func() { once.Do(g.Dec) }
}

// AuthFailure counts a refused request. The zero AuthReason is dropped.
func (m *Metrics) AuthFailure(reason AuthReason) {
	if m == nil || !slices.Contains(AuthReasons, reason) {
		return
	}
	m.authFails.WithLabelValues(reason.String()).Inc()
}

// Action counts an action request. The plugin and action must be a mounted action of a plugin
// instance or both are "other"; an outcome outside ActionOutcomes is counted as ActionError.
func (m *Metrics) Action(plugin, action, outcome string) {
	if m == nil {
		return
	}
	if !m.knownAction(plugin, action) {
		plugin, action = "other", "other"
	}
	if !slices.Contains(ActionOutcomes, outcome) {
		outcome = ActionError
	}
	m.actions.WithLabelValues(plugin, action, outcome).Inc()
}

func (m *Metrics) knownAction(plugin, action string) bool {
	if m.plugins == nil {
		return false
	}
	for _, p := range m.plugins() {
		if p.ID == plugin {
			return slices.ContainsFunc(p.Actions, func(a model.ActionInfo) bool { return a.Name == action })
		}
	}
	return false
}

// TrackAction counts the action whose handler w is given to. Call done when the handler
// returns; the outcome comes from the status it answered with: 2xx ok, 400 and 422 invalid,
// 401 and 403 denied, 429 rate_limited, anything else error.
func (m *Metrics) TrackAction(w http.ResponseWriter, plugin, action string) (http.ResponseWriter, func()) {
	if m == nil {
		return w, func() {}
	}
	sw := &statusWriter{ResponseWriter: w}
	return sw, func() { m.Action(plugin, action, ActionOutcomeOf(sw.status())) }
}

// ActionOutcomeOf is the outcome an action that answered with status counts as.
func ActionOutcomeOf(status int) string {
	switch {
	case status >= 200 && status < 300:
		return ActionOK
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return ActionInvalid
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ActionDenied
	case status == http.StatusTooManyRequests:
		return ActionRateLimited
	}
	return ActionError
}

// Upstream wraps rt (nil: http.DefaultTransport) so every request counts into
// dash_upstream_*. A plugin that is not one of UpstreamPlugins, and a nil *Metrics, leave rt
// as it is. A request cancelled by the caller is not an upstream failure and is not counted.
func (m *Metrics) Upstream(plugin string, rt http.RoundTripper) http.RoundTripper {
	if m == nil || !slices.Contains(UpstreamPlugins, plugin) {
		return rt
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &upstreamTransport{m: m, plugin: plugin, rt: rt}
}

type upstreamTransport struct {
	m      *Metrics
	plugin string
	rt     http.RoundTripper
}

func (t *upstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.rt.RoundTrip(req)
	if errors.Is(err, context.Canceled) {
		return resp, err
	}
	t.m.upReqs.WithLabelValues(t.plugin, UpstreamOutcomeOf(resp, err)).Inc()
	t.m.upDur.WithLabelValues(t.plugin).Observe(time.Since(start).Seconds())
	return resp, err
}

// UpstreamOutcomeOf classifies one round trip. A transport error is the category
// upstream.Category gives it (timeout, tls, or unreachable); an answer is ok below 400, auth for
// 401 and 403, and otherwise its class, so the 5xx and 4xx codes do not each get a series.
func UpstreamOutcomeOf(resp *http.Response, err error) string {
	if err != nil {
		switch c := upstream.Category(err); c {
		case UpstreamTimeout, UpstreamTLS:
			return c
		}
		return UpstreamUnreachable
	}
	switch c := resp.StatusCode; {
	case c < 400:
		return UpstreamOK
	case c == http.StatusUnauthorized || c == http.StatusForbidden:
		return UpstreamAuth
	case c < 500:
		return UpstreamHTTP4xx
	}
	return UpstreamHTTP5xx
}

// pluginStates is dash_plugin_state, read at scrape time: one series per instance and state,
// 1 for the state it is in.
type pluginStates struct {
	desc  *prometheus.Desc
	infos func() []model.PluginInfo
}

func (c *pluginStates) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *pluginStates) Collect(ch chan<- prometheus.Metric) {
	if c.infos == nil {
		return
	}
	for _, p := range c.infos() {
		for _, s := range PluginStates {
			v := 0.0
			if p.State == s {
				v = 1
			}
			ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, v, p.ID, string(s))
		}
	}
}
