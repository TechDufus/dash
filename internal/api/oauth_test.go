package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/tokens"
)

// testOAuth is an authorization server over d's credential store (a new one in a data dir
// when d has none), at d's public and direct URLs, with one config client: "inspector", a
// loopback redirect, both audiences.
func testOAuth(t *testing.T, d *Deps) *oauth.Server { return testOAuthWith(t, d, nil) }

// testOAuthWith is testOAuth fetching client ID metadata documents with hc (nil: netguard's).
func testOAuthWith(t *testing.T, d *Deps, hc *http.Client) *oauth.Server {
	t.Helper()
	if d.Credentials == nil {
		store, err := credentials.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		d.Credentials = store
	}
	if d.Tokens == nil {
		d.Tokens = tokens.New(d.Credentials, auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice"}}), nil, func() time.Time { return fixedNow })
	}
	s, err := oauth.New(oauth.Options{
		PublicURL: d.Cfg.PublicURL, DirectURL: d.Cfg.DirectURL, Store: d.Credentials, AccessKey: make([]byte, 32), HTTPClient: hc,
		Clients: []config.OAuthClient{{ClientID: "inspector", Name: "Inspector", RedirectURIs: []string{"http://127.0.0.1/callback"}, Audiences: []string{"api", "mcp"}}},
		Now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := d.Tokens.(*tokens.Resolver); ok {
		r.SetOAuth(s)
	}
	return s
}

// oauthServer is a proxy-mode server with the authorization server on and corsOrigins listing
// https://inspector.example.com; mod adjusts the deps first.
func oauthServer(t *testing.T, mod func(*Deps)) *Server {
	t.Helper()
	return newServer(func(d *Deps) {
		d.Cfg.CORSOrigins = []string{"https://inspector.example.com"}
		if mod != nil {
			mod(d)
		}
		d.OAuth = testOAuth(t, d)
	})
}

const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func testChallenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeURL is a valid authorization request of the inspector for audience.
func authorizeURL(audience string, mod func(url.Values)) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {"inspector"}, "redirect_uri": {"http://127.0.0.1:4711/callback"},
		"state": {"st"}, "code_challenge": {testChallenge(testVerifier)}, "code_challenge_method": {"S256"},
		"resource": {testOrigin + "/" + audience},
	}
	if mod != nil {
		mod(q)
	}
	return "/oauth/authorize?" + q.Encode()
}

// Without the authorization server every OAuth path answers a JSON 404, never the SPA, and
// /me says so.
func TestOAuthOffAnswers404(t *testing.T) {
	s := newServer(nil)
	for _, p := range []string{
		"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/api",
		"/.well-known/oauth-protected-resource/mcp", "/.well-known/openid-configuration",
		authorizeURL("mcp", nil), "/oauth/token", "/oauth/anything",
	} {
		w := req{path: p}.do(s.Handler())
		if w.Code != http.StatusNotFound || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s = %d %s, want a JSON 404", p, w.Code, w.Header().Get("Content-Type"))
		}
	}
}

// GET on the token and revoke endpoints is 405 with Allow: POST, an OAuth error body.
func TestOAuthFormEndpointsAnswer405ToGet(t *testing.T) {
	s := oauthServer(t, nil)
	for _, p := range []string{"/oauth/token", "/oauth/revoke"} {
		w := req{path: p}.do(s.Handler())
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "POST" || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
			t.Errorf("GET %s = %d %v %s", p, w.Code, w.Header(), w.Body)
		}
	}
}

