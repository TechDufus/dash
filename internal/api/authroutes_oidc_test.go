package api

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oidcauth"
	"github.com/TechDufus/dash/internal/oidcauth/oidctest"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

const (
	oidcClientID = "dash-client"
	oidcSecret   = "oidc-client-secret-canary-7f3a"
	oidcRedirect = testOrigin + oidcauth.CallbackPath
)

// oidcOpts shape one test's OIDC configuration; the zero value admits alice only, reads
// preferred_username and groups, has no role claim and no admins.
type oidcOpts struct {
	allowedUsers, allowedGroups []string
	roles                       auth.RoleMap
	userClaim, roleClaim        string
	secure                      bool
	issuer                      string       // "" = the mock IdP
	client                      *http.Client // nil = the provider's default
	secret                      string       // "" = the secret the IdP expects
}

// oidcEnv is an OIDC-mode server (session authenticator, audit log, the two OIDC routes and the
// API) in front of a mock IdP, on one settable clock.
type oidcEnv struct {
	t        *testing.T
	idp      *oidctest.IdP
	p        *oidcauth.Provider
	sh       *sessionHarness
	clock    *auditClock
	auditDir string
	h        http.Handler
}

func newOIDCEnv(t *testing.T, o oidcOpts) *oidcEnv {
	t.Helper()
	if o.allowedUsers == nil && o.allowedGroups == nil {
		o.allowedUsers = []string{"alice"}
	}
	e := &oidcEnv{t: t, clock: newAuditClock(), auditDir: t.TempDir()}
	e.idp = oidctest.New(t, oidcClientID, oidcSecret, oidcRedirect, e.clock.now)
	opt, sh := withSessions(t, auth.ModeOIDC, o.roles, false, e.clock.now)
	e.sh = sh
	al := openAudit(t, e.auditDir, e.clock.now)
	stateCodec, err := session.NewCodec(bytes.Repeat([]byte{0x24}, session.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e.p, err = oidcauth.New(oidcauth.Config{
		Issuer: cmp.Or(o.issuer, e.idp.Issuer()), ClientID: oidcClientID,
		ClientSecret: secret.New(cmp.Or(o.secret, oidcSecret), ""), Scopes: []string{"openid", "profile", "email"},
		UserClaim: cmp.Or(o.userClaim, "preferred_username"), GroupsClaim: "groups", RoleClaim: o.roleClaim,
		AllowedUsers: o.allowedUsers, AllowedGroups: o.allowedGroups, Roles: sh.Roles,
		RedirectURL: oidcRedirect, Secure: o.secure, HTTPClient: o.client, Now: e.clock.now,
	}, sh.Sessions, stateCodec)
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(func(d *Deps) {
		opt(d)
		d.Audit, d.OIDC = al, e.p
	})
	mux := http.NewServeMux()
	mux.Handle("GET /auth/oidc/start", srv.requestID(http.HandlerFunc(srv.oidcStart)))
	mux.Handle("GET /auth/callback", srv.requestID(http.HandlerFunc(srv.oidcCallback)))
	mux.Handle("/api/", srv.Handler())
	e.h = mux
	return e
}

// start runs /auth/oidc/start?next= and returns its answer.
func (e *oidcEnv) start(next string) *httptest.ResponseRecorder {
	e.t.Helper()
	return req{path: "/auth/oidc/start?next=" + url.QueryEscape(next)}.do(e.h)
}

// begin is a successful start: the IdP URL and the state cookie.
func (e *oidcEnv) begin(next string) (authURL string, state *http.Cookie) {
	e.t.Helper()
	w := e.start(next)
	if w.Code != http.StatusFound {
		e.t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	state = respCookie(w, e.p.ClearState().Name)
	if state == nil || state.Value == "" {
		e.t.Fatalf("start set no state cookie: %v", w.Header().Values("Set-Cookie"))
	}
	return w.Header().Get("Location"), state
}

// callback runs /auth/callback with the query and the cookies a browser would send.
func (e *oidcEnv) callback(code, state string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	q := url.Values{}
	if code != "" {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	return req{path: oidcauth.CallbackPath + "?" + q.Encode(), header: map[string]string{"Cookie": cookieLine(cookies...)}}.do(e.h)
}

// login is the whole flow for grant with next "/": start, the IdP, the callback.
func (e *oidcEnv) login(grant oidctest.Grant) *httptest.ResponseRecorder {
	e.t.Helper()
	authURL, st := e.begin("/")
	code, state := e.idp.Authorize(e.t, authURL, grant)
	return e.callback(code, state, st)
}

// session is the session cookie a callback set; it fails the test when there is none.
func (e *oidcEnv) session(w *httptest.ResponseRecorder) *http.Cookie {
	e.t.Helper()
	c := respCookie(w, session.CookiePlain)
	if w.Code != http.StatusFound || c == nil || c.Value == "" || c.MaxAge < 0 {
		e.t.Fatalf("callback = %d Location %q, Set-Cookie %v: want a session", w.Code, w.Header().Get("Location"), w.Header().Values("Set-Cookie"))
	}
	return c
}

// me is GET /api/v1/me with the session cookie.
func (e *oidcEnv) me(c *http.Cookie) model.Me {
	e.t.Helper()
	w := req{path: "/api/v1/me", header: map[string]string{"Cookie": cookieLine(c)}}.do(e.h)
	if w.Code != http.StatusOK {
		e.t.Fatalf("me = %d %s", w.Code, w.Body)
	}
	var me model.Me
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		e.t.Fatal(err)
	}
	return me
}

func claims(kv ...any) oidctest.Grant {
	g := oidctest.Grant{Claims: map[string]any{}}
	for i := 0; i+1 < len(kv); i += 2 {
		g.Claims[kv[i].(string)] = kv[i+1]
	}
	return g
}

func respCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	var found *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			found = c
		}
	}
	return found
}

