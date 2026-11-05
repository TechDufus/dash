package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/tokens"
)

// tokenEnv is a server in the proxy mode (alice is the one administrator; the other users are
// viewers) with a credential store in a data dir, its token resolver, and a real audit log.
type tokenEnv struct {
	s        *Server
	store    *credentials.Store
	dataDir  string
	auditDir string
}

func newTokenEnv(t *testing.T, mods ...func(*Deps)) *tokenEnv {
	t.Helper()
	e := &tokenEnv{dataDir: t.TempDir()}
	store, err := credentials.Open(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.store = store
	roles := auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice"}})
	e.s, e.auditDir = auditedServer(t, func(d *Deps) {
		d.Credentials, d.Tokens = store, tokens.New(store, roles, nil, func() time.Time { return fixedNow })
		d.Plugins = withActionPlugin{promPlugin(&fakeProm{})}
		for _, m := range mods {
			m(d)
		}
	})
	return e
}

// withActionPlugin adds the plugin "aa" with a mounted action to the list, so an actions:aa
// scope names a plugin whose actions are enabled.
type withActionPlugin struct{ PluginsAPI }

func (p withActionPlugin) Infos() []model.PluginInfo {
	return append(p.PluginsAPI.Infos(), model.PluginInfo{ID: "aa", Actions: []model.ActionInfo{{Name: "ping", Title: "Ping", MinRole: "admin"}}})
}

// as is a request by user through the identity proxy; for a write the SPA's CSRF headers are set.
func as(user string, r req) req {
	h := map[string]string{"X-Forwarded-Preferred-Username": user}
	for k, v := range r.header {
		h[k] = v
	}
	r.header = h
	return r
}

func (e *tokenEnv) do(r req) *httptest.ResponseRecorder { return r.do(e.s.Handler()) }

func (e *tokenEnv) create(t *testing.T, user string, body any) (*httptest.ResponseRecorder, model.TokenCreated) {
	t.Helper()
	w := e.do(as(user, req{method: http.MethodPost, path: "/api/v1/tokens", body: body}))
	var out model.TokenCreated
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return w, out
}

func (e *tokenEnv) list(t *testing.T, user, query string) []model.TokenInfo {
	t.Helper()
	w := e.do(as(user, req{path: "/api/v1/tokens" + query}))
	if w.Code != http.StatusOK {
		t.Fatalf("list as %s%s = %d %s", user, query, w.Code, w.Body)
	}
	var l model.TokenList
	decodeJSON(t, w, &l)
	return l.Tokens
}

var patRE = regexp.MustCompile(`^dash_pat_[a-z2-7]{10}_[A-Za-z0-9_-]{43}$`)

// The whole life of a token: created once (the secret in that answer only), listed, used,
// revoked, refused on the next request; credentials.yaml holds its hash, 0600, no backup; the
// audit log has intent and outcome of both writes and never the secret.
func TestTokenLifecycle(t *testing.T) {
	e := newTokenEnv(t)
	w, created := e.create(t, "bob", model.TokenCreateRequest{Name: "laptop", Audience: "api", Scopes: []string{"read", "logs"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	info := created.Info
	if !patRE.MatchString(created.Token) || info.User != "bob" || info.Name != "laptop" || info.Audience != "api" ||
		!slices.Equal(info.Scopes, []string{"read", "logs"}) || info.LastUsedAt != nil || !info.ExpiresAt.Equal(info.CreatedAt.AddDate(0, 0, 30)) ||
		!strings.Contains(created.Token, info.ID) {
		t.Fatalf("created = %+v", created)
	}
	secret := created.Token[len(created.Token)-43:]

	if me := meOf(t, req{path: "/api/v1/me", header: bearer(created.Token)}.do(e.s.Handler())); me.User != "bob" {
		t.Errorf("the token signs in as %q", me.User)
	}
	l := e.list(t, "bob", "")
	if len(l) != 1 || l[0].ID != info.ID || l[0].LastUsedAt == nil {
		t.Errorf("list = %+v, want the token with its last use", l)
	}
	raw := e.do(as("bob", req{path: "/api/v1/tokens"})).Body.String()
	if strings.Contains(raw, secret) {
		t.Error("the list shows the secret")
	}

	data, err := os.ReadFile(filepath.Join(e.dataDir, credentials.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), credentials.HashToken(created.Token)) {
		t.Errorf("credentials.yaml:\n%s", data)
	}
	fi, _ := os.Stat(filepath.Join(e.dataDir, credentials.FileName))
	entries, _ := os.ReadDir(e.dataDir)
	if fi.Mode().Perm() != 0o600 || len(entries) != 2 {
		t.Errorf("mode %v, data dir %v: want 0600 and only the file and its lock", fi.Mode().Perm(), entries)
	}

	if w := e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/tokens/" + info.ID})); w.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", w.Code, w.Body)
	}
	if w := (req{path: "/api/v1/me", header: bearer(created.Token)}).do(e.s.Handler()); w.Code != http.StatusUnauthorized {
		t.Errorf("after the revocation: %d, want 401", w.Code)
	}
	if l := e.list(t, "bob", ""); len(l) != 0 {
		t.Errorf("list after the revocation = %+v", l)
	}

	recs := auditRecords(t, e.auditDir)
	var kinds []string
	for _, r := range recs {
		if r.Action == auditTokenCreate || r.Action == auditTokenRevoke {
			kinds = append(kinds, r.Action+"/"+string(r.Kind))
			if r.Target != (audit.Target{Kind: "token", Namespace: "bob", Name: info.ID}) || r.Actor.User != "bob" {
				t.Errorf("record %+v", r)
			}
		}
	}
	if !slices.Equal(kinds, []string{"token.create/intent", "token.create/outcome", "token.revoke/intent", "token.revoke/outcome"}) {
		t.Errorf("audit = %v", kinds)
	}
	b, _ := os.ReadFile(filepath.Join(e.auditDir, audit.FileName))
	if strings.Contains(string(b), secret) {
		t.Error("the audit file holds the secret")
	}
}

// A viewer cannot mint actions:*, and nobody can mint approvals or push: 403 and nothing stored.
// An administrator can mint actions:<plugin>.
func TestTokenScopesAPersonMayMint(t *testing.T) {
	e := newTokenEnv(t)
	for _, tc := range []struct {
		user   string
		scopes []string
	}{
		{"bob", []string{"read", "actions:aa"}},
		{"bob", []string{"approvals"}},
		{"alice", []string{"read", "push"}},
		{"alice", []string{"approvals"}},
	} {
		w, _ := e.create(t, tc.user, model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: tc.scopes})
		wantError(t, w, http.StatusForbidden, model.ErrCodeForbidden)
	}
	if n := len(e.store.PATs("")); n != 0 {
		t.Fatalf("%d tokens were stored by refused requests", n)
	}
	if w, c := e.create(t, "alice", model.TokenCreateRequest{Name: "ci", Audience: "api", Scopes: []string{"actions:aa"}, ActionNamespaces: []string{"apps", "web-*"}}); w.Code != http.StatusCreated ||
		!slices.Equal(c.Info.ActionNamespaces, []string{"apps", "web-*"}) {
		t.Errorf("admin actions token = %d %s", w.Code, w.Body)
	}
}

// Bad input is 400 with a field error per key of the request, never repeating a value.
func TestTokenRequestFieldErrors(t *testing.T) {
	e := newTokenEnv(t)
	long := strings.Repeat("s3cret-", 10)
	days := func(n int) *int { return &n }
	for _, tc := range []struct {
		body model.TokenCreateRequest
		path string
	}{
		{model.TokenCreateRequest{Name: "", Audience: "api", Scopes: []string{"read"}}, "name"},
		{model.TokenCreateRequest{Name: long + long, Audience: "api", Scopes: []string{"read"}}, "name"},
		{model.TokenCreateRequest{Name: "ci\u202etxt.exe", Audience: "api", Scopes: []string{"read"}}, "name"},
		{model.TokenCreateRequest{Name: "\u200b", Audience: "api", Scopes: []string{"read"}}, "name"},
		{model.TokenCreateRequest{Name: "\ue000\ue001", Audience: "api", Scopes: []string{"read"}}, "name"},
		{model.TokenCreateRequest{Name: "x", Audience: "s3cret-web", Scopes: []string{"read"}}, "audience"},
		{model.TokenCreateRequest{Name: "x", Audience: "api"}, "scopes"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"s3cret-write"}}, "scopes"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read", "read"}}, "scopes"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"actions:nosuch"}}, "scopes"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}, ActionNamespaces: []string{"apps"}}, "actionNamespaces"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"actions:aa"}, ActionNamespaces: []string{"S3cret/ns"}}, "actionNamespaces"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}, ExpiresInDays: days(366)}, "expiresInDays"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}, ExpiresInDays: days(-1)}, "expiresInDays"},
		{model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}, ExpiresInDays: days(0)}, "expiresInDays"},
	} {
		w, _ := e.create(t, "alice", tc.body)
		got := wantError(t, w, http.StatusBadRequest, model.ErrCodeInvalidInput)
		b, _ := json.Marshal(got.Details)
		var d model.SettingsErrorDetails
		_ = json.Unmarshal(b, &d)
		if len(d.Fields) == 0 || d.Fields[0].Path != tc.path {
			t.Errorf("%+v: details %s, want a field error at %s", tc.body, b, tc.path)
		}
		if strings.Contains(w.Body.String(), "s3cret") || strings.Contains(w.Body.String(), "S3cret") || strings.Contains(w.Body.String(), "nosuch") {
			t.Errorf("the answer repeats a value: %s", w.Body)
		}
	}
	for _, ok := range []model.TokenCreateRequest{
		{Name: "x", Audience: "mcp", Scopes: []string{"read"}, ExpiresInDays: days(365)},
		{Name: "laptop · Grüße", Audience: "api", Scopes: []string{"actions:aa"}, ExpiresInDays: days(1)},
	} {
		if w, c := e.create(t, "alice", ok); w.Code != http.StatusCreated ||
			!c.Info.ExpiresAt.Equal(c.Info.CreatedAt.AddDate(0, 0, *ok.ExpiresInDays)) {
			t.Errorf("%+v = %d %s", ok, w.Code, w.Body)
		}
	}
}