// The metadata documents are served, carry the listed origin's Access-Control-Allow-Origin
// (never credentials), and an unknown resource is 404.
func TestOAuthMetadataRoutes(t *testing.T) {
	s := oauthServer(t, nil)
	origin := map[string]string{"Origin": "https://inspector.example.com"}
	w := req{path: "/.well-known/oauth-authorization-server", header: origin}.do(s.Handler())
	var m oauth.AuthorizationServerMetadata
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Issuer != testOrigin || m.TokenEndpoint != testOrigin+"/oauth/token" {
		t.Fatalf("AS metadata = %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example.com" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("CORS headers = %v", w.Header())
	}
	w = req{path: "/.well-known/oauth-protected-resource/api", header: origin}.do(s.Handler())
	var r oauth.ProtectedResourceMetadata
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.Resource != testOrigin+"/api" {
		t.Errorf("api resource metadata = %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("api resource metadata without ACAO")
	}
	// The MCP server is not in this release: its resource is unknown, as any other.
	for _, aud := range []string{"mcp", "settings"} {
		if w := (req{path: "/.well-known/oauth-protected-resource/" + aud}).do(s.Handler()); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
			t.Errorf("%s resource metadata = %d %s", aud, w.Code, w.Body)
		}
	}
	// An origin that is not listed gets nothing.
	w = req{path: "/.well-known/oauth-authorization-server", header: map[string]string{"Origin": "https://evil.example.com"}}.do(s.Handler())
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("an unlisted origin got ACAO")
	}
}

// /oauth/authorize sends a valid request to the SPA's consent page and an invalid one back to
// the client; it never carries Access-Control-Allow-Origin, and nothing on /oauth is stored.
func TestOAuthAuthorizeRoute(t *testing.T) {
	s := oauthServer(t, nil)
	origin := map[string]string{"Origin": "https://inspector.example.com"}
	w := req{path: authorizeURL("api", nil), header: origin}.do(s.Handler())
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), testOrigin+"/consent/") {
		t.Fatalf("authorize = %d %v", w.Code, w.Header())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v, want no ACAO and no-store", w.Header())
	}
	w = req{path: authorizeURL("api", func(q url.Values) { q.Set("code_challenge_method", "plain") }), header: origin}.do(s.Handler())
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc == nil || loc.Host != "127.0.0.1:4711" || loc.Query().Get("error") != "invalid_request" ||
		loc.Query().Get("iss") != testOrigin || loc.Query().Get("state") != "st" {
		t.Errorf("plain PKCE = %d %v", w.Code, w.Header())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("an authorize error redirect carries ACAO")
	}
	w = req{path: authorizeURL("api", func(q url.Values) { q.Set("redirect_uri", "https://evil.example.com/cb") })}.do(s.Handler())
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
		t.Errorf("unregistered redirect = %d %v %s", w.Code, w.Header(), w.Body)
	}
	w = req{path: authorizeURL("mcp", nil)}.do(s.Handler())
	if loc, _ := url.Parse(w.Header().Get("Location")); w.Code != http.StatusFound || loc == nil || loc.Query().Get("error") != "invalid_target" ||
		loc.Query().Get("error_description") != "the MCP server is not available on this dash" {
		t.Errorf("mcp resource = %d %v", w.Code, w.Header())
	}
}