// cookieLine is the Cookie header value of cookies (the local tests' cookieHeader builds the map).
func cookieLine(cookies ...*http.Cookie) string {
	var parts []string
	for _, c := range cookies {
		if c != nil {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// wantRefused asserts a refused callback: a 302 to the login page with reason, the state cookie
// cleared, and no session cookie.
func (e *oidcEnv) wantRefused(w *httptest.ResponseRecorder, reason string) {
	e.t.Helper()
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login?error="+reason {
		e.t.Fatalf("callback = %d Location %q, want 302 /login?error=%s", w.Code, w.Header().Get("Location"), reason)
	}
	if c := respCookie(w, session.CookiePlain); c != nil {
		e.t.Errorf("a refused callback set the session cookie %v", c)
	}
	wantStateCleared(e.t, w, e.p.ClearState().Name)
}

func wantStateCleared(t *testing.T, w *httptest.ResponseRecorder, name string) {
	t.Helper()
	if c := respCookie(w, name); c == nil || c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("the state cookie was not cleared: %v", w.Header().Values("Set-Cookie"))
	}
}

func TestOIDCLoginHappyPath(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{roles: auth.RoleMap{AdminUsers: []string{"alice"}}})
	authURL, st := e.begin("/plugins/alerts?tab=silences")
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	w := e.callback(code, state, st)
	c := e.session(w)
	if got := w.Header().Get("Location"); got != "/plugins/alerts?tab=silences" {
		t.Errorf("Location = %q, want the next of the start", got)
	}
	wantStateCleared(t, w, oidcauth.StateCookiePlain)
	if c.SameSite != http.SameSiteLaxMode || !c.HttpOnly {
		t.Errorf("session cookie %v", c)
	}
	if me := e.me(c); me.User != "alice" || me.Role != string(auth.RoleAdmin) {
		t.Errorf("me = %s/%s, want alice/admin", me.User, me.Role)
	}
	if n := e.idp.Hits(oidctest.PathUserInfo); n != 0 {
		t.Errorf("UserInfo called %d times: only ID-token claims may be used", n)
	}
	recs := auditRecords(t, e.auditDir)
	i := slices.IndexFunc(recs, func(r audit.Record) bool { return r.Action == "auth.login" })
	if i < 0 || recs[i].Actor.User != "alice" || recs[i].AuthMode != auth.ModeOIDC || recs[i].Kind != audit.KindEvent {
		t.Errorf("audit = %+v, want an auth.login event for alice", recs)
	}
	// The flow left nothing a later login could reuse in the browser.
	if w := e.callback(code, state, st); w.Header().Get("Location") != "/login?error=oidc_failed" {
		t.Errorf("a replayed callback = %q", w.Header().Get("Location"))
	}
}

