package api

import (
	"net/http"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// guard enforces the route table's access columns in one place, ahead of the handler: the
// principal must hold at least rt.Role, a route marked Interactive is for a person, not a
// credential that was minted for a program, and a route of scope logs is never for the anonymous
// viewer (the plugin routes get the same rule from serveRoute). A route without a Role (the
// catch-alls, the SPA and the sign-in routes) is public and is not wrapped.
//
// The principal comes from the auth middleware; a request that never passed it has the zero
// Principal, whose role grants nothing, so a route that is reached without auth fails closed.
// The refusal is one denial (audit record and metric) and 403 forbidden; handlers behind a
// guard need no role check of their own for the row's role. Plugin actions keep their own
// per-action MinRole check, which can only be stricter than the generic action row.
func (s *Server) guard(rt Route, next http.HandlerFunc) http.HandlerFunc {
	if rt.Role == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r.Context())
		if !p.Role.AtLeast(rt.Role) || (rt.Interactive && !p.Interactive()) || (p.Kind == auth.KindAnonymous && rt.Scope == "logs") {
			s.deny(r, obs.AuthForbidden)
			writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "forbidden")
			return
		}
		next(w, r)
	}
}