// With server.directURL, the challenge of a 401 points at the resource metadata on it, and the
// API answers its host.
func TestOAuthDirectURLChallenge(t *testing.T) {
	const direct = "https://dash-direct.example.com"
	s := oauthServer(t, func(d *Deps) { d.Cfg.DirectURL = direct })
	w := req{path: "/api/v1/me", host: "dash-direct.example.com", header: map[string]string{"Authorization": "Bearer dash_pat_nope"}}.do(s.Handler())
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != `Bearer resource_metadata="`+direct+`/.well-known/oauth-protected-resource/api"` {
		t.Errorf("401 = %d %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
	var m oauth.AuthorizationServerMetadata
	w = req{path: "/.well-known/oauth-authorization-server", host: "dash-direct.example.com"}.do(s.Handler())
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &m) != nil || m.Issuer != direct || m.AuthorizationEndpoint != testOrigin+"/oauth/authorize" {
		t.Errorf("metadata on the direct host = %d %s", w.Code, w.Body)
	}
}

// With server.directURL, /oauth/authorize and the consent page are the JSON 404 on the direct
// host, which bypasses the identity proxy; on the public host they work.
func TestOAuthPersonPagesAreNotOnTheDirectHost(t *testing.T) {
	s := oauthServer(t, func(d *Deps) { d.Cfg.DirectURL = "https://dash-direct.example.com" })
	for _, host := range []string{"dash-direct.example.com", "DASH-DIRECT.example.com.:443"} {
		for _, path := range []string{authorizeURL("api", nil), "/consent/abc", "/consent"} {
			w := req{path: path, host: host}.do(s.Handler())
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
				t.Errorf("%s on %s = %d %s", path, host, w.Code, w.Body)
			}
		}
	}
	direct := authorizeURL("api", func(q url.Values) { q.Set("resource", "https://dash-direct.example.com/api") })
	if w := (req{path: direct}).do(s.Handler()); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), testOrigin+"/consent/") {
		t.Errorf("authorize on the public host = %d %v", w.Code, w.Header())
	}
	if w := (req{path: "/consent/abc"}).do(s.Handler()); strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Errorf("the consent page on the public host = %d %s", w.Code, w.Body)
	}
}

// On server.directURL's host no identity header is believed, even from a trusted (loopback)
// peer, and neither is an upstream request id: defence in depth should a route deliver the
// direct host from the proxy's address. On the public host both still work.
func TestIdentityHeadersAreRefusedOnTheDirectHost(t *testing.T) {
	s := oauthServer(t, func(d *Deps) { d.Cfg.DirectURL = "https://dash-direct.example.com" })
	rid := map[string]string{requestIDHeader: "upstream-1"}
	for _, host := range []string{"dash-direct.example.com", "DASH-DIRECT.example.com.:443"} {
		w := req{path: "/api/v1/me", host: host, header: rid}.do(s.Handler())
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthenticated"`) {
			t.Errorf("identity headers on %s = %d %s", host, w.Code, w.Body)
		}
		if w.Header().Get(requestIDHeader) == "upstream-1" {
			t.Errorf("the upstream request id was believed on %s", host)
		}
	}
	w := req{path: "/api/v1/me", header: rid}.do(s.Handler())
	if w.Code != http.StatusOK || w.Header().Get(requestIDHeader) != "upstream-1" {
		t.Errorf("on the public host = %d %s, request id %q", w.Code, w.Body, w.Header().Get(requestIDHeader))
	}
}

// A preflight of /oauth/authorize, a navigation, gets no Access-Control-* header, while the
// token endpoint's still does.
func TestOAuthAuthorizePreflightGetsNoCORS(t *testing.T) {
	s := oauthServer(t, nil)
	pre := map[string]string{"Origin": "https://inspector.example.com", "Access-Control-Request-Method": "GET"}
	w := req{method: http.MethodOptions, path: authorizeURL("api", nil), header: pre, raw: true}.do(s.Handler())
	for k := range w.Header() {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Errorf("OPTIONS /oauth/authorize = %d with %s: %q", w.Code, k, w.Header().Get(k))
		}
	}
	pre["Access-Control-Request-Method"] = "POST"
	if w := (req{method: http.MethodOptions, path: "/oauth/token", header: pre, raw: true}).do(s.Handler()); w.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example.com" {
		t.Errorf("OPTIONS /oauth/token = %d %v", w.Code, w.Header())
	}
}

