package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// The tests of this file drive the local mode through the whole chain, s.Handler(): the route
// rows, the static gate, authn, csrf and the security headers. The handlers themselves are
// tested in authroutes_local_test.go.

const chainAdmin, chainPassword = "admin", "correct horse battery"

// chainPHC is the stub hasher's "hash": fast, and never the password itself.
func chainPHC(password string) string {
	sum := sha256.Sum256([]byte(password))
	return "stub$" + hex.EncodeToString(sum[:])
}

// localChain is a local-mode server: an in-memory store, the account chainAdmin, a stub hasher,
// a setup token when setup is true, the anonymous viewer when anonymous is true, a real audit log
// in dir, and a stub SPA.
type localChain struct {
	*Server
	sess  *sessionHarness
	local *localauth.Local
	dir   string
}

func newLocalChain(t *testing.T, setup, anonymous bool, envHash string) *localChain {
	t.Helper()
	opt, sh := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{chainAdmin}}, anonymous, nil)
	c := &localChain{sess: sh, dir: t.TempDir()}
	c.local = &localauth.Local{
		Account: localauth.Account{User: chainAdmin, EnvHash: envHash, Store: sh.Store},
		Hasher: localauth.NewHasher(localauth.HasherOptions{
			Hash:   func(pw string) (string, error) { return chainPHC(pw), nil },
			Verify: func(phc, pw string) (bool, error) { return phc == chainPHC(pw), nil },
		}),
		Throttle: localauth.NewThrottle(chainAdmin, nil),
	}
	if setup {
		tok, err := localauth.NewSetupToken(nil)
		if err != nil {
			t.Fatal(err)
		}
		c.local.Setup = tok
	}
	// As cmd/dash wires it: the anonymous viewer waits for the account.
	sh.Auth = session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeLocal, Sessions: sh.Sessions, Roles: sh.Roles, Anonymous: anonymous, SetupNeeded: c.local.SetupNeeded})
	c.Server = newServer(func(d *Deps) {
		opt(d)
		d.Local = c.local
		d.Audit = openAudit(t, c.dir, newAuditClock().now)
	})
	c.static = fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}}
	return c
}

// signIn creates the account through /setup and returns its session cookie.
func (c *localChain) signIn(t *testing.T) *http.Cookie {
	t.Helper()
	w := req{method: http.MethodPost, path: "/setup", body: map[string]string{"token": c.local.Setup.Value(), "password": chainPassword}}.do(c.Handler())
	if w.Code != http.StatusOK {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	for _, ck := range w.Result().Cookies() {
		if ck.Name == session.CookiePlain && ck.Value != "" {
			return ck
		}
	}
	t.Fatal("setup set no session cookie")
	return nil
}

// The sign-in routes are outside /api, where authn does not run, but csrf, the host check and
// the security headers do: a POST to any of them without the CSRF header or with a missing or
// foreign Origin is 403 csrf_failed and never reaches the handler, and every answer is no-store
// and nosniff.
func TestLoginPostsRequireAValidOrigin(t *testing.T) {
	c := newLocalChain(t, true, false, "")
	h := c.Handler()
	bodies := map[string]any{
		"/login":       map[string]string{"username": chainAdmin, "password": "wrong password!"},
		"/setup":       map[string]string{"token": "dash_setup_wrong", "password": chainPassword},
		"/auth/logout": map[string]string{"scope": "this"},
	}
	secured := func(t *testing.T, what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
			t.Errorf("%s: Cache-Control %q, want no-store", what, cc)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: security headers missing: %v", what, w.Header())
		}
	}
	for path, body := range bodies {
		for name, hd := range map[string]map[string]string{
			"no Origin":        {"Origin": ""},
			"foreign Origin":   {"Origin": "https://evil.example"},
			"no X-Dash-Csrf":   {"X-Dash-Csrf": ""},
			"no CSRF, no Orig": {"Origin": "", "X-Dash-Csrf": ""},
		} {
			w := req{method: http.MethodPost, path: path, body: body, header: hd}.do(h)
			wantError(t, w, http.StatusForbidden, model.ErrCodeCSRFFailed)
			secured(t, path+" "+name, w)
		}
	}
	// With both, each request reaches its handler, which refuses on its own terms.
	for path, want := range map[string]struct {
		status int
		code   string
	}{
		"/login":       {http.StatusUnauthorized, model.ErrCodeUnauthenticated},
		"/setup":       {http.StatusUnauthorized, model.ErrCodeInvalidToken},
		"/auth/logout": {http.StatusNoContent, ""},
	} {
		w := req{method: http.MethodPost, path: path, body: bodies[path]}.do(h)
		if want.code != "" {
			wantError(t, w, want.status, want.code)
		} else if w.Code != want.status {
			t.Errorf("%s = %d, want %d", path, w.Code, want.status)
		}
		secured(t, path, w)
	}
	csrf := 0
	for _, r := range auditRecords(t, c.dir) {
		if r.Kind == audit.KindDenial && r.Reason == obs.AuthCSRF.String() {
			csrf++
		}
	}
	if csrf == 0 {
		t.Error("no csrf denial was audited")
	}
}