func TestOIDCRoleMappingFromGroupsAndRoleClaim(t *testing.T) {
	roles := auth.RoleMap{AdminUsers: []string{"root"}, AdminGroups: []string{"ops"}, AdminRoles: []string{"admin"}}
	for _, tc := range []struct {
		name  string
		grant oidctest.Grant
		want  auth.Role
	}{
		{"admin user", claims("preferred_username", "root"), auth.RoleAdmin},
		{"admin group as a list", claims("preferred_username", "carol", "groups", []string{"dev", "ops"}), auth.RoleAdmin},
		{"admin group as a comma-separated string", claims("preferred_username", "carol", "groups", "dev, ops"), auth.RoleAdmin},
		{"admin role-claim value", claims("preferred_username", "carol", "roles", []string{"reader", "admin"}), auth.RoleAdmin},
		{"admin role-claim value as a string", claims("preferred_username", "carol", "roles", "admin"), auth.RoleAdmin},
		{"a group named like the role does not count", claims("preferred_username", "carol", "groups", []string{"admin"}), auth.RoleViewer},
		{"a role value named like the group does not count", claims("preferred_username", "carol", "roles", []string{"ops"}), auth.RoleViewer},
		{"nothing listed", claims("preferred_username", "carol", "groups", []string{"dev"}, "roles", []string{"reader"}), auth.RoleViewer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"*"}, roles: roles, roleClaim: "roles"})
			if me := e.me(e.session(e.login(tc.grant))); me.Role != string(tc.want) {
				t.Errorf("role = %s, want %s", me.Role, tc.want)
			}
		})
	}
}

func TestOIDCDeniesUsersOutsideTheAllowLists(t *testing.T) {
	for _, tc := range []struct {
		name          string
		users, groups []string
		grant         oidctest.Grant
		admitted      bool
	}{
		{"listed user", []string{"alice"}, []string{"ops"}, claims("preferred_username", "alice"), true},
		{"listed group", []string{"alice"}, []string{"ops"}, claims("preferred_username", "carol", "groups", []string{"dev", "ops"}), true},
		{"neither", []string{"alice"}, []string{"ops"}, claims("preferred_username", "bob", "groups", []string{"dev"}), false},
		{"user match is exact", []string{"alice"}, nil, claims("preferred_username", "Alice"), false},
		{"group match is exact", nil, []string{"ops"}, claims("preferred_username", "bob", "groups", []string{"Ops", "ops-team"}), false},
		{"a user named like a group", nil, []string{"ops"}, claims("preferred_username", "ops"), false},
		{"wildcard admits every account", []string{"*"}, nil, claims("preferred_username", "anyone"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{allowedUsers: tc.users, allowedGroups: tc.groups})
			w := e.login(tc.grant)
			if tc.admitted {
				e.session(w)
				return
			}
			e.wantRefused(w, "not_allowed")
			user := tc.grant.Claims["preferred_username"].(string)
			recs := auditRecords(t, e.auditDir)
			if len(recs) != 1 {
				t.Fatalf("audit = %+v, want one denial", recs)
			}
			r := recs[0]
			if r.Kind != audit.KindDenial || r.Action != "auth.login.failed" || r.Reason != "not_allowed" ||
				r.Target.Name != user || r.Actor.User != "" || r.Peer != "127.0.0.1" {
				t.Errorf("denial = %+v", r)
			}
		})
	}
}

func TestOIDCRejectsAWrongState(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{})
	authURL, st := e.begin("/")
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	for _, bad := range []string{"", "x" + state, state[:len(state)-1], strings.ToLower(state) + "!"} {
		e.wantRefused(e.callback(code, bad, st), "oidc_failed")
	}
	// Another login's cookie does not fit this state either.
	_, other := e.begin("/")
	e.wantRefused(e.callback(code, state, other), "oidc_failed")
	if n := e.idp.Hits(oidctest.PathToken); n != 0 {
		t.Errorf("the code was exchanged %d times for a wrong state", n)
	}
}

func TestOIDCRejectsAMissingStateCookie(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{})
	authURL, st := e.begin("/")
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	e.wantRefused(e.callback(code, state), "oidc_failed")
	e.wantRefused(e.callback(code, state, &http.Cookie{Name: st.Name, Value: "v1.forged"}), "oidc_failed")
	e.wantRefused(e.callback(code, state, &http.Cookie{Name: st.Name, Value: st.Value[:len(st.Value)-2] + "AA"}), "oidc_failed")
	// The secure name is not read on plain http.
	e.wantRefused(e.callback(code, state, &http.Cookie{Name: oidcauth.StateCookieSecure, Value: st.Value}), "oidc_failed")
	if n := e.idp.Hits(oidctest.PathToken); n != 0 {
		t.Errorf("the code was exchanged %d times without the state cookie", n)
	}
}

