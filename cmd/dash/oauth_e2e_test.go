package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/secret"
)

const (
	e2ePublic = "https://dash.example.com"
	e2eDirect = "https://dash-direct.example.com"
)

// e2e is dash in the proxy mode behind its identity proxy, with server.directURL and OAuth on,
// built the way main builds it (configuration from the environment, startAuth, api.New) and
// served over real HTTP. Requests name the public or the direct host; both reach the one
// listener, as the proxy and the chart's direct route do.
type e2e struct {
	t     *testing.T
	srv   *httptest.Server
	mcpOn *atomic.Bool // mcp.enabled, live: starts as the configuration says (true)
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	res, err := config.Resolve(config.Sources{Registry: secret.NewRegistry(), Environ: []string{
		"DASH_SERVER_PUBLIC_URL=" + e2ePublic, "DASH_SERVER_DIRECT_URL=" + e2eDirect,
		"DASH_DATA_DIR=" + t.TempDir(), "DASH_AUTH_MODE=proxy", `DASH_AUTH_PROXY_ADMIN_USERS=["alice"]`,
		"DASH_AUTH_OAUTH_ENABLED=true", "DASH_MCP_ENABLED=true",
		`DASH_AUTH_OAUTH_CLIENTS=[{"clientID":"agent","name":"Agent","redirectURIs":["http://127.0.0.1/callback"],"audiences":["api","mcp"]}]`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	pa, _, err := startLocal(t, res, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pa.oauth == nil {
		t.Fatal("OAuth is off")
	}
	on := &atomic.Bool{}
	on.Store(res.Tree.MCP.Enabled)
	d := api.Deps{Cfg: res.Config, Version: "test", Hub: hub.New(), MCPEnabled: on.Load}
	pa.deps(&d)
	srv := httptest.NewServer(api.New(d).Handler())
	t.Cleanup(srv.Close)
	return &e2e{t: t, srv: srv, mcpOn: on}
}

// do sends a request for rawURL (on the public or the direct host) to the listener, as the
// identity proxy would when user is set (the proxy's header, from loopback), and never follows
// a redirect.
func (e *e2e) do(method, rawURL, user string, header map[string]string, body []byte) *http.Response {
	e.t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := http.NewRequest(method, e.srv.URL+u.RequestURI(), bytes.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	r.Host = u.Host
	if user != "" {
		r.Header.Set("X-Forwarded-User", user)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func (e *e2e) json(resp *http.Response, v any) {
	e.t.Helper()
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, v); err != nil {
		e.t.Fatalf("%d %s: %v", resp.StatusCode, b, err)
	}
}

func (e *e2e) form(rawURL string, f url.Values) *http.Response {
	return e.do(http.MethodPost, rawURL, "", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(f.Encode()))
}

// flow runs the authorization code flow with PKCE for the agent at audience, as alice, and
// returns the code exchange's answer (status and body).
func (e *e2e) flow(audience, verifier string, exchangeVerifier string) (*http.Response, url.Values) {
	e.t.Helper()
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {"agent"}, "redirect_uri": {"http://127.0.0.1:53682/callback"},
		"state": {"opaque-state"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {e2eDirect + "/" + audience}, "scope": {"read logs"},
	}
	// The browser opens /oauth/authorize on the public URL, through the identity proxy.
	resp := e.do(http.MethodGet, e2ePublic+"/oauth/authorize?"+q.Encode(), "alice", nil, nil)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, e2ePublic+"/consent/") || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		e.t.Fatalf("authorize = %d %s", resp.StatusCode, loc)
	}
	id := loc[strings.LastIndex(loc, "/")+1:]
	var c model.OAuthConsent
	e.json(e.do(http.MethodGet, e2ePublic+"/api/v1/oauth/consents/"+id, "alice", nil, nil), &c)
	if strings.Join(c.DefaultScopes, " ") != "read" || c.Client.ID != "agent" || c.Audience != audience {
		e.t.Fatalf("consent = %+v", c)
	}
	body, _ := json.Marshal(model.OAuthConsentDecision{Approve: true, Scopes: c.DefaultScopes})
	var res model.OAuthConsentResult
	e.json(e.do(http.MethodPost, e2ePublic+"/api/v1/oauth/consents/"+id, "alice",
		map[string]string{"Origin": e2ePublic, "X-Dash-Csrf": "1", "Content-Type": "application/json"}, body), &res)
	back, _ := url.Parse(res.Redirect)
	if back.Host != "127.0.0.1:53682" || back.Query().Get("state") != "opaque-state" || back.Query().Get("iss") != e2eDirect {
		e.t.Fatalf("redirect = %s", res.Redirect)
	}
	// The client exchanges the code at the direct URL, past the proxy.
	return e.form(e2eDirect+"/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "client_id": {"agent"},
		"redirect_uri": {"http://127.0.0.1:53682/callback"}, "code_verifier": {exchangeVerifier}, "resource": {e2eDirect + "/" + audience},
	}), back.Query()
}

// The full PKCE flow against the built server, behind an identity proxy with a direct URL:
// discovery from the 401, metadata and iss on the direct URL, consent through the proxy, the
// code exchange and refresh on the direct URL. The mcp audience follows mcp.enabled, live: while
// it is on its metadata is served and its flow gives a token, once it is off both are refused; an
// api-audience token works at /api; a wrong verifier gets nothing; a refresh token used twice
// ends the grant.
func TestOAuthEndToEnd(t *testing.T) {
	e := newE2E(t)

	// Discovery: a client without a token learns where the resource metadata is.
	resp := e.do(http.MethodGet, e2eDirect+"/api/v1/me", "", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer resource_metadata="`+e2eDirect+`/.well-known/oauth-protected-resource/api"` {
		t.Fatalf("challenge = %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	var prm oauth.ProtectedResourceMetadata
	e.json(e.do(http.MethodGet, e2eDirect+"/.well-known/oauth-protected-resource/api", "", nil, nil), &prm)
	var asm oauth.AuthorizationServerMetadata
	e.json(e.do(http.MethodGet, e2eDirect+"/.well-known/oauth-authorization-server", "", nil, nil), &asm)
	if prm.Resource != e2eDirect+"/api" || prm.AuthorizationServers[0] != e2eDirect || asm.Issuer != e2eDirect ||
		asm.AuthorizationEndpoint != e2ePublic+"/oauth/authorize" || asm.TokenEndpoint != e2eDirect+"/oauth/token" {
		t.Fatalf("metadata: %+v %+v", prm, asm)
	}

	const verifier = "e2e-verifier-0123456789-abcdefghijklmnopqrstuvwxyz"
	// MCP on: the mcp resource has its metadata, and its flow gives an mcp token.
	var mrm oauth.ProtectedResourceMetadata
	e.json(e.do(http.MethodGet, e2eDirect+"/.well-known/oauth-protected-resource/mcp", "", nil, nil), &mrm)
	if mrm.Resource != e2eDirect+"/mcp" {
		t.Errorf("mcp resource metadata = %+v", mrm)
	}
	resp, _ = e.flow("mcp", verifier, verifier)
	var mcpTok oauth.TokenResponse
	e.json(resp, &mcpTok)
	if resp.StatusCode != http.StatusOK || mcpTok.AccessToken == "" {
		t.Errorf("mcp token = %d %+v", resp.StatusCode, mcpTok)
	}

	// MCP off: the metadata is the JSON 404 and a resource of <issuer>/mcp is invalid_target.
	e.mcpOn.Store(false)
	if r := e.do(http.MethodGet, e2eDirect+"/.well-known/oauth-protected-resource/mcp", "", nil, nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("mcp resource metadata with MCP off = %d", r.StatusCode)
	}
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {"agent"}, "redirect_uri": {"http://127.0.0.1:53682/callback"},
		"state": {"opaque-state"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {e2eDirect + "/mcp"},
	}
	resp = e.do(http.MethodGet, e2ePublic+"/oauth/authorize?"+q.Encode(), "alice", nil, nil)
	if back, err := url.Parse(resp.Header.Get("Location")); resp.StatusCode != http.StatusFound || err != nil || back.Query().Get("error") != "invalid_target" {
		t.Errorf("mcp authorize with MCP off = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, _ = e.flow("api", verifier, verifier)
	var apiTok oauth.TokenResponse
	e.json(resp, &apiTok)
	var me model.Me
	e.json(e.do(http.MethodGet, e2eDirect+"/api/v1/me", "", map[string]string{"Authorization": "Bearer " + apiTok.AccessToken}, nil), &me)
	if me.User != "alice" || me.Audience != "api" || strings.Join(me.Scopes, " ") != "read" {
		t.Errorf("me = %+v", me)
	}

	// A wrong verifier gets no token.
	resp, _ = e.flow("api", verifier, verifier+"x")
	var oe oauth.Error
	e.json(resp, &oe)
	if resp.StatusCode != http.StatusBadRequest || oe.Code != "invalid_grant" {
		t.Errorf("wrong verifier = %d %+v", resp.StatusCode, oe)
	}

	// Refresh rotates; the old refresh token used again ends the grant and its access token.
	refresh := func(rt string) *http.Response {
		return e.form(e2eDirect+"/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}, "client_id": {"agent"}})
	}
	var next oauth.TokenResponse
	e.json(refresh(apiTok.RefreshToken), &next)
	if next.RefreshToken == "" || next.RefreshToken == apiTok.RefreshToken {
		t.Fatalf("refresh = %+v", next)
	}
	if r := refresh(apiTok.RefreshToken); r.StatusCode != http.StatusBadRequest {
		t.Errorf("reuse = %d", r.StatusCode)
	}
	if r := e.do(http.MethodGet, e2eDirect+"/api/v1/me", "", map[string]string{"Authorization": "Bearer " + next.AccessToken}, nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("an access token after reuse = %d", r.StatusCode)
	}
}