// The token routes are for people: a bearer token gets 403 on each, whatever its scopes; and
// a write needs the CSRF header like every cookie or proxy write.
func TestTokenRoutesRefuseBearerAndNeedCSRF(t *testing.T) {
	e := newTokenEnv(t)
	_, c := e.create(t, "alice", model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read", "logs", "actions:aa"}})
	for _, r := range []req{
		{path: "/api/v1/tokens"},
		{method: http.MethodPost, path: "/api/v1/tokens", body: model.TokenCreateRequest{Name: "y", Audience: "api", Scopes: []string{"read"}}},
		{method: http.MethodDelete, path: "/api/v1/tokens/" + c.Info.ID},
	} {
		r.header = bearer(c.Token)
		wantError(t, e.do(r), http.StatusForbidden, model.ErrCodeForbidden)
	}
	w := e.do(as("alice", req{method: http.MethodPost, path: "/api/v1/tokens", raw: true, body: model.TokenCreateRequest{Name: "y", Audience: "api", Scopes: []string{"read"}}}))
	wantError(t, w, http.StatusForbidden, model.ErrCodeCSRFFailed)
	if n := len(e.store.PATs("")); n != 1 {
		t.Errorf("tokens = %d, want only the first", n)
	}
}

// A viewer sees and revokes only their own tokens (another's is 404 and keeps working); an
// administrator lists one user's or everyone's and revokes anyone's.
func TestTokenOwnership(t *testing.T) {
	e := newTokenEnv(t)
	_, alices := e.create(t, "alice", model.TokenCreateRequest{Name: "a", Audience: "api", Scopes: []string{"read"}})
	_, bobs := e.create(t, "bob", model.TokenCreateRequest{Name: "b", Audience: "api", Scopes: []string{"read"}})
	wantError(t, e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/tokens/" + alices.Info.ID})), http.StatusNotFound, model.ErrCodeNotFound)
	wantError(t, e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/tokens/nosuchtokn"})), http.StatusNotFound, model.ErrCodeNotFound)
	if w := (req{path: "/api/v1/me", header: bearer(alices.Token)}).do(e.s.Handler()); w.Code != http.StatusOK {
		t.Errorf("alice's token after bob's attempt = %d", w.Code)
	}
	wantError(t, e.do(as("carol", req{path: "/api/v1/tokens?user=bob"})), http.StatusForbidden, model.ErrCodeForbidden)
	if l := e.list(t, "bob", "?user=bob"); len(l) != 1 || l[0].ID != bobs.Info.ID {
		t.Errorf("bob's own list = %+v", l)
	}
	if l := e.list(t, "carol", ""); len(l) != 0 {
		t.Errorf("carol sees %+v", l)
	}
	if l := e.list(t, "alice", "?user=*"); len(l) != 2 {
		t.Errorf("admin ?user=* = %+v", l)
	}
	if l := e.list(t, "alice", "?user="); len(l) != 1 || l[0].User != "alice" {
		t.Errorf("admin ?user= (empty) = %+v, want her own", l)
	}
	if l := e.list(t, "alice", "?user=bob"); len(l) != 1 || l[0].User != "bob" {
		t.Errorf("admin ?user=bob = %+v", l)
	}
	if w := e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/tokens/" + bobs.Info.ID})); w.Code != http.StatusNoContent {
		t.Errorf("admin revokes bob's = %d", w.Code)
	}
}

// Where nothing keeps a token, the routes say so (contract D1): 409 tokens_unavailable without a
// writable data dir in local, oidc and proxy mode; 404 in auth.mode=none; dev and demo keep
// tokens in memory.
func TestTokensUnavailableWithoutPersistence(t *testing.T) {
	memory := func(dev bool) func(*Deps) {
		return func(d *Deps) {
			s, err := credentials.OpenMemory()
			if err != nil {
				t.Fatal(err)
			}
			d.Credentials, d.Tokens, d.Cfg.Dev = s, tokens.New(s, auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice"}}), nil, nil), dev
		}
	}
	body := model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}}
	e := newTokenEnv(t, memory(false))
	for _, r := range []req{{path: "/api/v1/tokens"}, {method: http.MethodPost, path: "/api/v1/tokens", body: body}, {method: http.MethodDelete, path: "/api/v1/tokens/abcdefghij"}} {
		wantError(t, e.do(r), http.StatusConflict, model.ErrCodeTokensUnavailable)
	}
	none := newTokenEnv(t, func(d *Deps) { d.Credentials, d.Tokens = nil, nil })
	wantError(t, none.do(req{path: "/api/v1/tokens"}), http.StatusNotFound, model.ErrCodeNotFound)
	dev := newTokenEnv(t, memory(true))
	if w, _ := dev.create(t, "alice", body); w.Code != http.StatusCreated {
		t.Errorf("dev mode = %d %s", w.Code, w.Body)
	}
}

// An audit log that cannot take the intent stops the write: 503 audit_unavailable, no token
// created, a token's revocation not applied (contract D6).
func TestTokenWritesFailClosedOnTheAudit(t *testing.T) {
	e := newTokenEnv(t)
	_, c := e.create(t, "alice", model.TokenCreateRequest{Name: "keep", Audience: "api", Scopes: []string{"read"}})
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
	e.s.d.Audit = al
	before, _ := os.ReadFile(filepath.Join(e.dataDir, credentials.FileName))

	w, _ := e.create(t, "alice", model.TokenCreateRequest{Name: "new", Audience: "api", Scopes: []string{"read"}})
	wantError(t, w, http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	wantError(t, e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/tokens/" + c.Info.ID})), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	if l := e.store.PATs(""); len(l) != 1 || l[0].ID != c.Info.ID {
		t.Errorf("tokens = %+v, want only the first", l)
	}
	if w := (req{path: "/api/v1/me", header: bearer(c.Token)}).do(e.s.Handler()); w.Code != http.StatusOK {
		t.Errorf("the token after a refused revocation = %d", w.Code)
	}
	after, _ := os.ReadFile(filepath.Join(e.dataDir, credentials.FileName))
	if string(before) != string(after) {
		t.Error("credentials.yaml changed")
	}
}

// CORS (design L.7): no Access-Control-* header without server.corsOrigins; a listed origin's
// preflight gets the allow headers, a bearer request and a 401 the allow-origin and the exposed
// headers; an unlisted origin and a same-origin request get nothing; never Allow-Credentials.
func TestCORS(t *testing.T) {
	const listed, other = "https://inspector.example.com", "https://evil.example.net"
	e := newTokenEnv(t, func(d *Deps) { d.Cfg.CORSOrigins = []string{listed} })
	_, c := e.create(t, "alice", model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}})
	acHeaders := func(w *httptest.ResponseRecorder) map[string]string {
		out := map[string]string{}
		for k, v := range w.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				out[k] = strings.Join(v, ",")
			}
		}
		return out
	}
	preflight := func(origin string) *httptest.ResponseRecorder {
		return req{method: http.MethodOptions, path: "/api/v1/me", raw: true, header: map[string]string{
			"Origin": origin, "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization", "X-Forwarded-Preferred-Username": "",
		}}.do(e.s.Handler())
	}

	w := preflight(listed)
	want := map[string]string{
		"Access-Control-Allow-Origin":  listed,
		"Access-Control-Allow-Methods": "GET, POST, DELETE",
		"Access-Control-Allow-Headers": "authorization, content-type, if-none-match, mcp-protocol-version, mcp-method, mcp-name",
		"Access-Control-Max-Age":       "600",
	}
	if got := acHeaders(w); w.Code != http.StatusNoContent || !mapsEqual(got, want) || w.Header().Get("Vary") != "Origin" {
		t.Errorf("listed preflight = %d %v vary %q", w.Code, got, w.Header().Get("Vary"))
	}
	if got := acHeaders(preflight(other)); len(got) != 0 {
		t.Errorf("unlisted preflight got %v", got)
	}

	exposed := map[string]string{"Access-Control-Allow-Origin": listed, "Access-Control-Expose-Headers": "ETag, X-Request-Id, WWW-Authenticate"}
	h := bearer(c.Token)
	h["Origin"] = listed
	if w := (req{path: "/api/v1/me", header: h}).do(e.s.Handler()); w.Code != http.StatusOK || !mapsEqual(acHeaders(w), exposed) {
		t.Errorf("bearer from the listed origin = %d %v", w.Code, acHeaders(w))
	}
	unauth := map[string]string{"Origin": listed, "X-Forwarded-Preferred-Username": ""}
	if w := (req{path: "/api/v1/me", peer: "192.0.2.1:1", header: unauth}).do(e.s.Handler()); w.Code != http.StatusUnauthorized || !mapsEqual(acHeaders(w), exposed) {
		t.Errorf("401 to the listed origin = %d %v", w.Code, acHeaders(w))
	}
	if w := e.do(as("alice", req{path: "/api/v1/me", header: map[string]string{"Origin": listed}})); len(acHeaders(w)) != 0 {
		t.Errorf("an identity-proxy request from the listed origin got %v", acHeaders(w))
	}
	h["Origin"] = other
	if w := (req{path: "/api/v1/me", header: h}).do(e.s.Handler()); len(acHeaders(w)) != 0 {
		t.Errorf("bearer from an unlisted origin got %v", acHeaders(w))
	}

	plain := newTokenEnv(t)
	if w := (req{method: http.MethodOptions, path: "/api/v1/me", raw: true, header: map[string]string{"Origin": listed, "Access-Control-Request-Method": "GET"}}).do(plain.s.Handler()); len(acHeaders(w)) != 0 {
		t.Errorf("without corsOrigins: %v", acHeaders(w))
	}

	// The decision is the resolved principal's: a token refused by a route still gets the headers
	// (its client reads the 403), and a Bearer header that resolves nothing gets none.
	h["Origin"] = listed
	if w := (req{path: "/api/v1/settings", header: h}).do(e.s.Handler()); w.Code != http.StatusForbidden || !mapsEqual(acHeaders(w), exposed) {
		t.Errorf("a token's 403 from the listed origin = %d %v", w.Code, acHeaders(w))
	}
	none := newTokenEnv(t, func(d *Deps) {
		d.Cfg.CORSOrigins = []string{listed}
		d.Credentials, d.Tokens = nil, nil
		d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
	})
	for _, r := range []req{
		{path: "/api/v1/me", header: map[string]string{"Origin": listed, "Authorization": "Bearer x"}},
		{method: http.MethodOptions, path: "/api/v1/me", raw: true, header: map[string]string{"Origin": listed, "Access-Control-Request-Method": "GET"}},
	} {
		if w := r.do(none.s.Handler()); len(acHeaders(w)) != 0 {
			t.Errorf("auth.mode=none, %s with a Bearer header: %d %v; want no CORS at all", r.method, w.Code, acHeaders(w))
		}
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// The OpenAPI document has the bearerAuth scheme and documents every token route as for people.
func TestOpenAPIDocumentsBearerAndTheTokenRoutes(t *testing.T) {
	ms, routes := docManifests()
	doc := openAPIDoc(t, ms, routes)
	if got := dig(t, doc, "components", "securitySchemes", "bearerAuth", "scheme"); got != "bearer" {
		t.Errorf("bearerAuth scheme = %v", got)
	}
	for path, methods := range map[string][]string{"/api/v1/tokens": {"get", "post"}, "/api/v1/tokens/{id}": {"delete"}} {
		for _, m := range methods {
			if dig(t, doc, "paths", path, m, "x-dash-interactive") != true {
				t.Errorf("%s %s is not marked interactive", m, path)
			}
		}
	}
}

// A user holds at most 50 unexpired tokens: the next create is 409 token_limit and stores nothing,
// before any audit record; another user may still create.
func TestTokenLimitPerUser(t *testing.T) {
	e := newTokenEnv(t)
	for range credentials.MaxActivePATs {
		_, id, hash, err := credentials.NewPAT()
		if err != nil {
			t.Fatal(err)
		}
		if err := e.store.AddPAT(credentials.PAT{ID: id, Name: "t", User: "bob", Hash: hash, Audience: "api", Scopes: []string{"read"},
			CreatedAt: fixedNow, ExpiresAt: fixedNow.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	body := model.TokenCreateRequest{Name: "one more", Audience: "api", Scopes: []string{"read"}}
	w, _ := e.create(t, "bob", body)
	wantError(t, w, http.StatusConflict, model.ErrCodeTokenLimit)
	if n := len(e.store.PATs("bob")); n != credentials.MaxActivePATs {
		t.Errorf("bob holds %d tokens", n)
	}
	for _, r := range auditRecords(t, e.auditDir) {
		if r.Action == auditTokenCreate {
			t.Errorf("a refused create was audited: %+v", r)
		}
	}
	if w, _ := e.create(t, "carol", body); w.Code != http.StatusCreated {
		t.Errorf("another user = %d", w.Code)
	}
}

// A user creates at most 10 tokens a minute: the 11th is 429 with Retry-After; a refused request
// does not count, and another user is not affected.
func TestTokenCreationIsRateLimited(t *testing.T) {
	e := newTokenEnv(t)
	ok := model.TokenCreateRequest{Name: "t", Audience: "api", Scopes: []string{"read"}}
	if w, _ := e.create(t, "bob", model.TokenCreateRequest{Name: "", Audience: "api", Scopes: []string{"read"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad request = %d", w.Code)
	}
	for i := range tokenCreateLimit {
		if w, _ := e.create(t, "bob", ok); w.Code != http.StatusCreated {
			t.Fatalf("create %d = %d %s", i+1, w.Code, w.Body)
		}
	}
	w, _ := e.create(t, "bob", ok)
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if n := len(e.store.PATs("bob")); n != tokenCreateLimit {
		t.Errorf("bob holds %d tokens", n)
	}
	if w, _ := e.create(t, "carol", ok); w.Code != http.StatusCreated {
		t.Errorf("another user = %d", w.Code)
	}
}

// A revocation that cannot reach credentials.yaml (an unwritable data dir, or a file replaced
// while dash runs) answers 500 and says so, and the token is refused from then on anyway: a
// restart would bring it back, which a 204 would hide.
func TestRevokeThatCannotBePersistedIs500(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(t *testing.T, dir string)
	}{
		{"unwritable data dir", func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root ignores directory permissions")
			}
			if err := os.Chmod(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(dir, 0o700) })
		}},
		{"credentials.yaml replaced", func(t *testing.T, dir string) {
			path := filepath.Join(dir, credentials.FileName)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTokenEnv(t)
			_, c := e.create(t, "alice", model.TokenCreateRequest{Name: "x", Audience: "api", Scopes: []string{"read"}})
			tc.spoil(t, e.dataDir)
			w := e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/tokens/" + c.Info.ID}))
			got := wantError(t, w, http.StatusInternalServerError, model.ErrCodeInternal)
			if !strings.Contains(got.Error, "until dash restarts") {
				t.Errorf("message = %q", got.Error)
			}
			if w := (req{path: "/api/v1/me", header: bearer(c.Token)}).do(e.s.Handler()); w.Code != http.StatusUnauthorized {
				t.Errorf("the token after the failed revocation = %d, want 401", w.Code)
			}
		})
	}
}