func TestOIDCRejectsAWrongNonce(t *testing.T) {
	for name, nonce := range map[string]any{"another nonce": "nonce-of-another-login", "no nonce": oidctest.Delete, "empty": ""} {
		t.Run(name, func(t *testing.T) {
			logs := captureSlog(t)
			e := newOIDCEnv(t, oidcOpts{})
			e.wantRefused(e.login(claims("preferred_username", "alice", "nonce", nonce)), "oidc_failed")
			if !strings.Contains(logs.String(), `"stage":"nonce_mismatch"`) {
				t.Errorf("log = %s", logs)
			}
		})
	}
}

func TestOIDCRejectsAnExpiredOrWrongAudienceToken(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant func(now time.Time) oidctest.Grant
	}{
		{"expired", func(now time.Time) oidctest.Grant {
			return claims("preferred_username", "alice", "exp", now.Add(-time.Second).Unix())
		}},
		{"wrong audience", func(time.Time) oidctest.Grant {
			return claims("preferred_username", "alice", "aud", "another-client")
		}},
		{"wrong issuer", func(time.Time) oidctest.Grant {
			return claims("preferred_username", "alice", "iss", "https://idp.invalid")
		}},
		{"signed by a key outside the JWKS", func(time.Time) oidctest.Grant {
			g := claims("preferred_username", "alice")
			g.ForeignKey = true
			return g
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureSlog(t)
			e := newOIDCEnv(t, oidcOpts{})
			e.wantRefused(e.login(tc.grant(e.clock.now())), "oidc_failed")
			if !strings.Contains(logs.String(), `"stage":"verify"`) {
				t.Errorf("log = %s", logs)
			}
		})
	}
}

func TestOIDCUsesPKCEChallengeS256(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{})
	authURL, st := e.begin("/")
	q := mustQuery(t, authURL)
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("authorization request %v", q)
	}
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	e.session(e.callback(code, state, st))
	verifier := e.idp.TokenRequest().Get("code_verifier")
	sum := sha256.Sum256([]byte(verifier))
	if verifier == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Errorf("code_verifier %q does not hash to the challenge %q", verifier, q.Get("code_challenge"))
	}
	if strings.Contains(authURL, verifier) || strings.Contains(st.Value, verifier) {
		t.Error("the verifier left the server in the clear")
	}
	// Each login has its own verifier.
	other, _ := e.begin("/")
	if mustQuery(t, other).Get("code_challenge") == q.Get("code_challenge") {
		t.Error("two logins share a PKCE challenge")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestOIDCStateCookieIsSingleUse(t *testing.T) {
	logs := captureSlog(t)
	e := newOIDCEnv(t, oidcOpts{})
	authURL, st := e.begin("/")
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	e.session(e.callback(code, state, st))
	// The browser dropped the cookie; a stolen copy with a fresh code for the same state is refused
	// before any exchange.
	fresh, _ := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	exchanges := e.idp.Hits(oidctest.PathToken)
	e.wantRefused(e.callback(fresh, state, st), "oidc_failed")
	if e.idp.Hits(oidctest.PathToken) != exchanges {
		t.Error("a replayed state cookie reached the token endpoint")
	}
	if !strings.Contains(logs.String(), `"stage":"state_reused"`) {
		t.Errorf("log = %s", logs)
	}
	// A refused callback uses the state up too.
	authURL, st = e.begin("/")
	_, state = e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	e.wantRefused(e.callback("", state, st), "oidc_failed")
	code, _ = e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	e.wantRefused(e.callback(code, state, st), "oidc_failed")
}

func TestOIDCStateCookieExpiresAfterTenMinutes(t *testing.T) {
	for _, tc := range []struct {
		after time.Duration
		ok    bool
	}{{oidcauth.StateTTL - time.Second, true}, {oidcauth.StateTTL, false}} {
		t.Run(tc.after.String(), func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{})
			authURL, st := e.begin("/")
			e.clock.advance(tc.after)
			code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
			w := e.callback(code, state, st)
			if tc.ok {
				e.session(w)
			} else {
				e.wantRefused(w, "oidc_failed")
			}
		})
	}
}

