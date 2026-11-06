package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/oauth"
)

// oauthEnv is a proxy-mode server (alice is the one administrator) with the authorization
// server, a real audit log, a plugin with actions and https://inspector.example.com listed in
// corsOrigins.
type oauthEnv struct {
	s        *Server
	auditDir string
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	e := &oauthEnv{}
	e.s, e.auditDir = auditedServer(t, func(d *Deps) {
		d.Cfg.CORSOrigins = []string{"https://inspector.example.com"}
		d.Plugins = withActionPlugin{promPlugin(&fakeProm{})}
		d.OAuth = testOAuth(t, d)
	})
	return e
}

// form posts a form to path with no CSRF header and no session, as an OAuth client does.
func (e *oauthEnv) form(path string, f url.Values, header map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Forwarded-Preferred-Username": ""}
	for k, v := range header {
		h[k] = v
	}
	return req{method: http.MethodPost, path: path, body: f.Encode(), header: h, raw: true}.do(e.s.Handler())
}

// code runs the authorization for user p at audience with scopes (the consent approved
// directly) and returns the authorization code.
func (e *oauthEnv) code(t *testing.T, p auth.Principal, audience string, scopes ...string) string {
	t.Helper()
	w := req{path: authorizeURL(audience, func(q url.Values) { q.Set("scope", strings.Join(scopes, " ")) })}.do(e.s.Handler())
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.Contains(loc, "/consent/") {
		t.Fatalf("authorize = %d %s", w.Code, loc)
	}
	redirect, err := e.s.d.OAuth.Approve(loc[strings.LastIndex(loc, "/")+1:], p, scopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	return u.Query().Get("code")
}

// exchange is the inspector's code exchange form for code.
func exchange(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {"inspector"},
		"redirect_uri": {"http://127.0.0.1:4711/callback"}, "code_verifier": {testVerifier},
	}
}

// grant runs the flow for user p at audience with scopes (the consent approved directly) and
// returns the token response.
func (e *oauthEnv) grant(t *testing.T, p auth.Principal, audience string, scopes ...string) oauth.TokenResponse {
	t.Helper()
	w := e.form("/oauth/token", exchange(e.code(t, p, audience, scopes...)), nil)
	var res oauth.TokenResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &res) != nil {
		t.Fatalf("token = %d %s", w.Code, w.Body)
	}
	return res
}

var aliceAdmin = auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}

// The token endpoint takes a form with no CSRF header and no session, answers no-store, and a
// listed origin may read it (L.7), never with credentials.
func TestOAuthTokenEndpoint(t *testing.T) {
	e := newOAuthEnv(t)
	res := e.grant(t, aliceAdmin, "api", "read")
	w := e.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {res.RefreshToken}, "client_id": {"inspector"}},
		map[string]string{"Origin": "https://inspector.example.com"})
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example.com" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("refresh = %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := e.form("/oauth/token", url.Values{"grant_type": {"password"}, "client_id": {"inspector"}}, nil); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), `"error":"unsupported_grant_type"`) {
		t.Errorf("password grant = %d %s", w.Code, w.Body)
	}
	w = req{method: http.MethodPost, path: "/oauth/token", body: `{"grant_type":"refresh_token"}`, raw: true,
		header: map[string]string{"Content-Type": "application/json"}}.do(e.s.Handler())
	if w.Code != http.StatusBadRequest {
		t.Errorf("JSON body = %d", w.Code)
	}
}

