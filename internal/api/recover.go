package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
)

// recovered is the outermost layer around a route's handler (outside Mark, so the route label is
// already set): a panic in code a plugin supplied and the API calls (ActionSpec.Target and
// Input, an input's Validate, SeriesSpec.Expr, a Router's handlers) answers 500 internal instead
// of dropping the connection. Act and the supervisor's own calls are recovered where they run.
func (s *Server) recovered(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sw := &startedWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				s.answerPanic(sw, sw, r, rec)
			}
		}()
		h(sw, r)
	}
}

// answerPanic logs the panic (scrubbed, with its stack) and answers 500 on w unless started
// shows the response already began. w is where to write (a metrics wrapper may sit between it
// and started's writer); started may be nil when nothing tracks the response.
//
// http.ErrAbortHandler is the one panic net/http means to propagate: it is re-raised.
func (s *Server) answerPanic(w http.ResponseWriter, started *startedWriter, r *http.Request, rec any) {
	if rec == http.ErrAbortHandler {
		panic(rec)
	}
	slog.Error("panic in a handler", "route", r.Pattern, "panic", s.d.Secrets.Scrub(fmt.Sprint(rec)), "stack", string(debug.Stack()))
	if started != nil && started.wrote {
		return
	}
	writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
}

// refuse records a refused request in the audit log without counting it as an auth failure
// (deny does both); the log keeps at most one record per peer and reason a minute.
func (s *Server) refuse(r *http.Request, reason string) {
	line := r.Method + " " + r.URL.Path
	if len(line) > maxAuditAction {
		line = line[:maxAuditAction]
	}
	s.d.Audit.Denial(peerOf(r), reason, s.auditRecord(r, line, audit.Target{}))
}

// startedWriter remembers whether the handler began its answer, so a recovered panic does not
// write a second one over it. It keeps Flush and the deadline methods through Unwrap.
type startedWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *startedWriter) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols { // 1xx are informational
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *startedWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *startedWriter) Flush() {
	w.wrote = true
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *startedWriter) FlushError() error {
	w.wrote = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *startedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
