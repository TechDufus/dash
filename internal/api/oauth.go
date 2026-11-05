package api

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/session"
)

// The audit actions of the OAuth grants.
const (
	auditGrantCreate = "oauth.grant.create"
	auditGrantRevoke = "oauth.grant.revoke"
)

// The /oauth/token and /oauth/revoke limit (contract G7-R3): per grant for a refresh, else per
// known client_id and peer, so one client cannot starve another behind the same ingress.
const (
	oauthTokenLimit  = 60
	oauthTokenWindow = time.Minute
	maxOAuthForm     = 16 << 10
)

// oauthRoutes are the authorization server's endpoints (design M.6, contract G7-R3), outside
// /api: the metadata documents, /oauth/authorize, /oauth/token and /oauth/revoke. They are
// Infra rows, documented in docs/api/README.md rather than the OpenAPI document. Without the
// server (auth.oauth.enabled off, auth.mode=none, no writable data dir) each answers a JSON
// 404, as the catch-alls do. Token and revoke take no ambient credential, so CSRF does not
// apply to them (csrf).
func (s *Server) oauthRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/.well-known/oauth-authorization-server", Infra: true, Handler: s.oauthMetadata},
		{Method: "GET", Path: "/.well-known/oauth-protected-resource/{audience}", Infra: true, Handler: s.oauthResourceMetadata},
		{Method: "GET", Path: "/oauth/authorize", Infra: true, Handler: s.oauthAuthorize},
		{Method: "POST", Path: "/oauth/token", Infra: true, Handler: s.oauthToken},
		{Method: "POST", Path: "/oauth/revoke", Infra: true, Handler: s.oauthRevoke},
		{Path: "/oauth/", Infra: true, Handler: notFound},
		{Method: "GET", Path: "/consent", Infra: true, Handler: s.consentPage},
		{Method: "GET", Path: "/consent/", Infra: true, Handler: s.consentPage},
	}
}

// oauthFormEndpoint reports whether p is one of the form endpoints a client calls without a
// session: no browser attaches a credential they read, so the CSRF check has nothing to guard.
func oauthFormEndpoint(p string) bool { return p == "/oauth/token" || p == "/oauth/revoke" }

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
}

// oauthCORSPath reports whether a listed origin may read the answer at p whoever asks (design
// L.7): the metadata documents and the token and revoke endpoints, which a browser-based client
// calls without a session. Never /oauth/authorize, which is a navigation.
func oauthCORSPath(p string) bool {
	return strings.HasPrefix(p, "/.well-known/oauth-") || p == "/oauth/token" || p == "/oauth/revoke"
}

