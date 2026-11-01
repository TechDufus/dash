// Package api serves dash's REST endpoints, the SSE streams and the embedded SPA.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
	"github.com/TechDufus/dash/web"
)

// AlertmanagerAPI is the slice of upstream.Alertmanager the API uses.
type AlertmanagerAPI interface {
	CreateSilence(ctx context.Context, s upstream.SilenceSpec) (string, error)
	ExpireSilence(ctx context.Context, id string) error
}

// VLogsAPI is the slice of upstream.VLogs the API uses.
type VLogsAPI interface {
	Query(ctx context.Context, q upstream.LogQuery) ([]model.LogLine, error)
	Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error
}

// DataAPI is the slice of topics.Manager the API uses.
type DataAPI interface {
	PodDetail(ctx context.Context, ns, name string) (model.PodDetail, error)
	NodeIP(name string) (string, bool)
	VMDetail(ctx context.Context, vmid int) (model.VMDetail, error)
}

// PluginsAPI is the slice of plugin.Supervisor the API uses.
type PluginsAPI interface {
	Infos() []model.PluginInfo
	Manifests() []plugin.Manifest
	Action(instance plugin.ID, name string) (plugin.ActionRef, bool)
	// Router serves the routes of a running instance through g, the core's route policy.
	Router(instance plugin.ID, g plugin.RouteGuard) (http.Handler, bool)
	// Service is what a running instance's Provider exports, for the code that is not a plugin.
	Service(id plugin.ID) (any, bool)
	// Links resolves deep links through the running link providers.
	Links() plugin.LinkResolver
	Series(name string) (plugin.SeriesSpec, plugin.ID, bool)
	Ready() bool
}

// Deps are the collaborators of a Server.
type Deps struct {
	Cfg     config.Config
	Version string
	Hub     *hub.Hub
	Kube    kubernetes.Interface
	Dyn     dynamic.Interface
	AM      AlertmanagerAPI
	VLogs   VLogsAPI
	Data    DataAPI
	Plugins PluginsAPI   // nil = no plugins
	Health  HealthSource // readiness; see health.go
	Metrics *obs.Metrics // nil = metrics off; every method is nil-safe
	// Secrets scrubs upstream errors and log lines; nil only matches credential shapes.
	Secrets *secret.Registry
	Audit   *audit.Log // nil logs to slog only
}

// Server holds the handlers' state.
type Server struct {
	d           Deps
	static      fs.FS
	now         func() time.Time
	heartbeat   time.Duration
	events      *streamLimiter // open /api/v1/stream connections
	tails       *streamLimiter // open /api/logs/tail connections
	tailMax     time.Duration  // longest a log tail stays open
	sseWrite    time.Duration  // deadline of each SSE write
	bodyTimeout time.Duration  // longest a request body may take to arrive
	actionLimit *rateLimiter   // framework actions per principal
	openTarget  *rateLimiter   // stream-open audit records per user and target
	openUser    *rateLimiter   // stream-open audit records per user
	hostWarn    atomic.Int64   // unix nanos of the last unknown-host warning
	serving     atomic.Bool    // the listeners are bound (MarkServing)
	schemas     sync.Map       // reflect.Type -> json.RawMessage: action input schemas
}

// New builds a Server serving the embedded SPA.
func New(d Deps) *Server {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil { // only if the embed root is renamed
		panic(err)
	}
	l := withDefaultLimits(d.Cfg.Limits)
	return &Server{
		d: d, static: dist, now: time.Now, heartbeat: 15 * time.Second,
		events:  newStreamLimiter(l.StreamsPerUser, l.Streams),
		tails:   newStreamLimiter(l.TailsPerUser, l.Tails),
		tailMax: l.TailMaxDuration, sseWrite: l.SSEWriteTimeout, bodyTimeout: l.BodyReadTimeout,
		actionLimit: newRateLimiter(actionRateLimit, actionRateWindow, maxRateKeys),
		openTarget:  newRateLimiter(1, openRecordWindow, maxRateKeys),
		openUser:    newRateLimiter(openRecordPerUser, openRecordWindow, maxRateKeys),
	}
}

// Handler is the API plus the SPA, for the loopback listener. The chain is request id, host
// check, CSRF, authentication, then the mux the route table builds.
func (s *Server) Handler() http.Handler {
	return s.d.Metrics.Instrument(s.requestID(s.hosts(s.csrf(s.auth(s.mux())))))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers the L.2 error envelope: msg is text for a person, code one of model.ErrCode*.
// The request id is the one requestID put on the response.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, model.APIError{Error: msg, Code: code, RequestID: w.Header().Get(requestIDHeader)})
}

// maxBody caps JSON request bodies.
const maxBody = 64 << 10

// maxTailResume is how far back a reconnecting log tail replays. The other stream and request
// limits are settings (server.limits in internal/config).
const maxTailResume = time.Minute

// decodeBody reads a JSON body into v; it answers 400 itself and reports false on failure.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return s.decode(w, r, v, false)
}

// decodeStrict is decodeBody for a body with a declared shape: a field v does not have is an
// error (the action framework's input structs).
func (s *Server) decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	return s.decode(w, r, v, true)
}

// decode is decodeBody and decodeStrict.
//
// The read deadline bounds the whole body. On failure it is left set, so net/http's drain of
// the unread remainder fails at once and closes the connection instead of waiting for a
// client that stalled mid-body. On success the rest of the body is read while the deadline
// still applies, then the deadline is cleared. On Go 1.27 net/http clears it itself when it
// starts the background read after the body is drained; the explicit clear keeps a deadline
// from outliving the body (and, once it fired, cancelling the request context of a handler
// that outlasts the timeout) independent of the Go version.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any, strict bool) bool {
	rc := http.NewResponseController(w)
	armed := rc.SetReadDeadline(time.Now().Add(s.bodyTimeout)) == nil
	body := http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid request body")
		return false
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid request body")
		return false
	}
	if armed {
		_ = rc.SetReadDeadline(time.Time{})
	}
	return true
}

// firstOf returns the first non-empty query value among keys.
func firstOf(r *http.Request, keys ...string) string {
	q := r.URL.Query()
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// me answers GET /api/v1/me (design L.4). Scopes are what the role allows: read and logs, and
// actions:<plugin> for each instance with mounted actions. mcp, push and oauth are phase 7.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	me := model.Me{
		User: p.User, Role: string(p.Role), AuthMode: s.authMode(), Audience: "api", Version: s.d.Version,
		Scopes: []string{"read", "logs"},
		UI:     model.MeUI{IconBaseURL: s.d.Cfg.IconBaseURL},
	}
	if s.d.Plugins != nil {
		for _, pi := range s.d.Plugins.Infos() {
			// An action the principal's role cannot run grants neither its scope nor the capability:
			// a viewer is not offered controls the server would refuse (design D5).
			if slices.ContainsFunc(pi.Actions, func(a model.ActionInfo) bool { return p.Role.AtLeast(auth.Role(a.MinRole)) }) {
				me.Scopes = append(me.Scopes, "actions:"+pi.ID)
				me.Capabilities.Actions = true
			}
		}
	}
	writeJSON(w, http.StatusOK, me)
}

func (s *Server) uiConfig(w http.ResponseWriter, _ *http.Request) {
	launcher := s.d.Cfg.Launcher
	if launcher == nil {
		launcher = []model.LauncherLink{}
	}
	writeJSON(w, http.StatusOK, model.UIConfig{HostModel: s.d.Cfg.HostModel, Launcher: launcher})
}

// errInvalid reports whether err is an upstream input validation failure.
func errInvalid(err error) bool { return errors.Is(err, upstream.ErrInvalid) }
