package oauth

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
)

var (
	alice = auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin, Groups: []string{"ops"}}
	vera  = auth.Principal{Kind: auth.KindProxy, User: "vera", Role: auth.RoleViewer}

	aliceCaller = Caller{User: "alice"}
)

// consentID starts an authorization request of alice and returns its consent ID.
func consentID(t *testing.T, s *Server, mod func(url.Values), actions ...string) string {
	t.Helper()
	return consentIDOf(t, s, alice, mod, actions...)
}

// consentIDOf starts an authorization request of p and returns its consent ID.
func consentIDOf(t *testing.T, s *Server, p auth.Principal, mod func(url.Values), actions ...string) string {
	t.Helper()
	res, err := s.Authorize(context.Background(), authReq(mod), actions, Caller{User: p.User})
	if err != nil || res.ConsentURL == "" {
		t.Fatalf("authorize: %+v, %v", res, err)
	}
	return res.ConsentURL[strings.LastIndex(res.ConsentURL, "/")+1:]
}

// approve approves request id for p with scopes and returns the code from the redirect.
func approve(t *testing.T, s *Server, id string, p auth.Principal, scopes ...string) url.Values {
	t.Helper()
	redirect, err := s.Approve(id, p, scopes, nil)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	u, _ := url.Parse(redirect)
	return u.Query()
}

func exchange(s *Server, code string, mod func(url.Values)) (*TokenResponse, *TokenEvent, *Error) {
	f := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {"inspector"},
		"redirect_uri": {"http://127.0.0.1:4711/callback"}, "code_verifier": {verifier}, "resource": {public + "/api"},
	}
	if mod != nil {
		mod(f)
	}
	return s.Token(f)
}

func refresh(s *Server, rt string, mod func(url.Values)) (*TokenResponse, *TokenEvent, *Error) {
	f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"inspector"}}
	if mod != nil {
		mod(f)
	}
	return s.Token(f)
}

// The consent shows what the client asked for that the person may grant, ticks only read, and
// belongs to the person who sent the request.
func TestConsent(t *testing.T) {
	s, _, _ := testServer(t, nil)
	id := consentIDOf(t, s, vera, func(q url.Values) { q.Set("scope", "read logs actions:kubernetes") }, "kubernetes")
	if _, err := s.Consent(id, vera); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Consent(id, alice); !errors.Is(err, ErrConsentNotFound) {
		t.Errorf("another person: %v, want not found", err)
	}
	c, _ := s.Consent(id, vera)
	if !slices.Equal(c.Scopes, []string{"read", "logs"}) || !slices.Equal(c.Withheld, []string{"actions:kubernetes"}) || !slices.Equal(c.DefaultScopes, []string{"read"}) ||
		c.Client.ID != "inspector" || c.Client.Kind != "config" || c.Audience != "api" || c.RedirectURI != "http://127.0.0.1:4711/callback" {
		t.Errorf("viewer's consent = %+v", c)
	}
	if _, err := s.Approve(id, vera, []string{"read", "actions:kubernetes"}, nil); !errors.Is(err, ErrScopes) {
		t.Errorf("a scope beyond the role: %v", err)
	}
	if _, err := s.Approve(id, vera, nil, nil); !errors.Is(err, ErrScopes) {
		t.Errorf("no scope: %v", err)
	}
	id = consentID(t, s, func(q url.Values) { q.Set("scope", "read logs actions:kubernetes") }, "kubernetes")
	if c, _ := s.Consent(id, alice); !slices.Equal(c.Scopes, []string{"read", "logs", "actions:kubernetes"}) || c.Withheld == nil || len(c.Withheld) != 0 || !slices.Equal(c.DefaultScopes, []string{"read"}) {
		t.Errorf("admin's consent = %+v", c)
	}
	id = consentIDOf(t, s, vera, func(q url.Values) { q.Set("scope", "actions:kubernetes") }, "kubernetes")
	if _, err := s.Consent(id, vera); !errors.Is(err, ErrNothingToGrant) {
		t.Errorf("only actions for a viewer: %v", err)
	}
	id = consentIDOf(t, s, vera, nil)
	redirect, err := s.Deny(id, vera)
	if u, _ := url.Parse(redirect); err != nil || u.Query().Get("error") != "access_denied" || u.Query().Get("state") != "xyz" || u.Query().Get("iss") != public {
		t.Errorf("deny = %q, %v", redirect, err)
	}
	if _, err := s.Consent(id, vera); !errors.Is(err, ErrConsentNotFound) {
		t.Errorf("after deny: %v", err)
	}
}

