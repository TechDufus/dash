package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// authn resolves the principal of every /api request through the configured authenticator and
// stores it for the handlers (principalFrom, userFrom). A request the authenticator refuses gets
// the Failure's status and code, one denial (metric and audit record) and no further handling.
// The cookie a session mode's authenticator returns (a renewed session, or the clearing cookie of
// one that failed) is set first, on a refusal too. Anything outside /api (the SPA, its assets)
// needs no principal.
func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		p, cookie, fail := s.authenticate(r)
		if cookie != nil {
			http.SetCookie(w, cookie)
		}
		if fail != nil {
			s.deny(r, authDenialReason(fail))
			if fail.WWWAuth != "" {
				w.Header().Set("WWW-Authenticate", fail.WWWAuth)
			}
			var details any
			if fail.Code == auth.CodeUnauthenticated {
				details = s.authDetails()
			}
			writeErrDetails(w, fail.Status, fail.Code, strings.ReplaceAll(fail.Code, "_", " "), details)
			return
		}
		s.probe.see(p, peerOf(r))
		ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate asks the configured authenticator; a session mode's (auth.CookieAuthenticator)
// also returns the session cookie the response must carry, nil when there is none to set.
func (s *Server) authenticate(r *http.Request) (auth.Principal, *http.Cookie, *auth.Failure) {
	if ca, ok := s.d.Auth.(auth.CookieAuthenticator); ok {
		return ca.AuthenticateCookie(r)
	}
	p, fail := s.d.Auth.Authenticate(r)
	return p, nil, fail
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

// authDetails is the details of a 401 unauthenticated (design D13): the mode, so the SPA knows
// how to sign the user in without an unauthenticated discovery endpoint, whether the local mode is
// still waiting for its setup, and in the oidc mode the provider's host for the login page.
func (s *Server) authDetails() model.AuthDetails {
	d := model.AuthDetails{Mode: s.authMode(auth.Principal{}), Setup: s.d.Local.SetupOpen()}
	if s.d.OIDC != nil {
		d.OIDC = &model.AuthDetailsOIDC{Issuer: s.d.OIDC.IssuerHost()}
	}
	return d
}
