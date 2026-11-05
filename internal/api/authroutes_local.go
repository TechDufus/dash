package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/session"
)

// The auth events of the local mode (contract §1.3): best-effort audit events, and the failed
// login as a denial, rate-limited per peer and reason.
const (
	auditLogin       = "auth.login"
	auditLoginFailed = "auth.login.failed"
	auditLogout      = "auth.logout"
	auditSetup       = "auth.setup"
)

// maxAuditName caps the attempted user name a failed-login record carries: it comes from the
// client.
const maxAuditName = 128

// The logout scopes of POST /auth/logout.
const (
	logoutThis = "this"
	logoutUser = "user"
	logoutAll  = "all"
)

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupBody struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

type logoutBody struct {
	Scope string `json:"scope"`
}

// login answers POST /login {username, password} (contract §4.4 Login): 204 and the session
// cookie, or one generic 401 unauthenticated for an unknown name and a wrong password alike (the
// same status, body, headers and hashing work), or 429 rate_limited with Retry-After when the
// throttle refuses the peer or the name (nothing is hashed or counted) or no hash slot is free.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	l := s.d.Local
	if l == nil || s.d.Sessions == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	var b loginBody
	if !s.decodeStrict(w, r, &b) {
		return
	}
	peer := peerOf(r)
	if wait, ok := l.Throttle.Allow(peer, b.Username); !ok {
		s.loginDenial(r, obs.AuthLoginThrottled, auditLoginFailed, b.Username)
		tooManyAttempts(w, wait)
		return
	}
	match, err := s.checkPassword(l, b.Username, b.Password)
	if errors.Is(err, localauth.ErrBusy) {
		tooManyAttempts(w, time.Second)
		return
	}
	if !match {
		l.Throttle.Fail(peer, b.Username)
		s.loginDenial(r, obs.AuthLoginFailed, auditLoginFailed, b.Username)
		writeErr(w, http.StatusUnauthorized, model.ErrCodeUnauthenticated, "invalid credentials")
		return
	}
	l.Throttle.Succeed(peer, b.Username)
	s.signIn(w, r, l.Account.User, auditLogin)
}

// checkPassword verifies password for user with one hash slot and the cost of one verify,
// whether or not user is the account: an unknown name, a password outside the length bounds and
// an account without a hash all run the dummy verify. It reports ErrBusy without a free slot.
func (s *Server) checkPassword(l *localauth.Local, user, password string) (bool, error) {
	phc, ok := l.Account.Hash()
	known := ok && l.Account.User != "" && subtle.ConstantTimeCompare([]byte(user), []byte(l.Account.User)) == 1
	if !known || localauth.ValidPassword(password) != nil {
		return false, l.Hasher.VerifyDummy(password)
	}
	match, err := l.Hasher.Verify(phc, password)
	switch {
	case errors.Is(err, localauth.ErrBusy):
		return false, err
	case err != nil:
		// The account's hash is unusable (cmd/dash checks it at startup). Never the hash itself.
		slog.Error("local admin password hash is not usable", "error", err.Error())
		return false, nil
	}
	return match, nil
}

// setup answers POST /setup {token, password} (contract §4.4 Setup token): with the token of this
// start it creates the administrator, consumes the token, bumps the account's epoch and signs it
// in, and answers 200 with SetupResult: the account's name, which only the token holder learns
// (no unauthenticated answer names it). 404 once an account exists (stored or from the
// environment); 401 invalid_token for a wrong, used or expired token, counted against the peer;
// 400 for a password outside 12..256 bytes; 429 when the throttle refuses the peer or no hash
// slot is free. The password is hashed before the token is consumed, so a busy hasher never
// burns the one token of this start.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	l := s.d.Local
	if l == nil || l.Setup == nil || s.d.Sessions == nil || l.Account.Store == nil || l.Account.Exists() {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	var b setupBody
	if !s.decodeStrict(w, r, &b) {
		return
	}
	peer := peerOf(r)
	if wait, ok := l.Throttle.Allow(peer, ""); !ok {
		s.loginDenial(r, obs.AuthLoginThrottled, auditSetup, "")
		tooManyAttempts(w, wait)
		return
	}
	if localauth.ValidPassword(b.Password) != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput,
			"the password must be "+strconv.Itoa(localauth.MinPasswordBytes)+" to "+strconv.Itoa(localauth.MaxPasswordBytes)+" bytes")
		return
	}
	phc, err := l.Hasher.Hash(b.Password)
	if errors.Is(err, localauth.ErrBusy) {
		tooManyAttempts(w, time.Second)
		return
	}
	if err != nil {
		slog.Error("setup: hashing the password failed", "error", err.Error())
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "setup failed")
		return
	}
	if !l.Setup.Consume(b.Token) {
		l.Throttle.Fail(peer, "")
		s.loginDenial(r, obs.AuthLoginFailed, auditSetup, "")
		writeErr(w, http.StatusUnauthorized, model.ErrCodeInvalidToken, "invalid or expired setup token")
		return
	}
	user := l.Account.User
	if err := l.Account.Store.SetAdmin(credentials.Admin{User: user, Hash: phc, CreatedAt: s.now().UTC()}); err != nil {
		slog.Error("setup: storing the administrator failed", "error", err.Error())
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "setup failed")
		return
	}
	if err := l.Account.Store.BumpEpoch(user); err != nil {
		slog.Error("setup: ending the sessions of the account failed", "error", err.Error())
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "setup failed")
		return
	}
	l.Throttle.Succeed(peer, "")
	if s.startSession(w, r, user, auditSetup) {
		writeJSON(w, http.StatusOK, model.SetupResult{AdminUser: user})
	}
}

