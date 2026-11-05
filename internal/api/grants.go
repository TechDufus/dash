package api

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// grantRoutes are the consent and grant routes (design M.6, contract G7-R3): for people only
// (Interactive: a token gets 403), CSRF-checked like every write, and audited fail-closed.
// Without the authorization server each answers 404.
func (s *Server) grantRoutes() []Route {
	row := func(r Route) Route {
		r.Role, r.Interactive, r.Tag = auth.RoleViewer, true, "OAuth"
		return r
	}
	idParam := []plugin.Param{{Name: "id", Doc: "The ID in the consent page's address, /consent/<id>.", Required: true}}
	return []Route{
		row(Route{
			ID: "getOAuthConsent", Method: "GET", Path: "/api/v1/oauth/consents/{id}",
			Summary: "An authorization request waiting for consent",
			Description: "Only the person who sent the request sees it. 404 for a request that does not exist, expired (10 minutes) or " +
				"is another person's; 403 when the caller may grant none of the scopes the application asks for.",
			PathParams: idParam, Response: model.OAuthConsent{}, Handler: s.getConsent,
		}),
		row(Route{
			ID: "decideOAuthConsent", Method: "POST", Path: "/api/v1/oauth/consents/{id}",
			Summary: "Approve or deny an authorization request",
			Description: "Approving creates the grant; the answer is the address to send the browser to, with code, state and iss " +
				"(or error=access_denied). 400 invalid_input for scopes outside the offered ones; 404 as for GET; 409 grant_limit " +
				"past 20 grants; 503 audit_unavailable when the audit log cannot be written.",
			PathParams: idParam, Request: model.OAuthConsentDecision{}, Response: model.OAuthConsentResult{}, Handler: s.decideConsent,
		}),
		row(Route{
			ID: "listOAuthGrants", Method: "GET", Path: "/api/v1/oauth/grants",
			Summary:     "List OAuth grants (connected apps)",
			Description: "The caller's own grants; an administrator may list one user's (?user=<name>) or everyone's (?user=*).",
			Query:       []plugin.Param{{Name: "user", Doc: "Administrators only: a user name, or * for every user. Absent or empty: the caller's own."}},
			Response:    model.OAuthGrantList{}, Handler: s.listGrants,
		}),
		row(Route{
			ID: "revokeOAuthGrant", Method: "DELETE", Path: "/api/v1/oauth/grants/{id}",
			Summary: "Revoke an OAuth grant",
			Description: "Its access and refresh tokens stop working on the next request: one's own grant, or (administrators) " +
				"anyone's. 404 for a grant that is not the caller's; 503 audit_unavailable when the audit log cannot be written.",
			PathParams: []plugin.Param{{Name: "id", Doc: "The grant's ID.", Required: true}},
			Status:     http.StatusNoContent, Handler: s.revokeGrant,
		}),
		row(Route{
			ID: "revokeMyOAuthGrants", Method: "DELETE", Path: "/api/v1/oauth/grants",
			Summary: "Revoke all of one's OAuth grants",
			Description: "?user=me revokes every grant of the caller (the identity proxy's \"Revoke all my connected apps\": " +
				"proxy mode has no logout). Personal access tokens are not touched. 503 audit_unavailable when the audit log " +
				"cannot be written.",
			Query:  []plugin.Param{{Name: "user", Doc: "Must be me.", Required: true}},
			Status: http.StatusNoContent, Handler: s.revokeMyGrants,
		}),
	}
}

func (s *Server) oauthReady(w http.ResponseWriter, r *http.Request) bool {
	if s.d.OAuth == nil || s.d.Credentials == nil {
		notFound(w, r)
		return false
	}
	return true
}

// grantInfo is the wire form of a stored grant.
func grantInfo(g credentials.Grant) model.OAuthGrantInfo {
	return model.OAuthGrantInfo{
		ID: g.ID, User: g.User, Client: oauth.ClientInfo(g.ClientID, g.ClientName, g.ClientKind), Audience: g.Audience,
		Scopes: slices.Clone(g.Scopes), CreatedAt: g.CreatedAt, ExpiresAt: g.ExpiresAt, LastUsedAt: g.LastUsedAt,
	}
}

