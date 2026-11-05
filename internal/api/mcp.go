package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	dashmcp "github.com/TechDufus/dash/internal/mcp"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
)

// mcpGate is the part of the /mcp policy that runs before any credential is looked at (design
// N.1): with MCP off the path is a JSON 404, whatever the request carries; a present Origin must
// be the public URL's or a listed CORS origin (403 otherwise), since a browser page may send it.
func (s *Server) mcpGate(w http.ResponseWriter, r *http.Request) bool {
	if !s.mcpEnabled() {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != s.d.Cfg.PublicOrigin() && !slices.Contains(s.d.Cfg.CORSOrigins, o) {
		s.deny(r, obs.AuthCSRF)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "origin not allowed")
		return false
	}
	return true
}

// mcpPrincipal resolves the principal of a /mcp request: a bearer token of audience mcp and
// nothing else. Neither the session cookie nor the identity proxy's user reaches MCP.
func (s *Server) mcpPrincipal(r *http.Request) (auth.Principal, *auth.Failure) {
	p, fail, ok := s.bearer(r)
	if !ok {
		return auth.Principal{}, &auth.Failure{
			Status: http.StatusUnauthorized, Code: auth.CodeInvalidToken, Reason: auth.ReasonInvalidToken,
			Message: "a bearer token with the mcp audience is required",
		}
	}
	return p, fail
}

// serveMCP hands an authenticated /mcp request to the MCP server.
func (s *Server) serveMCP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	if p.Kind != auth.KindToken || p.Audience != "mcp" { // authn guarantees it; fail closed anyway
		writeErr(w, http.StatusUnauthorized, model.ErrCodeUnauthenticated, "unauthenticated")
		return
	}
	s.mcpOnce.Do(func() {
		s.mcp = dashmcp.New(dashmcp.Options{Backend: mcpBackend{s}, Version: s.d.Version, Scrub: s.d.Secrets.ScrubLogLine})
	})
	s.mcp.Handler(w, r, p)
}

// mcpBackend is what the MCP tools read, answered by the API's own code under its policies.
type mcpBackend struct{ s *Server }

func (b mcpBackend) Overview() (model.OverviewData, bool) { return b.s.overview() }

func (b mcpBackend) Search(query string, limit int) []model.SearchHit {
	out := []model.SearchHit{}
	if b.s.d.Plugins == nil {
		return out
	}
	for _, id := range b.s.d.Plugins.ObjectInstances() {
		if len(out) >= limit {
			break
		}
		if o, ok := b.s.d.Plugins.Objects(id); ok {
			out = append(out, o.Search(query, limit-len(out))...)
		}
	}
	return out
}

func (b mcpBackend) Get(ctx context.Context, ref string) (any, error) {
	id, kind, _, ok := model.ParseRef(ref)
	if !ok {
		return nil, &dashmcp.Error{Code: model.ErrCodeInvalidInput, Message: "invalid ref"}
	}
	unknown := &dashmcp.Error{Code: model.ErrCodeNotFound, Message: "unknown object"}
	if plugin.RefusedKind(kind) || b.s.d.Plugins == nil {
		return nil, unknown
	}
	o, ok := b.s.d.Plugins.Objects(plugin.ID(id))
	if !ok {
		return nil, unknown
	}
	v, err := o.Get(ctx, ref)
	if errors.Is(err, plugin.ErrNotFound) {
		return nil, unknown
	}
	return v, err
}

func (b mcpBackend) Topic(user, topic string) (model.Envelope, bool) {
	var env model.Envelope
	if b.s.d.Hub == nil {
		return env, false
	}
	ev, ok := b.s.d.Hub.Latest(user, model.Topic(topic))
	if !ok || json.Unmarshal(ev.Data, &env) != nil {
		return env, false
	}
	return env, true
}

func (b mcpBackend) Series(ctx context.Context, name, rng string, args map[string]string) ([]model.SeriesLine, int, error) {
	a := queries.Args{VMID: args["vmid"], Namespace: args["namespace"], Pod: args["pod"], Instance: args["node"]}
	lines, step, f := b.s.readSeries(ctx, name, rng, a)
	if f != nil {
		return nil, 0, failureError(f)
	}
	return lines, int(step / time.Second), nil
}

func (b mcpBackend) Logs(r *http.Request, ref model.LogsRef, since, query string, limit int) ([]model.LogLine, error) {
	if !mayReadLogs(principalFrom(r.Context())) {
		return nil, &dashmcp.Error{Code: model.ErrCodeInsufficientScope, Message: "this token lacks the logs scope"}
	}
	lines, note := b.s.readLogs(r, ref, since, query, limit)
	switch note {
	case "":
		return lines, nil
	case logsDenied:
		return nil, &dashmcp.Error{Code: model.ErrCodeForbidden, Message: "the logs of this namespace are not available"}
	}
	return nil, errors.New("logs unavailable")
}

func (b mcpBackend) Context(r *http.Request, ref string, o model.ContextOptions) (model.ContextBundle, error) {
	bun, f := b.s.buildContext(r, ref, o)
	if f != nil {
		return bun, failureError(f)
	}
	return bun, nil
}

// failureError is f as the MCP tools report it: a refusal, or the upstream error.
func failureError(f *failure) error {
	if f.Upstream != nil {
		return f.Upstream
	}
	return &dashmcp.Error{Code: f.Code, Message: f.Message}
}
