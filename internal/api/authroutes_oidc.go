package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/oidcauth"
)

// Where a refused OIDC callback lands: the login page shows the reason, and nothing else about
// the refusal leaves the server.
const (
	oidcNotAllowedURL = "/login?error=not_allowed"
	oidcFailedURL     = "/login?error=oidc_failed"
)

// oidcStart begins the OIDC login (GET /auth/oidc/start?next=): a 302 to the identity provider
// with the sealed state cookie; 400 invalid_input for a next that is not a path on this origin;
// 503 upstream_unavailable, with a generic message, when discovery fails.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.d.OIDC == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	loc, state, err := s.d.OIDC.Start(r.Context(), r.URL.Query().Get("next"))
	switch {
	case errors.Is(err, oidcauth.ErrInvalidNext):
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid next path")
	case errors.Is(err, oidcauth.ErrUnavailable):
		slog.Warn("oidc discovery failed", "requestId", w.Header().Get(requestIDHeader), "error", s.d.Secrets.Scrub(err.Error()))
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable, "identity provider unavailable")
	case err != nil:
		slog.Error("oidc start failed", "requestId", w.Header().Get(requestIDHeader), "error", s.d.Secrets.Scrub(err.Error()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
	default:
		http.SetCookie(w, state)
		found(w, loc)
	}
}

// oidcCallback completes the OIDC login (GET /auth/callback?code&state): the state cookie is
// cleared in every outcome; success sets the session cookie and goes to the sealed next; an
// account outside the allow-lists goes to /login?error=not_allowed (audited, the attempted name as
// the target); any other refusal goes to /login?error=oidc_failed with the reason in the log only.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.d.OIDC == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	http.SetCookie(w, s.d.OIDC.ClearState())
	login, err := s.d.OIDC.Callback(r)
	switch {
	case errors.Is(err, oidcauth.ErrNotAllowed):
		s.d.Metrics.AuthFailure(obs.AuthLoginFailed)
		rec := s.oidcRecord(r, "auth.login.failed", "")
		rec.Target = audit.Target{Kind: "user", Name: login.User}
		s.d.Audit.Denial(peerOf(r), "not_allowed", rec)
		found(w, oidcNotAllowedURL)
	case err != nil:
		s.d.Metrics.AuthFailure(obs.AuthLoginFailed)
		stage := "unknown"
		if f, ok := errors.AsType[*oidcauth.Failure](err); ok {
			stage = f.Stage
		}
		slog.Warn("oidc login refused", "stage", stage, "peer", peerOf(r), "requestId", w.Header().Get(requestIDHeader),
			"error", s.d.Secrets.Scrub(err.Error()))
		found(w, oidcFailedURL)
	default:
		http.SetCookie(w, login.Cookie)
		s.d.Audit.Event(s.oidcRecord(r, "auth.login", login.User))
		found(w, login.Next)
	}
}

// oidcRecord is an audit record of the OIDC login: the callback runs before any principal exists,
// so the mode is named here, and the user only for a login that succeeded (a denial with a user
// would be rate limited per attempted name instead of per peer).
func (s *Server) oidcRecord(r *http.Request, action, user string) audit.Record {
	return audit.Record{
		Action: action, Peer: peerOf(r), AuthMode: auth.ModeOIDC, RequestID: requestIDFrom(r.Context()),
		Actor: audit.Actor{Type: auth.ModeOIDC, User: user, Surface: "ui"},
	}
}

// found answers 302 to loc, which is either the IdP's authorization URL or a path this server
// validated: it is set as given, without http.Redirect's rewriting.
func found(w http.ResponseWriter, loc string) {
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}
