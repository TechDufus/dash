// Package api serves dash's REST endpoints, the SSE streams and the embedded SPA.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
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
}

// Server holds the handlers' state.
type Server struct {
	d         Deps
	static    fs.FS
	now       func() time.Time
	heartbeat time.Duration
	events    *streamLimiter // open /api/stream connections
	tails     *streamLimiter // open /api/logs/tail connections
	tailMax   time.Duration  // longest a log tail stays open
}

// New builds a Server serving the embedded SPA.
func New(d Deps) *Server {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil { // only if the embed root is renamed
		panic(err)
	}
	return &Server{
		d: d, static: dist, now: time.Now, heartbeat: 15 * time.Second,
		events:  newStreamLimiter(maxStreamsPerUser, maxStreams),
		tails:   newStreamLimiter(maxTailsPerUser, maxTails),
		tailMax: maxTailDuration,
	}
}

// Handler is the API plus the SPA, for the loopback listener.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", s.me)
	mux.HandleFunc("GET /api/config", s.uiConfig)
	mux.HandleFunc("GET /api/stream", s.stream)
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
	return s.csrf(s.auth(mux))
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

// Stream and request limits; constants until the layered config makes them settings.
const (
	maxStreamsPerUser = 16 // /api/stream connections per user
	maxStreams        = 256
	maxTailsPerUser   = 4 // /api/logs/tail connections per user
	maxTails          = 32
	maxTailDuration   = 30 * time.Minute
	sseWriteTimeout   = 10 * time.Second
	bodyReadTimeout   = 10 * time.Second
)

// decodeBody reads a JSON body into v; it answers 400 itself and reports false on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	rc := http.NewResponseController(w)
	if rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil {
		// Reset on return: the server's background read inherits the deadline, and once it
		// fires it cancels the request context.
		defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
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