// actionPlugins are the plugin instances with mounted actions: their actions:<id> scopes are the
// ones the metadata lists and a client may ask for.
func (s *Server) actionPlugins() []string {
	if s.d.Plugins == nil {
		return nil
	}
	var ids []string
	for _, pi := range s.d.Plugins.Infos() {
		if len(pi.Actions) > 0 {
			ids = append(ids, pi.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (s *Server) oauthMetadata(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.d.OAuth.Metadata(s.actionPlugins()))
}

func (s *Server) oauthResourceMetadata(w http.ResponseWriter, r *http.Request) {
	aud := r.PathValue("audience")
	if s.d.OAuth == nil || !s.d.OAuth.Serves(aud) {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.d.OAuth.ResourceMetadata(aud, s.actionPlugins()))
}

// oauthAuthorize answers /oauth/authorize (contract G7-R3 fix A2). With server.directURL set it
// is the JSON 404 except on the public URL's host, behind the identity proxy. The request is
// validated first: when the client or its redirect URI cannot be verified it is answered here
// (400); any other error goes back to the client. A valid request of a signed-in person goes on
// to their consent page; an anonymous caller goes to sign in, with next set to this same
// request, and dash keeps nothing for them (400 when the request is too long to be a next).
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil || !s.oauthPersonHost(r) {
		notFound(w, r)
		return
	}
	caller := s.oauthCaller(w, r)
	res, err := s.d.OAuth.Authorize(r.Context(), r.URL.Query(), s.actionPlugins(), caller)
	var oe *oauth.Error
	switch {
	case errors.As(err, &oe):
		writeJSON(w, http.StatusBadRequest, oe)
	case errors.Is(err, oauth.ErrBusy):
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusServiceUnavailable, &oauth.Error{Code: "temporarily_unavailable", Description: err.Error()})
	case err != nil:
		slog.Error("oauth authorize failed", "error", err, "requestId", requestIDFrom(r.Context()))
		writeJSON(w, http.StatusInternalServerError, &oauth.Error{Code: "server_error"})
	case res.ErrorRedirect != "":
		http.Redirect(w, r, res.ErrorRedirect, http.StatusFound)
	case res.SignIn:
		next := r.URL.RequestURI()
		if !session.ValidNext(next) {
			writeJSON(w, http.StatusBadRequest, &oauth.Error{Code: "invalid_request",
				Description: "the authorization request is too long to carry through sign-in: its path and query must be at most 1024 bytes"})
			return
		}
		signIn := "/login?next="
		if s.d.OIDC != nil {
			signIn = "/auth/oidc/start?next="
		}
		http.Redirect(w, r, signIn+url.QueryEscape(next), http.StatusFound)
	default:
		http.Redirect(w, r, res.ConsentURL, http.StatusFound)
	}
}

// oauthPersonHost reports whether r may reach the pages a person signs in for: /oauth/authorize
// and /consent. With server.directURL set, only on the public URL's host: the direct host
// bypasses the identity proxy, which must sign the person in.
func (s *Server) oauthPersonHost(r *http.Request) bool {
	return s.d.Cfg.DirectURL == "" || s.d.Cfg.PublicHost(r.Host)
}

// consentPage serves the SPA's consent page, except on the direct host (oauthPersonHost).
func (s *Server) consentPage(w http.ResponseWriter, r *http.Request) {
	if !s.oauthPersonHost(r) {
		notFound(w, r)
		return
	}
	s.serveStatic(w, r)
}

// oauthCaller is who sends an authorization request: the signed-in person (a session, the
// identity proxy's header; never a bearer token), or an anonymous caller. Only a signed-in
// person makes dash fetch a client ID metadata document, or, behind the identity proxy, any
// caller on the public URL's host. A renewed or cleared session cookie is set.
func (s *Server) oauthCaller(w http.ResponseWriter, r *http.Request) oauth.Caller {
	var c oauth.Caller
	if _, ok := auth.Bearer(r); !ok {
		p, cookie, fail := s.authenticate(r)
		if cookie != nil {
			http.SetCookie(w, cookie)
		}
		if fail == nil && p.Kind != auth.KindToken {
			c.User = p.User
		}
	}
	c.Fetch = c.User != "" || (s.authMode(auth.Principal{}) == string(auth.KindProxy) && s.d.Cfg.PublicHost(r.Host))
	return c
}

// oauthForm reads the form body of /oauth/token or /oauth/revoke (RFC 6749 3.2: POST,
// application/x-www-form-urlencoded, at most 16 KiB; query parameters are not read). It
// answers 400 itself and reports false on failure.
func (s *Server) oauthForm(w http.ResponseWriter, r *http.Request) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/x-www-form-urlencoded" {
		writeJSON(w, http.StatusBadRequest, &oauth.Error{Code: "invalid_request", Description: "the body must be application/x-www-form-urlencoded"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthForm)
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, &oauth.Error{Code: "invalid_request", Description: "the form could not be read"})
		return false
	}
	return true
}