// A request bound to no one belongs to no one: a principal without a user neither reads nor
// decides it, even when the request's own user is empty too.
func TestConsentNeedsAUser(t *testing.T) {
	s, store, _ := testServer(t, nil)
	id := consentIDOf(t, s, vera, nil)
	s.mu.Lock()
	s.pending[id].user = ""
	s.mu.Unlock()
	nobody := auth.Principal{Role: auth.RoleAdmin}
	if _, err := s.Consent(id, nobody); !errors.Is(err, ErrConsentNotFound) {
		t.Errorf("consent without a user: %v, want not found", err)
	}
	if _, err := s.Approve(id, nobody, []string{"read"}, nil); !errors.Is(err, ErrConsentNotFound) {
		t.Errorf("approve without a user: %v, want not found", err)
	}
	if n := len(store.Grants("")); n != 0 {
		t.Errorf("grants = %d", n)
	}
}

// The whole flow: consent, code with state and iss, exchange with PKCE for an access token and
// a refresh token; the grant records the person's groups and the approved scopes.
func TestCodeExchange(t *testing.T) {
	s, store, _ := testServer(t, nil)
	q := approve(t, s, consentID(t, s, nil), alice, "read")
	if q.Get("state") != "xyz" || q.Get("iss") != public || q.Get("code") == "" {
		t.Fatalf("redirect = %v", q)
	}
	res, ev, oe := exchange(s, q.Get("code"), nil)
	if oe != nil || ev != nil {
		t.Fatalf("exchange: %v %v", oe, ev)
	}
	if !strings.HasPrefix(res.AccessToken, AccessPrefix) || strings.Contains(res.AccessToken, ".") || !strings.HasPrefix(res.RefreshToken, credentials.RefreshPrefix) ||
		res.TokenType != "Bearer" || res.ExpiresIn != 900 || res.Scope != "read" {
		t.Fatalf("token response = %+v", res)
	}
	a, err := s.OpenAccess(res.AccessToken)
	if err != nil || a.Audience != "api" || !slices.Equal(a.Scopes, []string{"read"}) {
		t.Fatalf("access = %+v, %v", a, err)
	}
	g, ok := store.GrantByID(a.GrantID)
	if !ok || g.User != "alice" || !slices.Equal(g.Groups, []string{"ops"}) || !slices.Equal(g.Scopes, []string{"read"}) || g.ClientID != "inspector" {
		t.Errorf("grant = %+v", g)
	}
}

// PKCE: a wrong verifier, or none, fails, and the grant the code made goes with the code; so do
// another client and another redirect URI. A code works once: using it again revokes the grant.
func TestCodeExchangeChecks(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mod        func(url.Values)
	}{
		{"wrong verifier", "invalid_grant", func(f url.Values) { f.Set("code_verifier", strings.Repeat("a", 43)) }},
		{"no verifier", "invalid_grant", func(f url.Values) { f.Del("code_verifier") }},
		{"the challenge as verifier", "invalid_grant", func(f url.Values) { f.Set("code_verifier", challengeOf(verifier)) }},
		{"another client", "invalid_grant", func(f url.Values) { f.Set("client_id", "dash-android") }},
		{"another redirect", "invalid_grant", func(f url.Values) { f.Set("redirect_uri", "http://127.0.0.1:4712/callback") }},
		{"another resource", "invalid_target", func(f url.Values) { f.Set("resource", public+"/mcp") }},
	} {
		s, store, _ := testServer(t, nil)
		code := approve(t, s, consentID(t, s, nil), alice, "read").Get("code")
		_, _, oe := exchange(s, code, tc.mod)
		if oe == nil || oe.Code != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, oe, tc.code)
		}
		if n := len(store.Grants("alice")); n != 0 {
			t.Errorf("%s: %d grants left", tc.name, n)
		}
		if _, _, oe := exchange(s, code, nil); oe == nil {
			t.Errorf("%s: the code worked after a failed exchange", tc.name)
		}
	}
	s, store, _ := testServer(t, nil)
	code := approve(t, s, consentID(t, s, nil), alice, "read").Get("code")
	if _, _, oe := exchange(s, code, nil); oe != nil {
		t.Fatal(oe)
	}
	_, ev, oe := exchange(s, code, nil)
	if oe == nil || oe.Code != "invalid_grant" || ev == nil || ev.Reason != "code_reuse" || len(store.Grants("")) != 0 {
		t.Errorf("reuse: %v %+v, grants %d", oe, ev, len(store.Grants("")))
	}
}