func TestOIDCMappingChangeAppliesNextRequest(t *testing.T) {
	t.Run("adminGroups", func(t *testing.T) {
		e := newOIDCEnv(t, oidcOpts{allowedGroups: []string{"ops"}})
		c := e.session(e.login(claims("preferred_username", "carol", "groups", []string{"ops"})))
		if me := e.me(c); me.Role != string(auth.RoleViewer) {
			t.Fatalf("role = %s before the change", me.Role)
		}
		e.sh.Roles.Store(auth.RoleMap{AdminGroups: []string{"ops"}})
		if me := e.me(c); me.Role != string(auth.RoleAdmin) {
			t.Errorf("role = %s after adding ops to adminGroups, want admin on the same cookie", me.Role)
		}
		e.sh.Roles.Store(auth.RoleMap{})
		if me := e.me(c); me.Role != string(auth.RoleViewer) {
			t.Errorf("role = %s after removing it", me.Role)
		}
	})
	t.Run("adminRoles", func(t *testing.T) {
		e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"carol"}, roleClaim: "roles", roles: auth.RoleMap{AdminRoles: []string{"admin"}}})
		c := e.session(e.login(claims("preferred_username", "carol", "roles", []string{"admin"})))
		if me := e.me(c); me.Role != string(auth.RoleAdmin) {
			t.Fatalf("role = %s before the change", me.Role)
		}
		e.sh.Roles.Store(auth.RoleMap{AdminRoles: []string{"superuser"}})
		if me := e.me(c); me.Role != string(auth.RoleViewer) {
			t.Errorf("role = %s after dropping admin from adminRoles, want viewer on the same cookie", me.Role)
		}
		e.sh.Roles.Store(auth.RoleMap{AdminRoles: []string{"admin"}})
		if me := e.me(c); me.Role != string(auth.RoleAdmin) {
			t.Errorf("role = %s after restoring it", me.Role)
		}
	})
}

func TestOIDCGroupChangeNeedsANewLogin(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"carol"}, roles: auth.RoleMap{AdminGroups: []string{"ops"}}})
	before := e.session(e.login(claims("preferred_username", "carol", "groups", []string{"dev"})))
	if me := e.me(before); me.Role != string(auth.RoleViewer) {
		t.Fatalf("role = %s", me.Role)
	}
	// The IdP adds carol to ops: the cookie she holds still says what the IdP said at login.
	if me := e.me(before); me.Role != string(auth.RoleViewer) {
		t.Errorf("role = %s on the old cookie", me.Role)
	}
	after := e.session(e.login(claims("preferred_username", "carol", "groups", []string{"dev", "ops"})))
	if me := e.me(after); me.Role != string(auth.RoleAdmin) {
		t.Errorf("role = %s after a new login", me.Role)
	}
	// A group that was not listed at login was not kept: listing it later needs a new login too.
	e.sh.Roles.Store(auth.RoleMap{AdminGroups: []string{"ops", "dev"}})
	if me := e.me(before); me.Role != string(auth.RoleViewer) {
		t.Errorf("role = %s: dev was not captured at login, so listing it must not apply to the old cookie", me.Role)
	}
	again := e.session(e.login(claims("preferred_username", "carol", "groups", []string{"dev"})))
	if me := e.me(again); me.Role != string(auth.RoleAdmin) {
		t.Errorf("role = %s after the next login", me.Role)
	}
}

func TestOIDCUnverifiedEmailIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		verified any
		ok       bool
	}{
		{"verified", true, true},
		{"unverified", false, false},
		{"missing", oidctest.Delete, false},
		{"a string is not a boolean", "true", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{userClaim: "email", allowedUsers: []string{"alice@example.com"}})
			w := e.login(claims("email", "alice@example.com", "email_verified", tc.verified))
			if !tc.ok {
				e.wantRefused(w, "oidc_failed")
				return
			}
			if me := e.me(e.session(w)); me.User != "alice@example.com" {
				t.Errorf("user = %q", me.User)
			}
		})
	}
	// Without the email claim, email_verified is not asked for.
	e := newOIDCEnv(t, oidcOpts{})
	e.session(e.login(claims("preferred_username", "alice", "email_verified", false)))
}