// An api-audience OAuth access token works on /api with its scopes; a refresh token is never a
// bearer credential; revoking the grant ends the access token at once.
func TestOAuthAccessTokenAtTheAPI(t *testing.T) {
	e := newOAuthEnv(t)
	api := e.grant(t, aliceAdmin, "api", "read")
	w := req{path: "/api/v1/me", header: bearer(api.AccessToken)}.do(e.s.Handler())
	me := meOf(t, w)
	if me.User != "alice" || me.Role != "admin" || me.Audience != "api" || strings.Join(me.Scopes, ",") != "read" {
		t.Errorf("me = %+v", me)
	}
	if w := (req{path: "/api/v1/me", header: bearer(api.RefreshToken)}).do(e.s.Handler()); w.Code != http.StatusUnauthorized {
		t.Errorf("refresh token at /api = %d", w.Code)
	}
	// A write needs no CSRF header for an OAuth token principal either.
	if w := (req{method: http.MethodPost, path: "/api/v1/tokens", raw: true, header: bearer(api.AccessToken), body: map[string]any{}}).do(e.s.Handler()); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "csrf") {
		t.Errorf("token route as an OAuth principal = %d %s, want 403 forbidden (interactive only), not csrf", w.Code, w.Body)
	}
	w = e.form("/oauth/revoke", url.Values{"token": {api.AccessToken}, "client_id": {"inspector"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", w.Code, w.Body)
	}
	if w := (req{path: "/api/v1/me", header: bearer(api.AccessToken)}).do(e.s.Handler()); w.Code != http.StatusUnauthorized {
		t.Errorf("after revocation = %d", w.Code)
	}
	if w := e.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {api.RefreshToken}, "client_id": {"inspector"}}, nil); w.Code != http.StatusBadRequest {
		t.Errorf("refresh after revocation = %d", w.Code)
	}
}

// RFC 7009: a refresh token revokes its grant; an unknown token is 200 and changes nothing; the
// revocation is audited first, and an audit outage is 503 with nothing changed.
func TestOAuthRevokeEndpoint(t *testing.T) {
	e := newOAuthEnv(t)
	res := e.grant(t, aliceAdmin, "api", "read")
	if w := e.form("/oauth/revoke", url.Values{"token": {"dash_rt_nope"}}, nil); w.Code != http.StatusOK {
		t.Errorf("unknown token = %d", w.Code)
	}
	if n := len(e.s.d.Credentials.Grants("alice")); n != 1 {
		t.Fatalf("grants = %d", n)
	}

	// The audit log breaks: the revocation is refused and the grant stays.
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	al, err := audit.Open(dir, audit.Options{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	al.Event(audit.Record{Action: "seed"})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	good := e.s.d.Audit
	e.s.d.Audit = al
	if w := e.form("/oauth/revoke", url.Values{"token": {res.RefreshToken}, "client_id": {"inspector"}}, nil); w.Code != http.StatusServiceUnavailable {
		t.Errorf("revoke with the audit log down = %d", w.Code)
	}
	if n := len(e.s.d.Credentials.Grants("alice")); n != 1 {
		t.Fatalf("grants after a refused revocation = %d", n)
	}
	e.s.d.Audit = good

	if w := e.form("/oauth/revoke", url.Values{"token": {res.RefreshToken}, "client_id": {"inspector"}}, nil); w.Code != http.StatusOK {
		t.Fatalf("revoke = %d", w.Code)
	}
	if n := len(e.s.d.Credentials.Grants("alice")); n != 0 {
		t.Errorf("grants after revocation = %d", n)
	}
	var intent, outcome bool
	for _, r := range auditRecords(t, e.auditDir) {
		if r.Action != auditGrantRevoke || r.Actor.User != "alice" || r.Actor.ClientID != "inspector" || r.Target.Kind != "grant" {
			continue
		}
		intent = intent || r.Kind == audit.KindIntent
		outcome = outcome || (r.Kind == audit.KindOutcome && r.Result == audit.ResultOK)
		if strings.Contains(r.Target.Name, "dash_rt_") {
			t.Error("the audit record holds a token")
		}
	}
	if !intent || !outcome {
		t.Errorf("audit: intent %v outcome %v", intent, outcome)
	}
}

// A refresh token used twice revokes its grant, and the audit log says so.
func TestOAuthRefreshReuseIsAudited(t *testing.T) {
	e := newOAuthEnv(t)
	res := e.grant(t, aliceAdmin, "api", "read")
	refresh := func() *httptest.ResponseRecorder {
		return e.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {res.RefreshToken}, "client_id": {"inspector"}}, nil)
	}
	if w := refresh(); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	if w := refresh(); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("reuse = %d %s", w.Code, w.Body)
	}
	if n := len(e.s.d.Credentials.Grants("")); n != 0 {
		t.Errorf("grants after reuse = %d", n)
	}
	found := false
	for _, r := range auditRecords(t, e.auditDir) {
		found = found || (r.Action == auditGrantRevoke && r.Reason == "refresh_reuse" && r.Actor.User == "alice")
	}
	if !found {
		t.Error("no audit record of the reuse revocation")
	}
}

