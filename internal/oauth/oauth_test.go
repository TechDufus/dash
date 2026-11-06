package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/netguard"
)

const public = "https://dash.example.com"

var t0 = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// testServer is a server over a fresh store, with two config clients ("inspector", loopback
// redirect, api only; "mcp-only", loopback redirect, mcp only) and the clock at *now.
func testServer(t *testing.T, mod func(*Options)) (*Server, *credentials.Store, *time.Time) {
	t.Helper()
	store, err := credentials.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := t0
	o := Options{
		PublicURL: public, Store: store, AccessKey: make([]byte, 32), Now: func() time.Time { return now },
		Clients: []config.OAuthClient{
			{ClientID: "inspector", Name: "Inspector", RedirectURIs: []string{"http://127.0.0.1/callback", "https://app.example.com/cb?x=1"}, Audiences: []string{"api"}},
			{ClientID: "mcp-only", Name: "MCP only", RedirectURIs: []string{"http://127.0.0.1/callback"}, Audiences: []string{"mcp"}},
		},
	}
	if mod != nil {
		mod(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, &now
}

// verifier and its S256 challenge.
const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func challengeOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authReq is a valid authorization request of the inspector for the api resource.
func authReq(mod func(url.Values)) url.Values {
	q := url.Values{
		"response_type": {"code"}, "client_id": {"inspector"}, "redirect_uri": {"http://127.0.0.1:4711/callback"},
		"state": {"xyz"}, "code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"},
		"resource": {public + "/api"}, "scope": {"read logs"},
	}
	if mod != nil {
		mod(q)
	}
	return q
}

// redirectError reads the error redirect of res: its parameters, after checking it goes to want.
func redirectError(t *testing.T, res AuthorizeResult, err error, wantBase string) url.Values {
	t.Helper()
	if err != nil || res.ErrorRedirect == "" {
		t.Fatalf("want an error redirect, got %+v, %v", res, err)
	}
	u, perr := url.Parse(res.ErrorRedirect)
	if perr != nil || !strings.HasPrefix(res.ErrorRedirect, wantBase) {
		t.Fatalf("redirect %q, want it under %q", res.ErrorRedirect, wantBase)
	}
	return u.Query()
}

func TestMetadata(t *testing.T) {
	s, _, _ := testServer(t, nil)
	m := s.Metadata([]string{"kubernetes"})
	if m.Issuer != public || m.AuthorizationEndpoint != public+"/oauth/authorize" || m.TokenEndpoint != public+"/oauth/token" ||
		m.RevocationEndpoint != public+"/oauth/revoke" || !slices.Equal(m.CodeChallengeMethodsSupported, []string{"S256"}) ||
		!slices.Equal(m.GrantTypesSupported, []string{"authorization_code", "refresh_token"}) ||
		!slices.Equal(m.TokenEndpointAuthMethodsSupported, []string{"none"}) || !m.ClientIDMetadataDocumentSupported ||
		!m.AuthorizationResponseIssParameterSupported || !slices.Equal(m.ResponseTypesSupported, []string{"code"}) {
		t.Errorf("metadata = %+v", m)
	}
	api := s.ResourceMetadata("api", []string{"kubernetes"})
	mcp := s.ResourceMetadata("mcp", []string{"kubernetes"})
	if api.Resource != public+"/api" || !slices.Equal(api.AuthorizationServers, []string{public}) ||
		!slices.Equal(api.ScopesSupported, []string{"read", "logs", "actions:kubernetes", "approvals", "push"}) {
		t.Errorf("api = %+v", api)
	}
	if mcp.Resource != public+"/mcp" || !slices.Equal(mcp.ScopesSupported, []string{"read", "logs", "actions:kubernetes"}) {
		t.Errorf("mcp = %+v", mcp)
	}
	if !s.Serves("api") || s.Serves("mcp") || s.Serves("other") {
		t.Error("Serves: want api only before the MCP server")
	}
}

// With server.directURL the issuer, the resources and the token and revoke endpoints are on it;
// /oauth/authorize stays on the public URL, behind the identity proxy.
func TestDirectURL(t *testing.T) {
	const direct = "https://dash-direct.example.com"
	s, _, _ := testServer(t, func(o *Options) { o.DirectURL = direct })
	m := s.Metadata(nil)
	if m.Issuer != direct || m.TokenEndpoint != direct+"/oauth/token" || m.RevocationEndpoint != direct+"/oauth/revoke" ||
		m.AuthorizationEndpoint != public+"/oauth/authorize" {
		t.Errorf("metadata = %+v", m)
	}
	if r := s.ResourceMetadata("mcp", nil); r.Resource != direct+"/mcp" || r.AuthorizationServers[0] != direct {
		t.Errorf("resource metadata = %+v", r)
	}
	if u := s.ResourceMetadataURL("api"); u != direct+"/.well-known/oauth-protected-resource/api" {
		t.Errorf("resource metadata URL = %s", u)
	}
	// The resource of the public URL is not dash's any more.
	res, err := s.Authorize(context.Background(), authReq(nil), nil, aliceCaller)
	if q := redirectError(t, res, err, "http://127.0.0.1:4711/callback"); q.Get("error") != "invalid_target" || q.Get("iss") != direct {
		t.Errorf("public resource with directURL: %v", q)
	}
	res, err = s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("resource", direct+"/api") }), nil, aliceCaller)
	if err != nil || !strings.HasPrefix(res.ConsentURL, public+"/consent/") {
		t.Errorf("direct resource: %+v, %v", res, err)
	}
}