func TestOIDCUserClaimMustBeAValidName(t *testing.T) {
	for name, user := range map[string]any{
		"missing": oidctest.Delete, "empty": "", "not a string": 42, "a list": []string{"alice"},
		"over 128 bytes": strings.Repeat("a", 129), "control character": "ali\nce",
	} {
		t.Run(name, func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"*"}})
			e.wantRefused(e.login(claims("preferred_username", user)), "oidc_failed")
		})
	}
	e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"*"}})
	e.session(e.login(claims("preferred_username", strings.Repeat("a", 128))))
}

// countingTransport counts the requests that reach the network.
type countingTransport struct{ n atomic.Int64 }

func (c *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return http.DefaultTransport.RoundTrip(r)
}

func TestOIDCDiscoveryFailureIs503AndDashStillStarts(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	issuer := dead.URL
	dead.Close()
	ct := &countingTransport{}
	e := newOIDCEnv(t, oidcOpts{issuer: issuer, client: &http.Client{Transport: ct}})
	if n := ct.n.Load(); n != 0 {
		t.Fatalf("New made %d requests: construction must not dial", n)
	}
	if want := strings.TrimPrefix(issuer, "http://"); e.p.IssuerHost() != want {
		t.Errorf("IssuerHost = %q, want %q", e.p.IssuerHost(), want)
	}
	w := e.start("/")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	var body model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != model.ErrCodeUpstreamUnavailable ||
		body.Error != "identity provider unavailable" {
		t.Errorf("body = %s", w.Body)
	}
	if respCookie(w, oidcauth.StateCookiePlain) != nil {
		t.Error("a failed start set a state cookie")
	}
	if ct.n.Load() == 0 {
		t.Error("start did not try discovery")
	}

	// A failure is not cached: the IdP coming back is enough.
	e = newOIDCEnv(t, oidcOpts{})
	e.idp.SetDown(true)
	if w := e.start("/"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("start with the IdP down = %d", w.Code)
	}
	e.idp.SetDown(false)
	e.session(e.login(claims("preferred_username", "alice")))
	// A success is: discovery ran twice in all, not once per login.
	e.session(e.login(claims("preferred_username", "alice")))
	if n := e.idp.Hits(oidctest.PathDiscovery); n != 2 {
		t.Errorf("discovery ran %d times, want 2 (one failure, one success)", n)
	}
}

func TestOIDCNextIsValidated(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{})
	long := "/" + strings.Repeat("a", 1023)
	for _, bad := range []string{"//evil.example", "/\\evil.example", "https://evil.example/", "javascript:alert(1)", "relative", "/a\x00b", long + "a"} {
		w := e.start(bad)
		if w.Code != http.StatusBadRequest {
			t.Errorf("next %q = %d, want 400", bad, w.Code)
			continue
		}
		var body model.APIError
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != model.ErrCodeInvalidInput {
			t.Errorf("next %q body = %s", bad, w.Body)
		}
		if respCookie(w, oidcauth.StateCookiePlain) != nil {
			t.Errorf("next %q set a state cookie", bad)
		}
	}
	if n := e.idp.Hits(oidctest.PathDiscovery); n != 0 {
		t.Errorf("a bad next reached the IdP %d times", n)
	}
	for next, want := range map[string]string{"": "/", "/": "/", long: long, "/plugins/x?tab=1#top": "/plugins/x?tab=1#top"} {
		authURL, st := e.begin(next)
		code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
		w := e.callback(code, state, st)
		e.session(w)
		if got := w.Header().Get("Location"); got != want {
			t.Errorf("next %q: Location = %q, want %q", next, got, want)
		}
	}
}

