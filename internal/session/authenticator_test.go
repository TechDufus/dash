package session_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

type authFixture struct {
	clk   *clock
	store *credentials.Store
	m     *session.Manager
	roles *auth.RoleHolder
}

func newAuthFixture(t *testing.T, roles auth.RoleMap) *authFixture {
	t.Helper()
	f := &authFixture{clk: newClock(), store: memStore(t), roles: auth.NewRoleHolder(roles)}
	f.m = sessiontest.Manager(f.store, f.clk.now)
	return f
}

func (f *authFixture) auth(mode string, anonymous bool, warnings ...string) *session.Authenticator {
	return session.NewAuthenticator(session.AuthOptions{Mode: mode, Sessions: f.m, Roles: f.roles, Anonymous: anonymous, Warnings: warnings})
}

func wantFailure(t *testing.T, f *auth.Failure, code, reason string) {
	t.Helper()
	if f == nil {
		t.Fatalf("authenticated, want 401 %s/%s", code, reason)
	}
	if f.Status != http.StatusUnauthorized || f.Code != code || f.Reason != reason {
		t.Fatalf("failure %+v, want 401 %s/%s", *f, code, reason)
	}
}

func wantClearing(t *testing.T, m *session.Manager, c *http.Cookie) {
	t.Helper()
	if c == nil || c.Name != m.CookieName() || c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("cookie %v, want the clearing cookie", c)
	}
}

func TestSessionPayloadHoldsNoRole(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	c, err := f.m.Issue(session.Identity{User: "alice", Groups: []string{"ops"}, RoleClaim: []string{"admin"}},
		session.Keep{Groups: []string{"ops"}, RoleClaim: []string{"admin"}})
	must(t, err)

	var raw map[string]any
	must(t, sessiontest.Codec().Open(c.Value, &raw))
	var keys []string
	for k := range raw {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if want := []string{"ep", "exp", "g", "ge", "iat", "rc", "u"}; !slices.Equal(keys, want) {
		t.Fatalf("payload keys %q, want exactly %q (no role)", keys, want)
	}

	a := f.auth(auth.ModeOIDC, false)
	role := func() auth.Role {
		t.Helper()
		p, _, fail := a.AuthenticateCookie(withCookies(c))
		if fail != nil {
			t.Fatalf("refused: %+v", *fail)
		}
		return p.Role
	}
	if r := role(); r != auth.RoleAdmin {
		t.Fatalf("role %q with alice in adminUsers, want admin", r)
	}
	// The same cookie, the next request, a new mapping: the role follows the mapping.
	f.roles.Store(auth.RoleMap{})
	if r := role(); r != auth.RoleViewer {
		t.Fatalf("role %q after emptying adminUsers, want viewer", r)
	}
	f.roles.Store(auth.RoleMap{AdminGroups: []string{"ops"}})
	if r := role(); r != auth.RoleAdmin {
		t.Fatalf("role %q with ops in adminGroups, want admin", r)
	}
	f.roles.Store(auth.RoleMap{AdminRoles: []string{"admin"}})
	if r := role(); r != auth.RoleAdmin {
		t.Fatalf("role %q with admin in adminRoles, want admin", r)
	}
}

func TestSessionPrincipalComesFromTheCookie(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	c, err := f.m.Issue(session.Identity{User: "alice", Groups: []string{"ops"}}, session.Keep{Groups: []string{"ops"}})
	must(t, err)
	a := f.auth(auth.ModeLocal, true)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		p, cookie, fail := a.AuthenticateCookie(requestWith(method, c))
		if fail != nil || cookie != nil {
			t.Fatalf("%s: fail=%v cookie=%v", method, fail, cookie)
		}
		if p.Kind != auth.KindSession || p.User != "alice" || p.Role != auth.RoleAdmin || p.Via != "cookie" || !slices.Equal(p.Groups, []string{"ops"}) {
			t.Fatalf("%s: principal %+v", method, p)
		}
	}
	// In the second half of the idle window the renewal comes back with the principal.
	f.clk.advance(6 * time.Hour)
	p, renew, fail := a.AuthenticateCookie(withCookies(c))
	if fail != nil || p.User != "alice" || renew == nil || renew.Name != f.m.CookieName() || renew.Value == "" {
		t.Fatalf("renewal: p=%+v renew=%v fail=%v", p, renew, fail)
	}
	if p2, f2 := a.Authenticate(withCookies(c)); f2 != nil || p2.User != "alice" {
		t.Fatalf("Authenticate: %+v %v", p2, f2)
	}
}

func TestBearerIsRefusedBeforeTheCookie(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	c := issue(t, f.m, "alice")
	a := f.auth(auth.ModeLocal, true)
	for _, r := range []*http.Request{withCookies(c), withCookies(), requestWith(http.MethodHead)} {
		r.Header.Set("Authorization", "Bearer x")
		p, cookie, fail := a.AuthenticateCookie(r)
		wantFailure(t, fail, auth.CodeInvalidToken, auth.ReasonBearerRefused)
		if cookie != nil || p.User != "" {
			t.Fatalf("bearer: principal %+v cookie %v", p, cookie)
		}
	}
}