// oauthLimitKey is the /oauth/token and /oauth/revoke limit's key (fix A1): the grant a refresh
// token names, else a hash of a client_id dash knows and the peer, else the peer alone. An
// unknown client_id is never a key of its own, so inventing them cannot grow the table, and a
// flood under a known client_id cannot starve the refresh of that client's real grant.
func (s *Server) oauthLimitKey(r *http.Request) string {
	subject := s.d.OAuth.LimitSubject(r.PostForm)
	switch {
	case subject.GrantID != "":
		return "grant|" + subject.GrantID
	case subject.ClientID != "":
		sum := sha256.Sum256([]byte(subject.ClientID))
		return "client|" + peerOf(r) + "|" + hex.EncodeToString(sum[:8])
	}
	return "peer|" + peerOf(r)
}

// grantActor is the audit actor of a change a client made to its own grant at the token or
// revoke endpoint: the grant's user through that client, on the grant's surface.
func grantActor(g credentials.Grant) audit.Actor {
	return audit.Actor{Type: string(auth.KindToken), User: g.User, GrantID: g.ID, ClientID: g.ClientID, Surface: g.Audience}
}

// grantTarget is the audit target of a grant: its user's, named by its ID.
func grantTarget(g credentials.Grant) audit.Target {
	return audit.Target{Kind: "grant", Namespace: g.User, Name: g.ID}
}

// oauthToken answers /oauth/token (RFC 6749 3.2): the code exchange with PKCE and the refresh
// with rotation. Past 60 requests a minute under one key (oauthLimitKey) it is 429. A grant
// revoked because a code or refresh token was used twice is recorded in the audit log.
func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notFound(w, r)
		return
	}
	if !s.oauthForm(w, r) {
		return
	}
	if ok, retry := s.oauthLimit.allow(s.oauthLimitKey(r), s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, &oauth.Error{Code: "temporarily_unavailable", Description: "too many token requests; try again later"})
		return
	}
	w.Header().Set("Pragma", "no-cache")
	res, ev, oe := s.d.OAuth.Token(r.PostForm)
	if ev != nil {
		rec := audit.Record{
			Action: auditGrantRevoke, Target: grantTarget(ev.Grant), Peer: peerOf(r), AuthMode: s.authMode(auth.Principal{}),
			RequestID: requestIDFrom(r.Context()), Actor: grantActor(ev.Grant), Reason: ev.Reason,
		}
		s.d.Audit.Event(rec)
		slog.Warn("oauth: a grant was revoked because a token was used twice", "grant", ev.Grant.ID, "user", ev.Grant.User, "reason", ev.Reason)
	}
	if oe != nil {
		status := http.StatusBadRequest
		if oe.Code == "server_error" {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, oe)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// oauthRevoke answers /oauth/revoke (RFC 7009): a refresh token or an access token revokes its
// grant, and every token of it, at once. A token dash does not know, or another client's, is
// 200 and changes nothing. The revocation is audited before it happens: an audit outage is 503
// and nothing changes.
func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if s.d.OAuth == nil {
		notFound(w, r)
		return
	}
	if !s.oauthForm(w, r) {
		return
	}
	if ok, retry := s.oauthLimit.allow(s.oauthLimitKey(r), s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeJSON(w, http.StatusTooManyRequests, &oauth.Error{Code: "temporarily_unavailable", Description: "too many requests; try again later"})
		return
	}
	g, ok := s.d.OAuth.RevocationTarget(r.PostForm)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	rec := audit.Record{
		Action: auditGrantRevoke, Target: grantTarget(g), Peer: peerOf(r), AuthMode: s.authMode(auth.Principal{}),
		RequestID: requestIDFrom(r.Context()), Actor: grantActor(g),
	}
	pending, err := s.d.Audit.Intent(rec)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, &oauth.Error{Code: "temporarily_unavailable", Description: "audit log unavailable"})
		return
	}
	_, _, err = s.d.Credentials.RevokeGrant(g.ID)
	pending.Outcome(err)
	if err != nil {
		slog.Error("oauth revoke: credentials.yaml could not be written; the grant is revoked until dash restarts", "error", err, "requestId", requestIDFrom(r.Context()))
	}
	w.WriteHeader(http.StatusOK)
}