// A valid request is stored and sent to the consent page; every error after the redirect URI is
// verified goes back to the client with state and iss (RFC 9207).
func TestAuthorizeValidation(t *testing.T) {
	s, _, _ := testServer(t, nil)
	res, err := s.Authorize(context.Background(), authReq(nil), nil, aliceCaller)
	if err != nil || !strings.HasPrefix(res.ConsentURL, public+"/consent/") || res.ErrorRedirect != "" {
		t.Fatalf("valid request: %+v, %v", res, err)
	}
	cb := "http://127.0.0.1:4711/callback"
	for _, tc := range []struct {
		name string
		mod  func(url.Values)
		code string
	}{
		{"no PKCE", func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") }, "invalid_request"},
		{"plain PKCE", func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		{"no PKCE method", func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request"},
		{"short challenge", func(q url.Values) { q.Set("code_challenge", "abc") }, "invalid_request"},
		{"no resource", func(q url.Values) { q.Del("resource") }, "invalid_request"},
		{"another resource", func(q url.Values) { q.Set("resource", "https://other.example.com/api") }, "invalid_target"},
		{"an audience the client may not use", func(q url.Values) { q.Set("client_id", "mcp-only") }, "invalid_target"},
		{"the mcp resource before the MCP server", func(q url.Values) { q.Set("resource", public+"/mcp") }, "invalid_target"},
		{"no state", func(q url.Values) { q.Del("state") }, "invalid_request"},
		{"a token response", func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		{"an unknown scope", func(q url.Values) { q.Set("scope", "read admin") }, "invalid_scope"},
		{"approvals", func(q url.Values) { q.Set("scope", "read approvals") }, "invalid_scope"},
		{"push", func(q url.Values) { q.Set("scope", "push") }, "invalid_scope"},
		{"actions of a plugin without actions", func(q url.Values) { q.Set("scope", "actions:flux") }, "invalid_scope"},
		{"a repeated resource", func(q url.Values) { q.Add("resource", public+"/api") }, "invalid_request"},
	} {
		res, err := s.Authorize(context.Background(), authReq(tc.mod), nil, aliceCaller)
		q := redirectError(t, res, err, cb)
		if q.Get("error") != tc.code || q.Get("iss") != public {
			t.Errorf("%s: %v, want %s with iss", tc.name, q, tc.code)
		}
		if tc.name != "no state" && q.Get("state") != "xyz" {
			t.Errorf("%s: state %q", tc.name, q.Get("state"))
		}
	}
	res, err = s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("resource", public+"/mcp") }), nil, aliceCaller)
	if q := redirectError(t, res, err, cb); q.Get("error_description") != "the MCP server is not available on this dash" {
		t.Errorf("mcp resource: %v", q)
	}
}

// The redirect URI must be one the client registered, exactly; an http loopback one matches on
// any port, but not on another host, path or query. A mismatch is never redirected to.
func TestAuthorizeRedirectMatch(t *testing.T) {
	s, _, _ := testServer(t, nil)
	for uri, ok := range map[string]bool{
		"http://127.0.0.1/callback":           true,
		"http://127.0.0.1:1/callback":         true,
		"http://127.0.0.1:65000/callback":     true,
		"https://app.example.com/cb?x=1":      true,
		"http://127.0.0.1:4711/other":         false,
		"http://localhost:4711/callback":      false,
		"http://[::1]:4711/callback":          false,
		"http://127.0.0.1:4711/callback?a=b":  false,
		"https://app.example.com/cb":          false,
		"https://app.example.com/cb?x=2":      false,
		"https://app.example.com:8443/cb?x=1": false,
		"https://evil.example.com/cb?x=1":     false,
		"http://127.0.0.1:4711/callback#f":    false,
		"":                                    false,
	} {
		res, err := s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("redirect_uri", uri) }), nil, aliceCaller)
		var oe *Error
		switch {
		case ok && (err != nil || res.ConsentURL == ""):
			t.Errorf("%q: %+v, %v; want accepted", uri, res, err)
		case !ok && (!errors.As(err, &oe) || oe.Code != "invalid_request" || res.ErrorRedirect != ""):
			t.Errorf("%q: %+v, %v; want a 400 without a redirect", uri, res, err)
		}
	}
	if _, err := s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("client_id", "nobody") }), nil, aliceCaller); err == nil {
		t.Error("an unknown client was accepted")
	}
	// An http metadata URL is refused with the reason, whoever asks.
	for _, id := range []string{"http://app.example.com/client.json", "HTTP://app.example.com/client.json"} {
		_, err := s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("client_id", id) }), nil, Caller{})
		var oe *Error
		if !errors.As(err, &oe) || oe.Code != "invalid_client" || !strings.Contains(oe.Description, "client metadata URLs must be https") {
			t.Errorf("%s: %v, want invalid_client naming https", id, err)
		}
	}
}