// /setup (the page and the POST) exists only while setup is needed: before the account exists in
// the local mode. Once setup made it, or when the environment supplies the hash, or in another
// mode, both are 404.
func TestSetupIs404OnceAnAdminExists(t *testing.T) {
	c := newLocalChain(t, true, false, "")
	h := c.Handler()
	if w := (req{path: "/setup"}).do(h); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "spa") {
		t.Fatalf("GET /setup while open = %d %q, want the SPA", w.Code, w.Body)
	}
	c.signIn(t)
	if w := (req{path: "/setup"}).do(h); w.Code != http.StatusNotFound {
		t.Errorf("GET /setup after setup = %d, want 404", w.Code)
	}
	w := req{method: http.MethodPost, path: "/setup", body: map[string]string{"token": c.local.Setup.Value(), "password": chainPassword}}.do(h)
	wantError(t, w, http.StatusNotFound, model.ErrCodeNotFound)

	env := newLocalChain(t, false, false, chainPHC(chainPassword))
	for _, r := range []req{{path: "/setup"}, {path: "/setup/again"}, {method: http.MethodPost, path: "/setup", body: map[string]string{"token": "dash_setup_x", "password": chainPassword}}} {
		if w := r.do(env.Handler()); w.Code != http.StatusNotFound {
			t.Errorf("env-managed %s %s = %d, want 404", r.method, r.path, w.Code)
		}
	}
	proxy := newServer(nil)
	proxy.static = fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}}
	if w := (req{path: "/setup"}).do(proxy.Handler()); w.Code != http.StatusNotFound {
		t.Errorf("GET /setup in proxy mode = %d, want 404", w.Code)
	}
	if w := (req{path: "/login"}).do(proxy.Handler()); w.Code != http.StatusOK {
		t.Errorf("GET /login (the SPA) = %d, want 200", w.Code)
	}
}

// authDetailsOf is the details of a 401 unauthenticated.
func authDetailsOf(t *testing.T, w *httptest.ResponseRecorder) model.AuthDetails {
	t.Helper()
	var e struct {
		Code    string            `json:"code"`
		Details model.AuthDetails `json:"details"`
	}
	if w.Code != http.StatusUnauthorized || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Code != model.ErrCodeUnauthenticated {
		t.Fatalf("status %d, body %s; want 401 unauthenticated", w.Code, w.Body)
	}
	return e.Details
}

// A 401 unauthenticated tells the SPA how to sign in: the mode, and in the local mode whether
// setup is still open. Another refusal (a bearer token) carries no details.
func TestUnauthenticatedCarriesTheModeDetails(t *testing.T) {
	noUser := map[string]string{"X-Forwarded-Preferred-Username": ""}
	if d := authDetailsOf(t, req{path: "/api/v1/me", header: noUser}.do(newServer(nil).Handler())); d != (model.AuthDetails{Mode: "proxy"}) {
		t.Errorf("proxy details = %+v", d)
	}
	c := newLocalChain(t, true, false, "")
	if d := authDetailsOf(t, req{path: "/api/v1/me"}.do(c.Handler())); d != (model.AuthDetails{Mode: "local", Setup: true}) {
		t.Errorf("local before setup = %+v, want setup true", d)
	}
	c.signIn(t)
	if d := authDetailsOf(t, req{path: "/api/v1/me"}.do(c.Handler())); d != (model.AuthDetails{Mode: "local"}) {
		t.Errorf("local after setup = %+v, want setup false", d)
	}
	w := req{path: "/api/v1/me", header: map[string]string{"Authorization": "Bearer x"}}.do(c.Handler())
	if e := wantError(t, w, http.StatusUnauthorized, model.ErrCodeInvalidToken); e.Details != nil {
		t.Errorf("invalid_token details = %v, want none", e.Details)
	}
}