// /oauth/token is limited per client_id and peer: the 61st request in a minute is 429, while
// another client from the same peer, or the same client from another peer, goes on.
func TestOAuthTokenRateLimit(t *testing.T) {
	e := newOAuthEnv(t)
	f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"inspector"}}
	for i := range oauthTokenLimit {
		if w := e.form("/oauth/token", f, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("request %d = %d", i+1, w.Code)
		}
	}
	w := e.form("/oauth/token", f, nil)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("request %d = %d", oauthTokenLimit+1, w.Code)
	}
	other := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"dash-android"}}
	if w := e.form("/oauth/token", other, nil); w.Code != http.StatusBadRequest {
		t.Errorf("another client = %d", w.Code)
	}
	w = req{method: http.MethodPost, path: "/oauth/token", body: f.Encode(), raw: true, peer: "127.0.0.2:5000",
		header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}.do(e.s.Handler())
	if w.Code != http.StatusBadRequest {
		t.Errorf("another peer = %d", w.Code)
	}
}

// The limit's table cannot be filled shut: 4200 requests, each from a new peer with a new
// client_id, leave dash-android's first request unlimited.
func TestOAuthLimitTableNeverRefusesANewKey(t *testing.T) {
	e := newOAuthEnv(t)
	h := e.s.Handler()
	for i := range 4200 {
		f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {fmt.Sprintf("junk-%d", i)}}
		peer := fmt.Sprintf("127.1.%d.%d:5000", i/256, i%256)
		w := req{method: http.MethodPost, path: "/oauth/token", body: f.Encode(), raw: true, peer: peer,
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded", "X-Forwarded-Preferred-Username": ""}}.do(h)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("junk request %d = %d", i+1, w.Code)
		}
	}
	android := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"dash-android"}}
	if w := e.form("/oauth/token", android, nil); w.Code != http.StatusBadRequest {
		t.Errorf("dash-android after the flood = %d %s", w.Code, w.Body)
	}
}

// A flood of junk refreshes under a client's client_id, from the client's own peer, uses up that
// client_id's limit but not the refresh of the client's real grant, which is keyed on the grant.
// Unknown client_ids share the peer's one key.
func TestOAuthJunkRefreshesDoNotStarveARealRefresh(t *testing.T) {
	e := newOAuthEnv(t)
	res := e.grant(t, aliceAdmin, "api", "read")
	junk := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"inspector"}}
	for range oauthTokenLimit {
		e.form("/oauth/token", junk, nil)
	}
	if w := e.form("/oauth/token", junk, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("junk past the limit = %d", w.Code)
	}
	valid := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {res.RefreshToken}, "client_id": {"inspector"}}
	if w := e.form("/oauth/token", valid, nil); w.Code != http.StatusOK {
		t.Errorf("the real refresh = %d %s", w.Code, w.Body)
	}

	for i := range oauthTokenLimit {
		unknown := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {fmt.Sprintf("unknown-%d", i)}}
		if w := e.form("/oauth/token", unknown, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("unknown client %d = %d", i+1, w.Code)
		}
	}
	unknown := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}, "client_id": {"unknown-new"}}
	if w := e.form("/oauth/token", unknown, nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("a new unknown client_id past the peer's limit = %d", w.Code)
	}
}

// A flood of junk codes under a client's client_id, from the client's own peer, uses up that
// client_id's limit but not the exchange of a code dash issued, which is keyed on the code
// (G7-R3 fix E2).
func TestOAuthJunkCodesDoNotStarveARealExchange(t *testing.T) {
	e := newOAuthEnv(t)
	code := e.code(t, aliceAdmin, "api", "read")
	for i := range oauthTokenLimit {
		if w := e.form("/oauth/token", exchange(fmt.Sprintf("junk-%d", i)), nil); w.Code != http.StatusBadRequest {
			t.Fatalf("junk code %d = %d %s", i+1, w.Code, w.Body)
		}
	}
	if w := e.form("/oauth/token", exchange("junk-past"), nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("junk past the limit = %d", w.Code)
	}
	if w := e.form("/oauth/token", exchange(code), nil); w.Code != http.StatusOK {
		t.Errorf("the real exchange = %d %s", w.Code, w.Body)
	}
}