// dash-android may ask for approvals and push, on the api resource only.
func TestAuthorizeAndroid(t *testing.T) {
	s, _, _ := testServer(t, nil)
	android := func(q url.Values) {
		q.Set("client_id", "dash-android")
		q.Set("redirect_uri", AndroidRedirect)
		q.Set("resource", public+"/api")
		q.Set("scope", "read approvals push")
	}
	if res, err := s.Authorize(context.Background(), authReq(android), nil, aliceCaller); err != nil || res.ConsentURL == "" {
		t.Fatalf("android: %+v, %v", res, err)
	}
	res, err := s.Authorize(context.Background(), authReq(func(q url.Values) { android(q); q.Set("resource", public+"/mcp") }), nil, aliceCaller)
	if q := redirectError(t, res, err, AndroidRedirect); q.Get("error") != "invalid_target" {
		t.Errorf("android at mcp: %v", q)
	}
}

// Pending requests are bounded: past maxPending, new ones wait; expired ones free their place.
func TestAuthorizeBoundsPending(t *testing.T) {
	s, _, now := testServer(t, nil)
	for i := range maxPending {
		if _, err := s.Authorize(context.Background(), authReq(nil), nil, Caller{User: fmt.Sprintf("u%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Authorize(context.Background(), authReq(nil), nil, aliceCaller); !errors.Is(err, ErrBusy) {
		t.Fatalf("past the bound: %v", err)
	}
	*now = now.Add(PendingTTL)
	if _, err := s.Authorize(context.Background(), authReq(nil), nil, aliceCaller); err != nil {
		t.Errorf("after expiry: %v", err)
	}
}

// One person holds at most maxPendingPerUser pending requests: the next one forgets their
// oldest, never anyone else's.
func TestAuthorizeBoundsPendingPerUser(t *testing.T) {
	s, _, now := testServer(t, nil)
	vera := consentIDOf(t, s, auth.Principal{User: "vera"}, nil)
	var ids []string
	for range maxPendingPerUser + 1 {
		ids = append(ids, consentID(t, s, nil))
		*now = now.Add(time.Second)
	}
	if _, err := s.Consent(ids[0], alice); !errors.Is(err, ErrConsentNotFound) {
		t.Errorf("the oldest past the per-user bound: %v, want gone", err)
	}
	for _, id := range ids[1:] {
		if _, err := s.Consent(id, alice); err != nil {
			t.Errorf("a newer one: %v", err)
		}
	}
	if _, err := s.Consent(vera, auth.Principal{User: "vera", Role: auth.RoleViewer}); err != nil {
		t.Errorf("another person's: %v", err)
	}
}

// An anonymous caller's request is validated, then sent to sign in; dash keeps nothing for it.
// An invalid one is refused as for anyone.
func TestAuthorizeKeepsNothingForAnAnonymousCaller(t *testing.T) {
	s, _, _ := testServer(t, nil)
	for range maxPending + 76 {
		res, err := s.Authorize(context.Background(), authReq(nil), nil, Caller{})
		if err != nil || !res.SignIn || res.ConsentURL != "" {
			t.Fatalf("anonymous: %+v, %v", res, err)
		}
	}
	s.mu.Lock()
	n := len(s.pending)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("%d pending requests after anonymous ones, want 0", n)
	}
	res, err := s.Authorize(context.Background(), authReq(func(q url.Values) { q.Del("state") }), nil, Caller{})
	if q := redirectError(t, res, err, "http://127.0.0.1:4711/callback"); q.Get("error") != "invalid_request" {
		t.Errorf("anonymous without state: %v", q)
	}
	if _, err := s.Authorize(context.Background(), authReq(func(q url.Values) { q.Set("client_id", "nobody") }), nil, Caller{}); err == nil {
		t.Error("an anonymous caller's unknown client was accepted")
	}
	if _, err := s.Authorize(context.Background(), authReq(nil), nil, aliceCaller); err != nil {
		t.Errorf("a signed-in request after them: %v", err)
	}
}

// cimdServer serves a client ID metadata document over TLS; the returned client ID is its URL.
func cimdServer(t *testing.T, h func(id string) http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	var id string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h(id)(w, r) }))
	t.Cleanup(srv.Close)
	id = srv.URL + "/client.json"
	return srv, id
}

