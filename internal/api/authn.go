package api

import (
	"cmp"
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// authn resolves the principal of every /api request (the bearer step, then the configured
// authenticator) and stores it for the handlers (principalFrom, userFrom). A request it refuses
// gets the Failure's status and code, one denial (metric and audit record) and no further
// handling; a 401 carries the WWW-Authenticate challenge of its surface (design M.1). The cookie
// a session mode's authenticator returns (a renewed session, or the clearing cookie of one that
// failed) is set first, on a refusal too. An api token past its rate limit gets 429. Anything
// outside /api (the SPA, its assets) needs no principal.
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
			} else if fail.Status == http.StatusUnauthorized {
				w.Header().Set("WWW-Authenticate", s.wwwAuthenticate(r.URL.Path))
			}
			var details any
			if fail.Code == auth.CodeUnauthenticated {
				details = s.authDetails()
			}
			writeErrDetails(w, fail.Status, fail.Code, cmp.Or(fail.Message, strings.ReplaceAll(fail.Code, "_", " ")), details)
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p)
		r = r.WithContext(ctx)
		if p.Kind == auth.KindToken {
			markTokenForCORS(r)
		}
		if p.Kind == auth.KindToken && p.Audience == "api" {
			if ok, retry := s.tokenLimit.allow(rateKey(p), s.now()); !ok {
				s.deny(r, obs.AuthRateLimit)
				w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
				writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many requests for this token; try again later")
				return
			}
		}
		s.probe.see(p, peerOf(r))
		if p.Kind == auth.KindProxy {
			// The proxy mode sees a person's groups on every request (design M.8).
			s.syncTokens(p.User, p.Groups, nil)
		}
		next.ServeHTTP(w, r)
	})
}

// rateKey names the credential of a token principal: its personal access token or static token
// (TokenID), or its OAuth grant, whose access tokens share one limit.
func rateKey(p auth.Principal) string {
	if p.GrantID != "" {
		return "grant:" + p.GrantID
	}
	return p.TokenID
}

// authenticate resolves a request's principal: a bearer credential first (bearer), then the
// configured authenticator; a session mode's (auth.CookieAuthenticator) also returns the session
// cookie the response must carry, nil when there is none to set.
func (s *Server) authenticate(r *http.Request) (auth.Principal, *http.Cookie, *auth.Failure) {
	if p, fail, ok := s.bearer(r); ok {
		return p, nil, fail
	}
	if ca, ok := s.d.Auth.(auth.CookieAuthenticator); ok {
		return ca.AuthenticateCookie(r)
	}
	p, fail := s.d.Auth.Authenticate(r)
	return p, nil, fail
}

// authDenialReason maps a Failure's machine reason to the obs label it is counted under: the
// untrusted peer, the ambiguous identity, the invalid token and the ambiguous credentials have
// their own, every other refusal is "unauthenticated".
func authDenialReason(f *auth.Failure) obs.AuthReason {
	switch f.Reason {
	case auth.ReasonUntrustedPeer:
		return obs.AuthUntrustedPeer
	case auth.ReasonAmbiguousIdentity:
		return obs.AuthAmbiguousIdentity
	case auth.ReasonInvalidToken:
		return obs.AuthInvalidToken
	case auth.ReasonAmbiguousCredentials:
		return obs.AuthAmbiguousCredentials
	}
	return obs.AuthUnauthenticated
}

// authDetails is the details of a 401 unauthenticated (design D13): the mode, so the SPA knows
// how to sign the user in without an unauthenticated discovery endpoint, whether the local mode is
// still waiting for its administrator (true until an account exists, whatever the token's state),
// and in the oidc mode the provider's host for the login page.
func (s *Server) authDetails() model.AuthDetails {
	d := model.AuthDetails{Mode: s.authMode(auth.Principal{}), Setup: s.d.Local.SetupNeeded()}
	if s.d.OIDC != nil {
		d.OIDC = &model.AuthDetailsOIDC{Issuer: s.d.OIDC.IssuerHost()}
	}
	return d
}