// signIn issues the session of user, audits action and answers 204 with the cookie.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, user, action string) {
	if s.startSession(w, r, user, action) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// startSession issues the session of user, audits action and sets the cookie; on failure it
// answers 500 itself and reports false.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user, action string) bool {
	c, err := s.d.Sessions.Issue(session.Identity{User: user}, session.Keep{})
	if err != nil {
		slog.Error("sign-in: issuing the session failed", "error", err.Error())
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "could not start a session")
		return false
	}
	rec := s.localRecord(r, action, auth.Principal{Kind: auth.KindSession, User: user})
	rec.Target = audit.Target{Kind: "user", Name: user}
	s.d.Audit.Event(rec)
	s.syncTokens(user, nil, nil) // a local account has no groups or role claim
	http.SetCookie(w, c)
	return true
}

// logout answers POST /auth/logout {scope} (contract §4.4 Sessions): every scope clears the
// cookie; "this" (the default) and "user" bump the caller's epoch, so a copy of the cookie dies
// too; "all" is for an administrator and bumps the global epoch, which ends every session.
// "user" also revokes the caller's OAuth grants and "all" every grant (contract D23); that
// revocation is audited before it happens, so with grants to revoke an audit outage is 503
// audit_unavailable and nothing changes. Personal access tokens are never revoked here.
// Without a session it only clears the cookie.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.d.Sessions == nil || s.d.Credentials == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		return
	}
	var b logoutBody
	if r.ContentLength != 0 && !s.decodeStrict(w, r, &b) {
		return
	}
	scope := b.Scope
	if scope == "" {
		scope = logoutThis
	}
	if scope != logoutThis && scope != logoutUser && scope != logoutAll {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, `scope must be "this", "user" or "all"`)
		return
	}
	// /auth/logout is outside /api, where authn may not have run: read the session here.
	p := principalFrom(r.Context())
	if p.Kind == "" {
		p, _ = s.d.Auth.Authenticate(r)
	}
	signedIn := p.Kind == auth.KindSession && p.User != ""
	if scope == logoutAll && (!signedIn || !p.Role.AtLeast(auth.RoleAdmin)) {
		s.deny(r, obs.AuthForbidden)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "forbidden")
		return
	}
	if signedIn && scope != logoutThis {
		// The audit record names the caller, whom authn did not store on this path.
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		grantsOf := p.User
		if scope == logoutAll {
			grantsOf = "" // every user's
		}
		if s.revokeUserGrants(w, r, grantsOf) != nil {
			return
		}
	}
	http.SetCookie(w, s.d.Sessions.Clear())
	if !signedIn {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var err error
	if scope == logoutAll {
		err = s.d.Credentials.BumpGlobal()
	} else {
		err = s.d.Credentials.BumpEpoch(p.User)
	}
	rec := s.localRecord(r, auditLogout, p)
	rec.Target = audit.Target{Kind: "sessions", Name: scope}
	if err != nil {
		// The cookie is cleared, but a copy of it would still work: say so.
		slog.Error("logout: ending the sessions failed", "error", err.Error())
		rec.Result, rec.Error = audit.ResultError, "ending the sessions failed"
		s.d.Audit.Event(rec)
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "logout failed; the sessions were not ended")
		return
	}
	s.d.Audit.Event(rec)
	w.WriteHeader(http.StatusNoContent)
}

// loginDenial counts a refused login or setup attempt and records it as a denial: at most one
// record per peer and reason a minute, so unauthenticated attempts cannot flood the audit log.
// The attempted name goes in the target, never in the actor, which the denial limit would key on.
func (s *Server) loginDenial(r *http.Request, reason obs.AuthReason, action, attempted string) {
	s.d.Metrics.AuthFailure(reason)
	rec := s.localRecord(r, action, auth.Principal{})
	if attempted != "" {
		rec.Target = audit.Target{Kind: "user", Name: s.clipName(attempted)}
	}
	s.d.Audit.Denial(peerOf(r), reason.String(), rec)
}

// localRecord is the audit record of a local-auth route for the principal p (the zero principal
// before a sign-in).
func (s *Server) localRecord(r *http.Request, action string, p auth.Principal) audit.Record {
	mode := s.authMode(p)
	return audit.Record{
		Action: action, Peer: peerOf(r), AuthMode: mode, RequestID: requestIDFrom(r.Context()),
		Actor: audit.Actor{Type: mode, User: p.User, Surface: "ui"},
	}
}

// clipName bounds a client-supplied name to at most maxAuditName bytes of valid UTF-8, or up to
// one redaction marker past it, without leaving part of a secret that straddles the cut
// (secret.Registry.Cut).
func (s *Server) clipName(name string) string {
	name = strings.ToValidUTF8(name, "\uFFFD")
	if len(name) <= maxAuditName {
		return name
	}
	cut := maxAuditName
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	out, _ := s.d.Secrets.Cut(name, cut)
	return out
}

// tooManyAttempts answers 429 rate_limited with Retry-After in whole seconds, at least 1.
func tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	secs := max(1, int(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many attempts; try again later")
}