// anonymous_viewer is a warning for administrators: the signed-in admin sees it, the anonymous
// viewer itself does not.
func TestMeWarnsAdminsOfTheAnonymousViewer(t *testing.T) {
	c := newLocalChain(t, true, true, "")
	ck := c.signIn(t)
	var me model.Me
	w := req{path: "/api/v1/me", header: map[string]string{"Cookie": ck.Name + "=" + ck.Value}}.do(c.Handler())
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || me.Role != "admin" || me.AuthMode != "local" || !slices.Contains(me.Warnings, auth.WarningAnonymousViewer) {
		t.Errorf("admin me = %d %s, want anonymous_viewer", w.Code, w.Body)
	}
	me = model.Me{}
	w = req{path: "/api/v1/me"}.do(c.Handler())
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || me.User != "anonymous" || me.Role != "viewer" || me.AuthMode != "local" || len(me.Warnings) != 0 {
		t.Errorf("anonymous me = %d %s, want a viewer without warnings", w.Code, w.Body)
	}
}

// The anonymous viewer is never told it may read logs (every log route refuses it): its scopes
// are read only. A signed-in viewer or admin keeps logs.
func TestAnonymousViewerHasNoLogsScope(t *testing.T) {
	c := newLocalChain(t, true, true, "")
	ck := c.signIn(t)
	var me model.Me
	w := req{path: "/api/v1/me"}.do(c.Handler())
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || me.User != "anonymous" || !slices.Equal(me.Scopes, []string{"read"}) {
		t.Errorf("anonymous me = %d %s, want scopes [read]", w.Code, w.Body)
	}
	me = model.Me{}
	w = req{path: "/api/v1/me", header: map[string]string{"Cookie": ck.Name + "=" + ck.Value}}.do(c.Handler())
	if json.Unmarshal(w.Body.Bytes(), &me) != nil || !slices.Contains(me.Scopes, "logs") {
		t.Errorf("admin me = %d %s, want logs among the scopes", w.Code, w.Body)
	}
}

// With the anonymous viewer on, a visitor before setup is not the viewer: /api answers 401 with
// details.setup, so the SPA points to the setup link rather than to a login nobody can use yet.
// Once the account exists, the visitor reads as the anonymous viewer.
func TestAnonymousViewerWaitsForSetup(t *testing.T) {
	c := newLocalChain(t, true, true, "")
	if d := authDetailsOf(t, req{path: "/api/v1/me"}.do(c.Handler())); d != (model.AuthDetails{Mode: "local", Setup: true}) {
		t.Errorf("before setup = %+v, want a 401 with setup true", d)
	}
	c.signIn(t)
	var me model.Me
	w := req{path: "/api/v1/me"}.do(c.Handler())
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &me) != nil || me.User != "anonymous" {
		t.Errorf("after setup = %d %s, want the anonymous viewer", w.Code, w.Body)
	}
}

