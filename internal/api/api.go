// Package api serves dash's REST endpoints, the SSE streams and the embedded SPA.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync/atomic"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
	"github.com/TechDufus/dash/web"
)

// PromAPI is the slice of upstream.Prom the API uses.
type PromAPI interface {
	Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error)
}

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

// Deps are the collaborators of a Server.
type Deps struct {
	Cfg     config.Config
	Version string
	Hub     *hub.Hub
	Kube    kubernetes.Interface
	Dyn     dynamic.Interface
	Prom    PromAPI
	AM      AlertmanagerAPI
	VLogs   VLogsAPI
	Data    DataAPI
	Ready   func() bool
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
	events      *streamLimiter // open /api/stream connections
	tails       *streamLimiter // open /api/logs/tail connections
	tailMax     time.Duration  // longest a log tail stays open
	sseWrite    time.Duration  // deadline of each SSE write
	bodyTimeout time.Duration  // longest a request body may take to arrive
	hostWarn    atomic.Int64   // unix nanos of the last unknown-host warning
}

// New builds a Server serving the embedded SPA.
func New(d Deps) *Server {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil { // only if the embed root is renamed
		panic(err)
	}
	l := d.Cfg.Limits
	return &Server{
		d: d, static: dist, now: time.Now, heartbeat: 15 * time.Second,
		events:  newStreamLimiter(l.StreamsPerUser, l.Streams),
		tails:   newStreamLimiter(l.TailsPerUser, l.Tails),
		tailMax: l.TailMaxDuration, sseWrite: l.SSEWriteTimeout, bodyTimeout: l.BodyReadTimeout,
	}
}

// Handler is the API plus the SPA, for the loopback listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/config", s.uiConfig)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/v1/topics/{topic}", s.topic)
	mux.HandleFunc("GET /api/series", s.series)
	mux.HandleFunc("GET /api/vms/{vmid}", s.vmDetail)
	mux.HandleFunc("GET /api/pods/{ns}/{name}", s.podDetail)
	mux.HandleFunc("GET /api/logs", s.logs)
	mux.HandleFunc("GET /api/logs/tail", s.logsTail)
	mux.HandleFunc("POST /api/actions/restart", s.restart)
	mux.HandleFunc("POST /api/actions/reconcile", s.reconcile)
	mux.HandleFunc("POST /api/silences", s.createSilence)
	mux.HandleFunc("DELETE /api/silences/{id}", s.expireSilence)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/", s.serveStatic)
	return s.hosts(s.csrf(s.auth(mux)))
}

// HealthHandler serves only /healthz and /readyz, for the unauthenticated health listener.
func (s *Server) HealthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if s.d.Ready != nil && s.d.Ready() {
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready"))
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, model.APIError{Error: msg})
}

// maxBody caps JSON request bodies.
const maxBody = 64 << 10

// maxTailResume is how far back a reconnecting log tail replays. The other stream and request
// limits are settings (server.limits in internal/config).
const maxTailResume = time.Minute

// decodeBody reads a JSON body into v; it answers 400 itself and reports false on failure.
//
// The read deadline bounds the whole body. On failure it is left set, so net/http's drain of
// the unread remainder fails at once and closes the connection instead of waiting for a
// client that stalled mid-body. On success the rest of the body is read while the deadline
// still applies, then the deadline is cleared. On Go 1.27 net/http clears it itself when it
// starts the background read after the body is drained; the explicit clear keeps a deadline
// from outliving the body (and, once it fired, cancelling the request context of a handler
// that outlasts the timeout) independent of the Go version.
func (s *Server) decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	rc := http.NewResponseController(w)
	armed := rc.SetReadDeadline(time.Now().Add(s.bodyTimeout)) == nil
	body := http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
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

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, model.Me{User: userFrom(r.Context()), Version: s.d.Version})
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
