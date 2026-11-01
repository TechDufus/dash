package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// pluginStreamDoc describes the 200 response of every plugin stream in the OpenAPI document.
const pluginStreamDoc = "A stream of server-sent events: each event is named by the route and carries a JSON payload, " +
	"with an id the browser sends back as Last-Event-ID when it reconnects. A comment line every 15 seconds keeps " +
	"the connection alive, and the server ends the stream after its maximum duration. 403 when the namespace is " +
	"denied, 429 when the user has too many open streams, 503 when the server has."

// routeGuard is the core's policy for the routes of plugins, handed to Supervisor.Router with
// every request: it runs inside the plugin's mux, so the path values are set when the policy
// reads them.
func (s *Server) routeGuard() plugin.RouteGuard {
	return plugin.RouteGuard{Serve: s.serveRoute, Fail: s.pluginFail}
}

// serveRoute applies the read policy to one plugin route, in this order: the role, the
// namespace the request targets against the instance's ReadDenyNamespaces (403, audited), then
// for a plain route its handler, and for a stream the stream pipeline. The namespace check comes
// before a stream takes a slot, so a denied tail answers 403 even when every slot is taken.
func (s *Server) serveRoute(w http.ResponseWriter, r *http.Request, mr plugin.MountedRoute, next http.HandlerFunc) {
	if !principalFrom(r.Context()).Role.AtLeast(auth.RoleViewer) {
		s.deny(r, obs.AuthForbidden)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "forbidden")
		return
	}
	if f := mr.Info.Spec.Namespace; f != nil {
		if ns := f(r); ns != "" && slices.Contains(mr.ReadDenyNamespaces, ns) {
			s.deny(r, obs.AuthNamespaceDenied)
			writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "this namespace is not available")
			return
		}
	}
	if mr.Info.Stream != nil {
		s.servePluginStream(w, r, mr)
		return
	}
	next(w, r)
}

// pluginFail answers an error of a plugin's route handler (plugin.Fail): a refusal the plugin
// chose as it is, an input error as 400, anything else as the category-only 502 of every
// upstream failure.
func (s *Server) pluginFail(w http.ResponseWriter, _ *http.Request, id plugin.ID, err error) {
	var ae *plugin.ActionError
	switch {
	case errors.As(err, &ae):
		s.writeActionError(w, ae)
	case errInvalid(err):
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, s.d.Secrets.Scrub(err.Error()))
	default:
		s.badGateway(w, string(id), err)
	}
}

// streamAuditAction is the audit action of a log tail opened through the stream pipeline; other
// scopes record a plain stream.open.
func streamAuditAction(spec *plugin.StreamSpec) string {
	if spec.Scope == "logs" {
		return "logs.tail"
	}
	return "stream.open"
}

// servePluginStream runs a plugin's stream: the slot of the tail limiter (429 per user, 503 in
// total), the audit record of the open, the maximum duration, the write deadline and the
// heartbeat. What flows is the plugin's StreamHandler, written through Stream.
func (s *Server) servePluginStream(w http.ResponseWriter, r *http.Request, mr plugin.MountedRoute) {
	spec := mr.Info.Stream
	release, ok := s.tails.acquire(w, userFrom(r.Context()))
	if !ok {
		s.deny(r, obs.AuthStreamLimit)
		return
	}
	defer release()
	defer s.d.Metrics.StreamOpened(obs.StreamTail)()

	var target audit.Target
	if spec.Target != nil {
		kind, ns, name := spec.Target(r)
		target = audit.Target{Kind: kind, Namespace: ns, Name: name}
	}
	rec := s.auditRecord(r, streamAuditAction(spec), target)
	rec.Plugin = string(mr.Instance)
	s.d.Audit.Event(rec)

	// When the deadline ends the stream, the browser's EventSource reconnects by itself.
	ctx, cancel := context.WithTimeout(r.Context(), s.tailMax)
	defer cancel()
	// The stream ends with the instance that serves it: a reconfiguration (a new deny list), a
	// disable or a shutdown stops the plugin, and its open streams must not outlive it.
	if mr.Context != nil {
		defer context.AfterFunc(mr.Context, cancel)()
	}
	st := &pluginStream{s: s, w: w, ctx: ctx}
	// The heartbeat writes to w, so it must be finished before the handler returns.
	defer st.finish()
	err := mr.Info.StreamHandler(ctx, r, principalFrom(r.Context()), st)
	if err == nil || st.began() {
		if err != nil && ctx.Err() == nil {
			slog.Warn("stream ended", "plugin", mr.Instance, "error", s.d.Secrets.Scrub(err.Error()))
		}
		return
	}
	s.pluginFail(w, r, mr.Instance, err)
}

// pluginStream is the plugin.Stream of one open response.
type pluginStream struct {
	s    *Server
	w    http.ResponseWriter
	ctx  context.Context
	mu   sync.Mutex
	e    *sseWriter
	done chan struct{} // closed when the heartbeat has ended
	stop context.CancelFunc
}

func (p *pluginStream) began() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.e != nil
}

// Begin implements plugin.Stream: the SSE headers, the retry hint and, from then on, the
// heartbeat.
func (p *pluginStream) Begin() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.e != nil {
		return errors.New("stream already begun")
	}
	e, err := startSSE(p.w, p.s.sseWrite)
	p.e = e
	hbCtx, stop := context.WithCancel(p.ctx)
	p.stop, p.done = stop, make(chan struct{})
	go func() {
		defer close(p.done)
		e.pings(hbCtx, p.s.heartbeat)
	}()
	return err
}

// Event implements plugin.Stream.
func (p *pluginStream) Event(event, id string, data any) error {
	p.mu.Lock()
	e := p.e
	p.mu.Unlock()
	if e == nil {
		return errors.New("stream not begun")
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id == "" {
		return e.event(event, b)
	}
	return e.eventID(event, id, b)
}

// finish stops the heartbeat, waits for it, and gives net/http's closing chunk a fresh write
// deadline.
func (p *pluginStream) finish() {
	p.mu.Lock()
	e, stop, done := p.e, p.stop, p.done
	p.mu.Unlock()
	if e == nil {
		return
	}
	stop()
	<-done
	e.done()
}
