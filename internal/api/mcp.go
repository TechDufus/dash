package api

import (
	"net/http"
	"slices"

	"github.com/TechDufus/dash/internal/auth"
	dashmcp "github.com/TechDufus/dash/internal/mcp"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
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

// mcpBackend is what the MCP tools read, answered by the API's own code.
type mcpBackend struct{ s *Server }

func (b mcpBackend) Overview() (model.OverviewData, bool) { return b.s.overview() }