func TestFailedCookieIsClearedAndTheRequestContinues(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	revoked := issue(t, f.m, "bob")
	must(t, f.store.BumpEpoch("bob"))
	expired := issue(t, f.m, "alice")
	for name, c := range map[string]*http.Cookie{
		"garbage": {Name: f.m.CookieName(), Value: "v1.garbage"},
		"revoked": revoked,
	} {
		// Without the anonymous viewer: 401, and the cookie is cleared.
		p, cookie, fail := f.auth(auth.ModeLocal, false).AuthenticateCookie(withCookies(c))
		wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
		wantClearing(t, f.m, cookie)
		if p.User != "" {
			t.Fatalf("%s: principal %+v", name, p)
		}
		// With it, the request continues as the anonymous viewer and still clears the cookie.
		p, cookie, fail = f.auth(auth.ModeLocal, true).AuthenticateCookie(withCookies(c))
		if fail != nil || p.Kind != auth.KindAnonymous {
			t.Fatalf("%s with the anonymous viewer: %+v %v", name, p, fail)
		}
		wantClearing(t, f.m, cookie)
	}
	// Past the idle limit: 401 and cleared.
	f.clk.advance(session.IdleTimeout)
	_, cookie, fail := f.auth(auth.ModeLocal, false).AuthenticateCookie(withCookies(expired))
	wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
	wantClearing(t, f.m, cookie)

	// No cookie at all: nothing to clear.
	_, cookie, fail = f.auth(auth.ModeLocal, false).AuthenticateCookie(withCookies())
	wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
	if cookie != nil {
		t.Fatalf("no cookie sent, got Set-Cookie %v", cookie)
	}
}

func TestAnonymousViewerGetsViewerOnly(t *testing.T) {
	// Even a wildcard admin mapping never reaches the anonymous viewer.
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"*"}})
	a := f.auth(auth.ModeLocal, true)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		p, cookie, fail := a.AuthenticateCookie(requestWith(method))
		if fail != nil || cookie != nil {
			t.Fatalf("%s: fail=%v cookie=%v", method, fail, cookie)
		}
		if p.Kind != auth.KindAnonymous || p.User != "anonymous" || p.Role != auth.RoleViewer || p.Via != "anonymous" || p.Interactive() {
			t.Fatalf("%s: principal %+v, want the anonymous viewer", method, p)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
		_, _, fail := a.AuthenticateCookie(requestWith(method))
		wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
	}
	// Off: a GET is 401. In oidc mode the option has no effect.
	for name, off := range map[string]*session.Authenticator{
		"off":  f.auth(auth.ModeLocal, false),
		"oidc": f.auth(auth.ModeOIDC, true),
	} {
		_, _, fail := off.AuthenticateCookie(requestWith(http.MethodGet))
		if fail == nil {
			t.Fatalf("%s: an unauthenticated GET was let in", name)
		}
		wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
	}
}

// While setup is needed (no account yet) the anonymous viewer is not granted, so the 401 tells the
// SPA to point to the setup link; once the account exists, a GET is the viewer again. A signed-in
// session is never affected, and the warning stays (the option is still on).
func TestAnonymousViewerWaitsForSetup(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	needed := true
	a := session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeLocal, Sessions: f.m, Roles: f.roles, Anonymous: true, SetupNeeded: func() bool { return needed }})
	_, _, fail := a.AuthenticateCookie(requestWith(http.MethodGet))
	wantFailure(t, fail, auth.CodeUnauthenticated, auth.ReasonNoIdentity)
	if !slices.Contains(a.Warnings(), auth.WarningAnonymousViewer) {
		t.Errorf("warnings %q, want anonymous_viewer while setup is needed", a.Warnings())
	}
	if p, _, fail := a.AuthenticateCookie(withCookies(issue(t, f.m, "alice"))); fail != nil || p.Kind != auth.KindSession {
		t.Fatalf("session while setup is needed: %+v %v", p, fail)
	}
	needed = false
	if p, _, fail := a.AuthenticateCookie(requestWith(http.MethodGet)); fail != nil || p.Kind != auth.KindAnonymous {
		t.Fatalf("after setup: %+v %v, want the anonymous viewer", p, fail)
	}
}

func TestAuthenticatorModeAndWarnings(t *testing.T) {
	f := newAuthFixture(t, auth.RoleMap{AdminUsers: []string{"alice"}})
	for _, tc := range []struct {
		mode      string
		anonymous bool
		extra     []string
		want      []string
	}{
		{auth.ModeLocal, false, nil, nil},
		{auth.ModeLocal, true, nil, []string{auth.WarningAnonymousViewer}},
		{auth.ModeOIDC, true, []string{auth.WarningOIDCOpen}, []string{auth.WarningOIDCOpen}},
		{auth.ModeOIDC, false, []string{auth.WarningOIDCOpen, auth.WarningOIDCOpen}, []string{auth.WarningOIDCOpen}},
	} {
		a := f.auth(tc.mode, tc.anonymous, tc.extra...)
		if a.Mode() != tc.mode {
			t.Errorf("Mode() = %q, want %q", a.Mode(), tc.mode)
		}
		if got := a.Warnings(); !slices.Equal(got, tc.want) {
			t.Errorf("%s anonymous=%v: Warnings() = %q, want %q", tc.mode, tc.anonymous, got, tc.want)
		}
	}
	// admin_wildcard follows the live mapping.
	a := f.auth(auth.ModeOIDC, false, auth.WarningOIDCOpen)
	f.roles.Store(auth.RoleMap{AdminUsers: []string{"*"}})
	if got, want := a.Warnings(), []string{auth.WarningAdminWildcard, auth.WarningOIDCOpen}; !slices.Equal(got, want) {
		t.Errorf("Warnings() = %q, want %q", got, want)
	}
	// Without a role holder everyone is a viewer and nothing panics.
	c := issue(t, f.m, "alice")
	bare := session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeLocal, Sessions: f.m})
	if p, _, fail := bare.AuthenticateCookie(withCookies(c)); fail != nil || p.Role != auth.RoleViewer || len(bare.Warnings()) != 0 {
		t.Fatalf("no roles: %+v %v %q", p, fail, bare.Warnings())
	}
}