// namedDocument serves a valid metadata document of its own URL with client_name name.
func namedDocument(name string) func(id string) http.HandlerFunc {
	return func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"client_id":"` + id + `","client_name":"` + name + `","redirect_uris":["http://127.0.0.1/cb"]}`))
		}
	}
}

func goodDocument(id string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":"` + id + `","client_name":"Agent","redirect_uris":["http://127.0.0.1/cb"]}`))
	}
}

// A client ID metadata document is fetched through the guard: from a private address only with
// cimdAllowPrivate, never across a redirect or past 64 KiB, and only when it names itself. A
// valid one is cached and its client may use both audiences, never approvals or push.
func TestClientIDMetadataDocument(t *testing.T) {
	cimdReq := func(id string, mod func(url.Values)) url.Values {
		return authReq(func(q url.Values) {
			q.Set("client_id", id)
			q.Set("redirect_uri", "http://127.0.0.1:5000/cb")
			if mod != nil {
				mod(q)
			}
		})
	}
	guarded := func(srv *httptest.Server, allow bool) func(*Options) {
		return func(o *Options) {
			pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
			o.HTTPClient = netguard.NewClient(netguard.Options{AllowPrivate: allow, RootCAs: pool})
		}
	}

	hits := 0
	srv, id := cimdServer(t, func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { hits++; goodDocument(id)(w, r) }
	})
	s, _, _ := testServer(t, guarded(srv, false))
	_, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, aliceCaller)
	var oe *Error
	if !errors.As(err, &oe) || oe.Code != "invalid_client" || !strings.Contains(oe.Description, "address") || hits != 0 {
		t.Fatalf("private address: %v (hits %d), want invalid_client and no request", err, hits)
	}

	s, _, _ = testServer(t, guarded(srv, true))
	if res, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, Caller{}); err != nil || !res.SignIn || hits != 0 {
		t.Fatalf("anonymous: %+v, %v (hits %d), want sign-in and no fetch", res, err, hits)
	}
	if res, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, Caller{Fetch: true}); err != nil || !res.SignIn || hits != 1 {
		t.Fatalf("anonymous behind the proxy: %+v, %v (hits %d), want sign-in after a fetch", res, err, hits)
	}
	hits = 0
	s, _, now := testServer(t, guarded(srv, true))
	if res, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, aliceCaller); err != nil || res.ConsentURL == "" {
		t.Fatalf("cimdAllowPrivate: %+v, %v", res, err)
	}
	if res, err := s.Authorize(context.Background(), cimdReq(id, func(q url.Values) { q.Set("resource", public+"/api") }), nil, aliceCaller); err != nil || res.ConsentURL == "" {
		t.Fatalf("api audience: %+v, %v", res, err)
	}
	if hits != 1 {
		t.Errorf("fetched %d times, want once (cached)", hits)
	}
	res, err := s.Authorize(context.Background(), cimdReq(id, func(q url.Values) { q.Set("resource", public+"/api"); q.Set("scope", "read approvals") }), nil, aliceCaller)
	if q := redirectError(t, res, err, "http://127.0.0.1:5000/cb"); q.Get("error") != "invalid_scope" {
		t.Errorf("approvals: %v", q)
	}
	*now = now.Add(24 * time.Hour)
	if _, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, aliceCaller); err != nil || hits != 2 {
		t.Errorf("after 24 h: %v, hits %d; want a new fetch", err, hits)
	}

	for _, tc := range []struct {
		name, want string
		h          func(id string) http.HandlerFunc
	}{
		{"a redirect", "redirects", func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/other.json", http.StatusFound) }
		}},
		{"oversize", "64 KiB", func(id string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"client_id":"` + id + `","client_name":"` + strings.Repeat("a", 64<<10) + `","redirect_uris":["http://127.0.0.1/cb"]}`))
			}
		}},
		{"another client_id", "another client_id", func(string) http.HandlerFunc {
			return goodDocument("https://elsewhere.example.com/client.json")
		}},
		{"no name", "client_name", func(id string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"client_id":"` + id + `","redirect_uris":["http://127.0.0.1/cb"]}`))
			}
		}},
		{"a bad redirect URI", "redirect URI", func(id string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"client_id":"` + id + `","client_name":"A","redirect_uris":["http://192.0.2.1/cb"]}`))
			}
		}},
		{"a confidential client", "public clients", func(id string) http.HandlerFunc {
			return func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"client_id":"` + id + `","client_name":"A","redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"private_key_jwt"}`))
			}
		}},
		{"an error status", "answered 404", func(string) http.HandlerFunc { return http.NotFound }},
		{"the built-in app's name", "name of an app this dash knows", namedDocument("  DASH for android ")},
		{"the built-in app's ID", "name of an app this dash knows", namedDocument("dash-android")},
		{"a config client's name", "name of an app this dash knows", namedDocument("inspector ")},
		{"a config client's ID", "name of an app this dash knows", namedDocument("MCP-ONLY")},
	} {
		srv, id := cimdServer(t, tc.h)
		s, _, _ := testServer(t, guarded(srv, true))
		_, err := s.Authorize(context.Background(), cimdReq(id, nil), nil, aliceCaller)
		if !errors.As(err, &oe) || oe.Code != "invalid_client" || !strings.Contains(oe.Description, tc.want) {
			t.Errorf("%s: %v, want invalid_client about %q", tc.name, err, tc.want)
		}
	}
	for _, bad := range []string{"https://app.example.com", "https://app.example.com/", "https://app.example.com/a/../b", "https://u@app.example.com/c", "http://app.example.com/c"} {
		s, _, _ := testServer(t, nil)
		if _, err := s.Authorize(context.Background(), cimdReq(bad, nil), nil, aliceCaller); !errors.As(err, &oe) || oe.Code != "invalid_client" {
			t.Errorf("client ID %q: %v, want invalid_client", bad, err)
		}
	}
}