func TestOIDCStateCookieSurvivesACrossSiteTopLevelRedirect(t *testing.T) {
	for _, tc := range []struct {
		secure bool
		name   string
	}{{true, oidcauth.StateCookieSecure}, {false, oidcauth.StateCookiePlain}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newOIDCEnv(t, oidcOpts{secure: tc.secure})
			w := e.start("/")
			c := respCookie(w, tc.name)
			if c == nil {
				t.Fatalf("no %s cookie: %v", tc.name, w.Header().Values("Set-Cookie"))
			}
			// Lax, never Strict: the IdP's redirect back is a cross-site top-level GET, which a
			// browser sends Lax cookies on and Strict ones not.
			raw := w.Header().Get("Set-Cookie")
			if c.SameSite != http.SameSiteLaxMode || strings.Contains(raw, "Strict") {
				t.Errorf("SameSite: %s", raw)
			}
			if !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.Secure != tc.secure || c.MaxAge != int(oidcauth.StateTTL/time.Second) {
				t.Errorf("attributes: %s", raw)
			}
			// The callback needs nothing a cross-site navigation lacks: no Origin, no Referer,
			// no CSRF header, only the cookie.
			code, state := e.idp.Authorize(t, w.Header().Get("Location"), claims("preferred_username", "alice"))
			cb := e.callback(code, state, c)
			e.session(cb)
			wantStateCleared(t, cb, tc.name)
			if cl := respCookie(cb, tc.name); cl.SameSite != http.SameSiteLaxMode || cl.Path != "/" || cl.Secure != tc.secure {
				t.Errorf("the clearing cookie does not match the one it clears: %v", cb.Header().Values("Set-Cookie"))
			}
		})
	}
}

func TestOIDCSessionCarriesOnlyListedGroupsAndRoles(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{
		allowedGroups: []string{"staff"}, roleClaim: "roles",
		roles: auth.RoleMap{AdminGroups: []string{"ops"}, AdminRoles: []string{"admin"}},
	})
	many := make([]string, 0, 300)
	for i := range 300 {
		many = append(many, fmt.Sprintf("unlisted-group-with-a-long-name-%03d", i))
	}
	groups := append([]string{"staff", "ops"}, many...)
	c := e.session(e.login(claims("preferred_username", "carol", "groups", groups, "roles", []string{"reader", "admin", "billing"})))
	var p session.Payload
	if err := sessiontest.Codec().Open(c.Value, &p); err != nil {
		t.Fatal(err)
	}
	slices.Sort(p.Groups)
	if p.User != "carol" || !slices.Equal(p.Groups, []string{"ops", "staff"}) || !slices.Equal(p.RoleClaim, []string{"admin"}) {
		t.Errorf("payload = %+v, want only ops/staff and admin", p)
	}
}

func TestOIDCRefusesALoginWhoseCookieWouldBeTooLarge(t *testing.T) {
	logs := captureSlog(t)
	listed := make([]string, 0, 150)
	for i := range 150 {
		listed = append(listed, fmt.Sprintf("listed-group-with-a-long-name-%03d", i))
	}
	e := newOIDCEnv(t, oidcOpts{allowedGroups: listed})
	e.wantRefused(e.login(claims("preferred_username", "carol", "groups", listed)), "oidc_failed")
	if !strings.Contains(logs.String(), `"stage":"session"`) || !strings.Contains(logs.String(), "3500 bytes") {
		t.Errorf("the refusal is not explained in the log: %s", logs)
	}
	// The same account with fewer listed groups fits.
	e.session(e.login(claims("preferred_username", "carol", "groups", listed[:3])))
}

// No secret, code, token or cookie value reaches a response, a log line or the audit log, even
// when the IdP echoes them in an error body.
func TestOIDCFailuresLeakNoSecret(t *testing.T) {
	logs := captureSlog(t)
	e := newOIDCEnv(t, oidcOpts{secret: "wrong-secret-canary-91c2"})
	e.idp.SetEchoOnTokenError(true)
	authURL, st := e.begin("/")
	code, state := e.idp.Authorize(t, authURL, claims("preferred_username", "alice"))
	w := e.callback(code, state, st)
	e.wantRefused(w, "oidc_failed")
	if !strings.Contains(logs.String(), `"stage":"exchange"`) || !strings.Contains(logs.String(), "invalid_client") {
		t.Errorf("log = %s", logs)
	}
	written := logs.String() + w.Body.String() + strings.Join(w.Header().Values("Location"), " ") + readAuditFile(t, e.auditDir)
	for _, s := range []string{"wrong-secret-canary-91c2", oidcSecret, code, st.Value} {
		if strings.Contains(written, s) {
			t.Errorf("%q leaked: %s", s, written)
		}
	}
}

func TestOIDCRoutesAre404OutsideOIDCMode(t *testing.T) {
	srv := newServer(nil)
	for _, h := range []http.HandlerFunc{srv.oidcStart, srv.oidcCallback} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, "/auth/oidc/start?next=/", nil))
		if w.Code != http.StatusNotFound || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("= %d %v", w.Code, w.Header())
		}
	}
}