// A code lives 60 seconds; one never exchanged takes the grant of its consent with it, and
// OnGrantExpired learns of that grant once, after the sweep.
func TestCodeExpiry(t *testing.T) {
	s, store, now := testServer(t, nil)
	var expired []credentials.Grant
	s.OnGrantExpired(func(g credentials.Grant) {
		if !s.mu.TryLock() {
			t.Error("the grant was revoked under the server's mutex")
		} else {
			s.mu.Unlock()
		}
		expired = append(expired, g)
	})
	code := approve(t, s, consentID(t, s, nil), alice, "read").Get("code")
	gid := store.Grants("")[0].ID
	*now = now.Add(CodeTTL)
	if _, _, oe := exchange(s, code, nil); oe == nil || oe.Code != "invalid_grant" {
		t.Errorf("expired code: %v", oe)
	}
	if n := len(store.Grants("")); n != 0 {
		t.Errorf("%d grants left by an unused code", n)
	}
	if len(expired) != 1 || expired[0].ID != gid || expired[0].User != "alice" {
		t.Errorf("expired = %+v, want the grant %s once", expired, gid)
	}
	// An exchanged code's grant is not expired by the sweep.
	if _, _, oe := exchange(s, approve(t, s, consentID(t, s, nil), alice, "read").Get("code"), nil); oe != nil {
		t.Fatal(oe)
	}
	*now = now.Add(CodeTTL)
	consentID(t, s, nil)
	if len(expired) != 1 || len(store.Grants("")) != 1 {
		t.Errorf("after an exchanged code expired: expired %d, grants %d", len(expired), len(store.Grants("")))
	}
	// The exchange is recorded on the grant, so the store's prune (at start-up too, when the
	// codes are forgotten) keeps it.
	if g := store.Grants(""); len(g) != 1 || g[0].LastUsedAt == nil {
		t.Fatalf("the exchanged grant = %+v, want its lastUsedAt set", g)
	}
	if n, err := store.PruneExpired(now.Add(time.Hour)); err != nil || n != 0 || len(store.Grants("")) != 1 {
		t.Errorf("prune removed %d (%v) of the exchanged grant", n, err)
	}
}