// A metadata document that failed is not fetched again for 60 seconds: the same answer comes
// from memory. After that it is fetched again, and a fixed document is used.
func TestCIMDFailureIsCached(t *testing.T) {
	hits, good := 0, false
	srv, id := cimdServer(t, func(id string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			hits++
			if good {
				goodDocument(id)(w, r)
				return
			}
			http.NotFound(w, r)
		}
	})
	s, _, now := testServer(t, func(o *Options) {
		pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		o.HTTPClient = netguard.NewClient(netguard.Options{AllowPrivate: true, RootCAs: pool})
	})
	q := authReq(func(q url.Values) { q.Set("client_id", id); q.Set("redirect_uri", "http://127.0.0.1:5000/cb") })
	for i := range 3 {
		_, err := s.Authorize(context.Background(), q, nil, aliceCaller)
		var oe *Error
		if !errors.As(err, &oe) || oe.Code != "invalid_client" || !strings.Contains(oe.Description, "answered 404") {
			t.Fatalf("attempt %d: %v, want the 404", i, err)
		}
	}
	if hits != 1 {
		t.Errorf("fetched %d times within a minute, want once", hits)
	}
	good = true
	*now = now.Add(cimdFailTTL - time.Second)
	if _, err := s.Authorize(context.Background(), q, nil, aliceCaller); err == nil || hits != 1 {
		t.Errorf("just before a minute: %v, hits %d; want the cached failure", err, hits)
	}
	*now = now.Add(time.Second)
	if res, err := s.Authorize(context.Background(), q, nil, aliceCaller); err != nil || res.ConsentURL == "" || hits != 2 {
		t.Errorf("after a minute: %+v, %v, hits %d; want a new fetch that works", res, err, hits)
	}
}
