package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
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
func testOAuth(t *testing.T, d *Deps) *oauth.Server {
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
		PublicURL: d.Cfg.PublicURL, DirectURL: d.Cfg.DirectURL, Store: d.Credentials, AccessKey: make([]byte, 32),
		Clients: []config.OAuthClient{{ClientID: "inspector", Name: "Inspector", RedirectURIs: []string{"http://127.0.0.1/callback"}, Audiences: []string{"api", "mcp"}}},
		Now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatal(err)
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
	for _, aud := range []string{"api", "mcp"} {
		w := req{path: "/.well-known/oauth-protected-resource/" + aud, header: origin}.do(s.Handler())
		var r oauth.ProtectedResourceMetadata
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &r) != nil || r.Resource != testOrigin+"/"+aud {
			t.Errorf("%s resource metadata = %d %s", aud, w.Code, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") == "" {
			t.Errorf("%s resource metadata without ACAO", aud)
		}
	}
	if w := (req{path: "/.well-known/oauth-protected-resource/settings"}).do(s.Handler()); w.Code != http.StatusNotFound {
		t.Errorf("unknown resource = %d", w.Code)
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
	w := req{path: authorizeURL("mcp", nil), header: origin}.do(s.Handler())
	if w.Code != http.StatusFound || !strings.HasPrefix(w.Header().Get("Location"), testOrigin+"/consent/") {
		t.Fatalf("authorize = %d %v", w.Code, w.Header())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v, want no ACAO and no-store", w.Header())
	}
	w = req{path: authorizeURL("mcp", func(q url.Values) { q.Set("code_challenge_method", "plain") }), header: origin}.do(s.Handler())
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || loc == nil || loc.Host != "127.0.0.1:4711" || loc.Query().Get("error") != "invalid_request" ||
		loc.Query().Get("iss") != testOrigin || loc.Query().Get("state") != "st" {
		t.Errorf("plain PKCE = %d %v", w.Code, w.Header())
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("an authorize error redirect carries ACAO")
	}
	w = req{path: authorizeURL("mcp", func(q url.Values) { q.Set("redirect_uri", "https://evil.example.com/cb") })}.do(s.Handler())
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), `"error":"invalid_request"`) {
		t.Errorf("unregistered redirect = %d %v %s", w.Code, w.Header(), w.Body)
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