// Setup stays needed until an account exists, whatever the token's state. With the hour of the
// token past and no account: the 401 still carries setup true and the anonymous viewer is still
// withheld (so /login keeps pointing to the setup line), GET /setup still serves the page (which
// says the link expired), and POST /setup refuses the token with 401 invalid_token. After a
// restart's new token sets the account up, /setup is 404 and the viewer reads.
func TestExpiredSetupTokenKeepsSetupNeeded(t *testing.T) {
	c := newLocalChain(t, true, true, "")
	now := time.Now()
	tok, err := localauth.NewSetupToken(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	c.local.Setup = tok
	now = now.Add(localauth.SetupTokenTTL + time.Second)
	h := c.Handler()

	if d := authDetailsOf(t, req{path: "/api/v1/me"}.do(h)); d != (model.AuthDetails{Mode: "local", Setup: true}) {
		t.Errorf("expired token, no account: details %+v, want a 401 with setup true", d)
	}
	if w := (req{path: "/setup"}).do(h); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "spa") {
		t.Errorf("GET /setup with an expired token = %d, want the SPA", w.Code)
	}
	w := req{method: http.MethodPost, path: "/setup", body: map[string]string{"token": tok.Value(), "password": chainPassword}}.do(h)
	wantError(t, w, http.StatusUnauthorized, model.ErrCodeInvalidToken)
	if _, ok := c.sess.Store.Admin(); ok {
		t.Fatal("an expired token created the account")
	}

	fresh, err := localauth.NewSetupToken(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.local.Setup = fresh
	c.signIn(t)
	if w := (req{path: "/setup"}).do(h); w.Code != http.StatusNotFound {
		t.Errorf("GET /setup after setup = %d, want 404", w.Code)
	}
	if w := (req{path: "/api/v1/me"}).do(h); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"anonymous"`) {
		t.Errorf("after setup = %d %s, want the anonymous viewer", w.Code, w.Body)
	}
}

// The anonymous viewer reads what a viewer reads, except every route of scope logs: 403, one
// denial, the stream never opened.
func TestAnonymousViewerNeverGetsLogs(t *testing.T) {
	h := newHarness(t)
	opt, _ := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{chainAdmin}}, true, nil)
	var d Deps
	opt(&d)
	h.Server.d.Auth = d.Auth
	if w := h.get("/api/v1/plugins/rt/items"); w.Code != http.StatusOK {
		t.Fatalf("anonymous read route = %d %s, want 200", w.Code, w.Body)
	}
	before := len(recordsOf(h.audit(t), audit.KindDenial))
	// Bounded: if the rule were gone the tail would open and stream until -timeout.
	wantError(t, within(t, "the anonymous viewer's tail", func() *httptest.ResponseRecorder { return h.get(tailPath) }), http.StatusForbidden, model.ErrCodeForbidden)
	if d := recordsOf(h.audit(t), audit.KindDenial); len(d) != before+1 || d[len(d)-1].Reason != obs.AuthForbidden.String() {
		t.Errorf("denials = %+v, want one forbidden for the tail", d)
	}
	if h.openTails() != 0 {
		t.Error("the refused tail took a slot")
	}
	// The same rule holds for a core route of scope logs.
	ran := false
	table := []Route{{Method: "GET", Path: "/api/v1/logs-row", Role: auth.RoleViewer, Scope: "logs", Handler: func(http.ResponseWriter, *http.Request) { ran = true }}}
	anon := auth.Principal{Kind: auth.KindAnonymous, User: "anonymous", Role: auth.RoleViewer}
	w := httptest.NewRecorder()
	h.Server.muxOf(table).ServeHTTP(w, asPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/logs-row", nil), anon))
	if w.Code != http.StatusForbidden || ran {
		t.Errorf("core logs row for anonymous = %d (ran %v), want 403", w.Code, ran)
	}
}

// The setup line is the way in: its URL, fragment and all, completes setup through the real
// chain; and the same line read back through the logs API (the VictoriaLogs plugin) shows the
// token redacted.
func TestSetupLineWorksAndLogsAPIRedactsIt(t *testing.T) {
	c := newLocalChain(t, true, false, "")
	var buf bytes.Buffer
	if err := localauth.WriteSetupLine(&buf, testOrigin, c.local.Setup); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	var rec struct {
		Msg, URL, Expires string
	}
	if err := json.Unmarshal([]byte(line), &rec); err != nil || !strings.Contains(rec.Msg, "setup token") || rec.Expires == "" {
		t.Fatalf("setup line %q (%v), want msg with \"setup token\", url and expires", line, err)
	}
	u, err := url.Parse(rec.URL)
	if err != nil || u.Path != "/setup" || u.RawQuery != "" {
		t.Fatalf("url %q: want /setup with the token in the fragment only", rec.URL)
	}
	frag, _ := url.ParseQuery(u.Fragment)
	tok := frag.Get("token")
	if !strings.HasPrefix(tok, localauth.SetupTokenPrefix) {
		t.Fatalf("fragment %q holds no setup token", u.Fragment)
	}
	w := req{method: http.MethodPost, path: "/setup", body: map[string]string{"token": tok, "password": chainPassword}}.do(c.Handler())
	if w.Code != http.StatusOK {
		t.Fatalf("setup with the printed token = %d %s", w.Code, w.Body)
	}

	logRec, _ := json.Marshal(map[string]string{"_msg": line, "_time": "2026-10-30T21:39:17.5Z", "kubernetes.pod_name": "p1", "kubernetes.container_name": "c1"})
	e := newVLEnv(t, newFakeVLogs(t, string(logRec)), secret.NewRegistry(), nil)
	e.waitRunning(t)
	w = e.get(vlLogsPath + "?ns=media&pod=p1")
	if w.Code != http.StatusOK {
		t.Fatalf("logs = %d", w.Code)
	}
	// The fragment's token= is redacted whole by the credential-shape rule before the dash_setup_
	// rule sees it; either way neither the token nor its random part survives.
	if body := w.Body.String(); strings.Contains(body, tok) || strings.Contains(body, tok[len(localauth.SetupTokenPrefix):]) || !strings.Contains(body, "[redacted]") {
		t.Errorf("logs API answer carries the token or no redaction:\n%s", body)
	}
}