// Every refresh rotates: the new token works, the old one used again revokes the grant and
// with it every access token. Another client's refresh changes nothing.
func TestRefreshRotation(t *testing.T) {
	s, store, now := testServer(t, nil)
	res, _, _ := exchange(s, approve(t, s, consentID(t, s, nil), alice, "read", "logs").Get("code"), nil)
	if _, _, oe := refresh(s, res.RefreshToken, func(f url.Values) { f.Set("client_id", "other") }); oe == nil || oe.Code != "invalid_grant" {
		t.Fatalf("another client: %v", oe)
	}
	*now = now.Add(time.Minute)
	r2, ev, oe := refresh(s, res.RefreshToken, func(f url.Values) { f.Set("scope", "read") })
	if oe != nil || ev != nil || r2.RefreshToken == res.RefreshToken || r2.Scope != "read" {
		t.Fatalf("refresh: %+v %v %v", r2, ev, oe)
	}
	if _, _, oe := refresh(s, r2.RefreshToken, func(f url.Values) { f.Set("scope", "read push") }); oe == nil || oe.Code != "invalid_scope" {
		t.Errorf("widening scope: %v", oe)
	}
	if _, _, oe := refresh(s, r2.RefreshToken, func(f url.Values) { f.Set("resource", public+"/mcp") }); oe == nil || oe.Code != "invalid_target" {
		t.Errorf("another resource: %v", oe)
	}
	a, err := s.OpenAccess(r2.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	_, ev, oe = refresh(s, res.RefreshToken, nil)
	if oe == nil || oe.Code != "invalid_grant" || ev == nil || ev.Reason != "refresh_reuse" || ev.Grant.ID != a.GrantID {
		t.Fatalf("reuse: %v %+v", oe, ev)
	}
	if _, ok := store.GrantByID(a.GrantID); ok {
		t.Error("reuse left the grant")
	}
	if _, _, oe := refresh(s, r2.RefreshToken, nil); oe == nil {
		t.Error("the newest refresh token works after reuse")
	}
}

// Access tokens expire after 15 minutes, and open only under the key that sealed them: a
// server with another key (a restart without DASH_SECRET_KEY) refuses them.
func TestAccessTokenSeal(t *testing.T) {
	s, store, now := testServer(t, nil)
	res, _, _ := exchange(s, approve(t, s, consentID(t, s, nil), alice, "read").Get("code"), nil)
	other, err := New(Options{PublicURL: public, Store: store, AccessKey: []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.OpenAccess(res.AccessToken); !errors.Is(err, ErrAccessInvalid) {
		t.Errorf("another key: %v", err)
	}
	same, _ := New(Options{PublicURL: public, Store: store, AccessKey: make([]byte, 32), Now: func() time.Time { return *now }})
	if _, err := same.OpenAccess(res.AccessToken); err != nil {
		t.Errorf("the same key: %v", err)
	}
	tampered := res.AccessToken[:len(res.AccessToken)-2] + "AA"
	if tampered == res.AccessToken {
		tampered = res.AccessToken[:len(res.AccessToken)-2] + "BB"
	}
	for _, bad := range []string{tampered, "dash_at_v1." + res.AccessToken[8:], res.AccessToken[8:], ""} {
		if _, err := s.OpenAccess(bad); err == nil {
			t.Errorf("%q opened", bad)
		}
	}
	*now = now.Add(AccessTTL)
	if _, err := s.OpenAccess(res.AccessToken); !errors.Is(err, ErrAccessExpired) {
		t.Errorf("after 15 minutes: %v", err)
	}
}

// RFC 7009: a refresh token (current or rotated) or an access token names its grant; another
// client's request, or an unknown token, names nothing.
func TestRevocationTarget(t *testing.T) {
	s, _, _ := testServer(t, nil)
	res, _, _ := exchange(s, approve(t, s, consentID(t, s, nil), alice, "read").Get("code"), nil)
	a, _ := s.OpenAccess(res.AccessToken)
	r2, _, _ := refresh(s, res.RefreshToken, nil)
	for name, tok := range map[string]string{"refresh": r2.RefreshToken, "rotated refresh": res.RefreshToken, "access": res.AccessToken} {
		g, ok := s.RevocationTarget(url.Values{"token": {tok}, "client_id": {"inspector"}})
		if !ok || g.ID != a.GrantID {
			t.Errorf("%s: %+v %v", name, g, ok)
		}
		if _, ok := s.RevocationTarget(url.Values{"token": {tok}, "client_id": {"other"}}); ok {
			t.Errorf("%s from another client named the grant", name)
		}
	}
	forged := r2.RefreshToken[:len(r2.RefreshToken)-4] + "AAAA"
	for _, tok := range []string{"nope", forged, ""} {
		if _, ok := s.RevocationTarget(url.Values{"token": {tok}}); ok {
			t.Errorf("%q named a grant", tok)
		}
	}
}

// Concurrent approvals of one request make one grant.
func TestApproveOnce(t *testing.T) {
	s, store, _ := testServer(t, nil)
	id := consentID(t, s, nil)
	done := make(chan error, 8)
	for range 8 {
		go func() { _, err := s.Approve(id, alice, []string{"read"}, nil); done <- err }()
	}
	ok := 0
	for range 8 {
		if <-done == nil {
			ok++
		}
	}
	if ok != 1 || len(store.Grants("alice")) != 1 {
		t.Errorf("%d approvals succeeded, %d grants", ok, len(store.Grants("alice")))
	}
}

// An approval whose audit intent fails changes nothing, and the request can be approved again.
func TestApproveBeforeFails(t *testing.T) {
	s, store, _ := testServer(t, nil)
	id := consentID(t, s, nil)
	boom := errors.New("audit down")
	if _, err := s.Approve(id, alice, []string{"read"}, func(credentials.Grant) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("approve: %v", err)
	}
	if len(store.Grants("")) != 0 {
		t.Error("a grant was stored")
	}
	if _, err := s.Approve(id, alice, []string{"read"}, nil); err != nil {
		t.Errorf("second approval: %v", err)
	}
}
