package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/obs"
)

// authn resolves the principal of every /api request through the configured authenticator and
// stores it for the handlers (principalFrom, userFrom). A request the authenticator refuses gets
// the Failure's status and code, one denial (metric and audit record) and no further handling.
// Anything outside /api (the SPA, its assets) needs no principal.
func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		p, fail := s.d.Auth.Authenticate(r)
		if fail != nil {
			s.deny(r, authDenialReason(fail))
			if fail.WWWAuth != "" {
				w.Header().Set("WWW-Authenticate", fail.WWWAuth)
			}
			writeErr(w, fail.Status, fail.Code, strings.ReplaceAll(fail.Code, "_", " "))
			return
		}
		s.probe.see(p, peerOf(r))
		ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authDenialReason maps a Failure's machine reason to the obs label it is counted under: the
// untrusted peer and the ambiguous identity have their own, every other refusal is
// "unauthenticated".
func authDenialReason(f *auth.Failure) obs.AuthReason {
	switch f.Reason {
	case auth.ReasonUntrustedPeer:
		return obs.AuthUntrustedPeer
	case auth.ReasonAmbiguousIdentity:
		return obs.AuthAmbiguousIdentity
	}
	return obs.AuthUnauthenticated
}