func (s *Server) getConsent(w http.ResponseWriter, r *http.Request) {
	if !s.oauthReady(w, r) {
		return
	}
	c, err := s.d.OAuth.Consent(r.PathValue("id"), principalFrom(r.Context()))
	switch {
	case errors.Is(err, oauth.ErrConsentNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such authorization request; it may have expired")
	case errors.Is(err, oauth.ErrNothingToGrant):
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

func writeGrantLimit(w http.ResponseWriter) {
	writeErr(w, http.StatusConflict, model.ErrCodeGrantLimit, "you hold the most connected apps allowed (20); revoke one in Settings -> Connected apps first")
}

func (s *Server) decideConsent(w http.ResponseWriter, r *http.Request) {
	if !s.oauthReady(w, r) {
		return
	}
	var body model.OAuthConsentDecision
	if !s.decodeStrict(w, r, &body) {
		return
	}
	p := principalFrom(r.Context())
	id := r.PathValue("id")
	if !body.Approve {
		redirect, err := s.d.OAuth.Deny(id, p)
		if err != nil {
			writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such authorization request; it may have expired")
			return
		}
		writeJSON(w, http.StatusOK, model.OAuthConsentResult{Redirect: redirect})
		return
	}
	if s.d.Credentials.ActiveGrants(p.User, s.now()) >= credentials.MaxActiveGrants {
		writeGrantLimit(w)
		return
	}
	// Intent, then the grant, then the outcome (contract D6): an intent that cannot be written
	// is 503 and no grant is created.
	var pending audit.Pending
	var intended bool
	auditDown := errors.New("audit unavailable")
	redirect, err := s.d.OAuth.Approve(id, p, body.Scopes, func(g credentials.Grant) error {
		rec := s.auditRecord(r, auditGrantCreate, grantTarget(g))
		rec.Actor.ClientID = g.ClientID
		pd, err := s.d.Audit.Intent(rec)
		if err != nil {
			return auditDown
		}
		pending, intended = pd, true
		return nil
	})
	if intended {
		pending.Outcome(err)
	}
	switch {
	case errors.Is(err, auditDown):
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable, "audit log unavailable")
	case errors.Is(err, oauth.ErrConsentNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such authorization request; it may have expired")
	case errors.Is(err, oauth.ErrScopes):
		writeErrDetails(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error(),
			&model.SettingsErrorDetails{Fields: []model.SettingsFieldError{{Path: "scopes", Message: "must be a non-empty subset of the offered scopes"}}})
	case errors.Is(err, credentials.ErrTooManyGrants):
		writeGrantLimit(w)
	case errors.Is(err, oauth.ErrBusy):
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, err.Error())
	case err != nil:
		slog.Error("oauth consent: the grant could not be stored", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the grant could not be stored")
	default:
		writeJSON(w, http.StatusOK, model.OAuthConsentResult{Redirect: redirect})
	}
}

func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	if !s.oauthReady(w, r) {
		return
	}
	p := principalFrom(r.Context())
	user := p.User
	if u := strings.Join(r.URL.Query()["user"], ","); u != "" { // an empty ?user= is the caller's own
		if u != p.User && !p.Role.AtLeast(auth.RoleAdmin) {
			s.deny(r, obs.AuthForbidden)
			writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "only an administrator lists another user's connected apps")
			return
		}
		user = u
		if u == "*" {
			user = "" // every user's
		}
	}
	list := model.OAuthGrantList{Grants: []model.OAuthGrantInfo{}}
	for _, g := range s.d.Credentials.Grants(user) {
		if g.Active(s.now()) {
			list.Grants = append(list.Grants, grantInfo(g))
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) revokeGrant(w http.ResponseWriter, r *http.Request) {
	if !s.oauthReady(w, r) {
		return
	}
	p := principalFrom(r.Context())
	g, found := s.d.Credentials.GrantByID(r.PathValue("id"))
	// Another user's grant is as unknown as a missing one to a viewer: its ID tells nothing.
	if !found || (g.User != p.User && !p.Role.AtLeast(auth.RoleAdmin)) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such grant")
		return
	}
	rec := s.auditRecord(r, auditGrantRevoke, grantTarget(g))
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return
	}
	_, _, err := s.d.Credentials.RevokeGrant(g.ID)
	pending.Outcome(err)
	if err != nil {
		slog.Error("grant revoke: credentials.yaml could not be written; the grant is revoked until dash restarts", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the grant is revoked until dash restarts: credentials.yaml could not be written")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeMyGrants revokes every grant of the caller (?user=me), the proxy mode's stand-in for
// "Log out everywhere", which revokes them in the session modes.
func (s *Server) revokeMyGrants(w http.ResponseWriter, r *http.Request) {
	if !s.oauthReady(w, r) {
		return
	}
	if r.URL.Query().Get("user") != "me" {
		writeErrDetails(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "user must be me",
			&model.SettingsErrorDetails{Fields: []model.SettingsFieldError{{Path: "user", Message: "must be me"}}})
		return
	}
	p := principalFrom(r.Context())
	if err := s.revokeUserGrants(w, r, p.User); err != nil {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errAnswered says the helper already wrote the answer.
var errAnswered = errors.New("answered")

// revokeUserGrants revokes every grant of user ("" = every user's), audited fail-closed as one
// record whose target names the user and "*". It writes the 503 or 500 itself and returns
// errAnswered then. With no grant to revoke it does nothing.
func (s *Server) revokeUserGrants(w http.ResponseWriter, r *http.Request, user string) error {
	if s.d.Credentials == nil {
		return nil
	}
	if len(s.d.Credentials.Grants(user)) == 0 {
		return nil
	}
	rec := s.auditRecord(r, auditGrantRevoke, audit.Target{Kind: "grant", Namespace: user, Name: "*"})
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return errAnswered
	}
	var err error
	if user == "" {
		_, err = s.d.Credentials.RevokeAllGrants()
	} else {
		_, err = s.d.Credentials.RevokeGrants(user)
	}
	pending.Outcome(err)
	if err != nil {
		slog.Error("grant revoke: credentials.yaml could not be written; the grants are revoked until dash restarts", "error", err, "requestId", requestIDFrom(r.Context()))
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the grants are revoked until dash restarts: credentials.yaml could not be written")
		return errAnswered
	}
	return nil
}