// An anonymous caller's valid request goes to sign in with next set to the same request, and dash
// keeps nothing for it: 1100 of them (past the 1024 pending requests dash holds) leave a person's
// request working. An invalid one is answered as before; one too long for next is 400.
func TestOAuthAuthorizeKeepsNothingForAnonymousCallers(t *testing.T) {
	s := oauthServer(t, nil)
	anon := map[string]string{"X-Forwarded-Preferred-Username": ""}
	path := authorizeURL("api", nil)
	for i := range 1100 {
		w := req{path: path, header: anon}.do(s.Handler())
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/login?next="+url.QueryEscape(path) {
			t.Fatalf("anonymous %d = %d %v", i+1, w.Code, w.Header())
		}
	}
	if w := (req{path: path}).do(s.Handler()); w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), testOrigin+"/consent/") {
		t.Errorf("alice after them = %d %v", w.Code, w.Header())
	}
	w := req{path: authorizeURL("api", func(q url.Values) { q.Set("code_challenge_method", "plain") }), header: anon}.do(s.Handler())
	if loc, _ := url.Parse(w.Header().Get("Location")); w.Code != http.StatusFound || loc == nil || loc.Query().Get("error") != "invalid_request" {
		t.Errorf("anonymous invalid = %d %v", w.Code, w.Header())
	}
	w = req{path: authorizeURL("api", func(q url.Values) { q.Set("state", strings.Repeat("s", 1000)) }), header: anon}.do(s.Handler())
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "too long to carry through sign-in") {
		t.Errorf("too long for next = %d %s", w.Code, w.Body)
	}
}

// fetchCounter is an HTTP client that counts client ID metadata fetches and fails each one.
type fetchCounter struct{ n atomic.Int32 }

func (f *fetchCounter) RoundTrip(*http.Request) (*http.Response, error) {
	f.n.Add(1)
	return nil, errors.New("no metadata here")
}

// cimdAuthorizeURL is a valid authorization request of a client ID metadata document client.
func cimdAuthorizeURL() string {
	return authorizeURL("api", func(q url.Values) {
		q.Set("client_id", "https://client.example.com/oauth/client.json")
		q.Set("redirect_uri", "https://client.example.com/callback")
	})
}

// The anonymous viewer of the local mode is not signed in (G7-R3 fix E1): its authorization
// request goes to sign in, so dash keeps no pending request for it (Authorize stores one only
// for a signed-in caller, whose answer is the consent page), and it never makes dash fetch a
// client ID metadata document.
func TestOAuthAuthorizeAnonymousViewerSignsIn(t *testing.T) {
	fc := &fetchCounter{}
	opt, _ := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{"alice"}}, true, nil)
	s := newServer(func(d *Deps) {
		opt(d)
		d.OAuth = testOAuthWith(t, d, &http.Client{Transport: fc})
	})
	for _, path := range []string{authorizeURL("api", nil), cimdAuthorizeURL()} {
		w := req{path: path}.do(s.Handler())
		if w.Code != http.StatusFound || w.Header().Get("Location") != "/login?next="+url.QueryEscape(path) {
			t.Errorf("the anonymous viewer's %s = %d %v, want the sign-in redirect", path, w.Code, w.Header())
		}
	}
	if n := fc.n.Load(); n != 0 {
		t.Errorf("the anonymous viewer made %d metadata fetches, want 0", n)
	}
}

// Behind the identity proxy an anonymous caller on the public URL's host makes dash fetch a
// client ID metadata document only when its peer is the proxy: an untrusted peer bypassed it.
func TestOAuthAuthorizeFetchNeedsTheProxyPeer(t *testing.T) {
	fc := &fetchCounter{}
	s := newServer(func(d *Deps) { d.OAuth = testOAuthWith(t, d, &http.Client{Transport: fc}) })
	anon := map[string]string{"X-Forwarded-Preferred-Username": ""}
	w := req{path: cimdAuthorizeURL(), peer: "192.0.2.202:1234", header: anon}.do(s.Handler())
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), "/login?next=") || fc.n.Load() != 0 {
		t.Errorf("untrusted peer = %d %v, %d fetches; want the sign-in redirect and none", w.Code, w.Header(), fc.n.Load())
	}
	w = req{path: cimdAuthorizeURL(), header: anon}.do(s.Handler())
	if w.Code != http.StatusBadRequest || fc.n.Load() != 1 {
		t.Errorf("the proxy's peer = %d %s, %d fetches; want the failed fetch's 400", w.Code, w.Body, fc.n.Load())
	}
}
