package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/tokens"
)

// tokenFixture is an in-memory credential store with a token resolver over it: alice is the one
// administrator of its mapping, everyone else a viewer.
type tokenFixture struct {
	store *credentials.Store
	res   *tokens.Resolver
}

func newTokenFixture(t *testing.T, static ...config.StaticToken) *tokenFixture {
	t.Helper()
	store, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	roles := auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice"}})
	return &tokenFixture{store: store, res: tokens.New(store, roles, static, func() time.Time { return fixedNow })}
}

// deps hands the store and the resolver to a Server.
func (f *tokenFixture) deps(d *Deps) { d.Tokens, d.Credentials = f.res, f.store }

// pat stores a personal access token of user and returns the token and its ID.
func (f *tokenFixture) pat(t *testing.T, user, audience string, scopes []string, namespaces ...string) (string, string) {
	t.Helper()
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddPAT(credentials.PAT{
		ID: id, Name: "test", User: user, Hash: hash, Audience: audience, Scopes: scopes, ActionNamespaces: namespaces,
		CreatedAt: fixedNow, ExpiresAt: fixedNow.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return token, id
}

// bearer is the header of a request that presents token. The proxy's identity header (alice,
// which req.do sets) stays: a bearer request never reads it.
func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func meOf(t *testing.T, w *httptest.ResponseRecorder) model.Me {
	t.Helper()
	var me model.Me
	decodeJSON(t, w, &me)
	return me
}

// A bearer request is the token's principal: the identity headers of the trusted proxy are not
// read, and /me tells the token's own scopes and audience.
func TestBearerIsTheTokensPrincipal(t *testing.T) {
	f := newTokenFixture(t)
	s := newServer(f.deps)
	token, _ := f.pat(t, "bob", "api", []string{"read"})
	w := req{path: "/api/v1/me", header: bearer(token)}.do(s.Handler())
	if w.Code != http.StatusOK {
		t.Fatalf("me = %d %s", w.Code, w.Body)
	}
	me := meOf(t, w)
	if me.User != "bob" || me.Role != "viewer" || !slices.Equal(me.Scopes, []string{"read"}) || me.Audience != "api" || me.Capabilities.Tokens {
		t.Errorf("me = %+v; want bob, viewer, [read], api, no token creation", me)
	}
	// Without the bearer the same request is alice through the proxy.
	if me := meOf(t, req{path: "/api/v1/me"}.do(s.Handler())); me.User != "alice" {
		t.Errorf("without the bearer: %+v", me)
	}
}

// Bearer plus dash's session cookie (either name) is 400 ambiguous_credentials; an identity
// proxy's cookie is not dash's and changes nothing.
func TestBearerWithTheSessionCookieIsAmbiguous(t *testing.T) {
	f := newTokenFixture(t)
	s, dir := auditedServer(t, f.deps)
	token, _ := f.pat(t, "bob", "api", []string{"read"})
	for _, name := range []string{session.CookieName(true), session.CookieName(false)} {
		h := bearer(token)
		h["Cookie"] = name + "=v1.anything"
		w := req{path: "/api/v1/me", header: h}.do(s.Handler())
		wantError(t, w, http.StatusBadRequest, model.ErrCodeAmbiguousCredentials)
	}
	h := bearer(token)
	h["Cookie"] = "_oauth2_proxy=abc"
	if w := (req{path: "/api/v1/me", header: h}).do(s.Handler()); w.Code != http.StatusOK {
		t.Errorf("with the identity proxy's cookie: %d %s, want 200", w.Code, w.Body)
	}
	if v, ok := seriesValue(t, scrapeMetrics(t, s), "dash_auth_failures_total", map[string]string{"reason": obs.AuthAmbiguousCredentials.String()}); !ok || v != 2 {
		t.Errorf("ambiguous_credentials counted %v (%v), want 2", v, ok)
	}
	if d := recordsOf(auditRecords(t, dir), audit.KindDenial); len(d) == 0 || d[0].Reason != "ambiguous_credentials" {
		t.Errorf("denials = %+v", d)
	}
}

// A token is bound to its audience: an mcp token on /api/v1 is 401 invalid_token with the
// challenge, as is an unknown, expired or revoked one.
func TestBearerRefusalsAre401WithTheChallenge(t *testing.T) {
	f := newTokenFixture(t)
	s, _ := auditedServer(t, f.deps)
	mcp, _ := f.pat(t, "alice", "mcp", []string{"read"})
	unknown, _, _, _ := credentials.NewPAT()
	for name, token := range map[string]string{"mcp audience": mcp, "unknown": unknown, "not a token": "abc", "empty": ""} {
		w := req{path: "/api/v1/me", header: bearer(token)}.do(s.Handler())
		wantError(t, w, http.StatusUnauthorized, model.ErrCodeInvalidToken)
		if got := w.Header().Get("WWW-Authenticate"); got != `Bearer resource_metadata="https://dash.example.com/.well-known/oauth-protected-resource/api"` {
			t.Errorf("%s: WWW-Authenticate = %q", name, got)
		}
	}
	if v, ok := seriesValue(t, scrapeMetrics(t, s), "dash_auth_failures_total", map[string]string{"reason": obs.AuthInvalidToken.String()}); !ok || v != 4 {
		t.Errorf("invalid_token counted %v (%v), want 4", v, ok)
	}
}

// A bearer write needs no CSRF header and no Origin; a cookie or identity-proxy write without
// them is 403 csrf_failed; and in auth.mode=none, where a Bearer header resolves nothing, the
// header alone never skips the check (contract D3).
func TestCSRFIsSkippedOnlyForTokenPrincipals(t *testing.T) {
	e := newActionEnv(t, nil)
	f := newTokenFixture(t)
	f.deps(&e.d)
	token, _ := f.pat(t, "alice", "api", []string{"read", "actions:aa"})
	post := func(header map[string]string) int {
		return req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", raw: true, header: header}.do(e.Handler()).Code
	}
	if got := post(bearer(token)); got != http.StatusAccepted {
		t.Errorf("bearer write without CSRF = %d, want 202", got)
	}
	if got := post(nil); got != http.StatusForbidden {
		t.Errorf("identity-proxy write without CSRF = %d, want 403", got)
	}

	opt, sh := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{"alice"}}, false, nil)
	var d Deps
	opt(&d)
	e.d.Auth = d.Auth
	if got := post(cookieFor(t, sh, "alice")); got != http.StatusForbidden {
		t.Errorf("cookie write without CSRF = %d, want 403", got)
	}

	e.d.Tokens = nil
	e.d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
	if got := post(bearer(token)); got != http.StatusForbidden {
		t.Errorf("auth.mode=none: a Bearer header skipped the CSRF check (%d)", got)
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("the action ran %d times, want once", n)
	}
}

// A revocation takes effect on the next request, and an open event stream or log tail ends at
// the first heartbeat after it (contract D13).
func TestRevokedTokenIsRefusedAndItsStreamsEnd(t *testing.T) {
	h := newHarness(t)
	f := newTokenFixture(t)
	f.deps(&h.d)
	h.heartbeat = 20 * time.Millisecond
	token, id := f.pat(t, "alice", "api", []string{"read", "logs"})
	other, _ := f.pat(t, "alice", "api", []string{"read"})

	events := h.openStream(t, "/api/v1/stream", "", bearer(token))
	tail := h.openStream(t, tailPath, "", bearer(token))
	kept := h.openStream(t, "/api/v1/stream", "", bearer(other))
	for _, st := range []*sseStream{events, tail, kept} {
		if st.status() != http.StatusOK {
			t.Fatalf("open = %d", st.status())
		}
	}
	events.expect(": ping")
	tail.expect(": ping")

	if _, ok, err := f.store.RevokePAT(id); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if w := (req{path: "/api/v1/me", header: bearer(token)}).do(h.Handler()); w.Code != http.StatusUnauthorized {
		t.Errorf("the next request = %d, want 401", w.Code)
	}
	ended(t, events.lines, "the event stream after the revocation")
	ended(t, tail.lines, "the tail after the revocation")
	kept.expect(": ping") // another token's stream carries on
	kept.expect(": ping")
	h.eventually(t, "the tail's slot to be released", func() bool { return h.openTails() == 0 })
}

// The token's scopes narrow what it reaches: read for /me and the read routes, logs for a log
// tail, actions:<plugin> for that plugin's actions, and its actionNamespaces for their target.
func TestTokenScopesAreEnforced(t *testing.T) {
	h := newHarness(t)
	f := newTokenFixture(t)
	f.deps(&h.d)
	readOnly, _ := f.pat(t, "alice", "api", []string{"read"})
	logsOnly, _ := f.pat(t, "alice", "api", []string{"logs"})
	noActions, _ := f.pat(t, "alice", "api", []string{"read"})
	scoped, _ := f.pat(t, "alice", "api", []string{"actions:aa"}, "app*", "web")
	viewerActions, _ := f.pat(t, "bob", "api", []string{"actions:aa"}) // stored past the API's own check
	everywhere, _ := f.pat(t, "alice", "api", []string{"actions:aa"}, "web", "*")
	ping := func(token string) *httptest.ResponseRecorder {
		return req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", raw: true, header: bearer(token)}.do(h.Handler())
	}

	get := func(path, token string) int { return req{path: path, header: bearer(token)}.do(h.Handler()).Code }
	act := func(token string, body any) *httptest.ResponseRecorder {
		return req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/restart", body: body, raw: true, header: bearer(token)}.do(h.Handler())
	}
	for _, tc := range []struct {
		name  string
		w     *httptest.ResponseRecorder
		error string
	}{
		{"a logs token on /me", req{path: "/api/v1/me", header: bearer(logsOnly)}.do(h.Handler()), model.ErrCodeInsufficientScope},
		{"a logs token on a read route", req{path: "/api/v1/plugins/rt/items", header: bearer(logsOnly)}.do(h.Handler()), model.ErrCodeInsufficientScope},
		{"a read token on a log tail", req{path: tailPath, header: bearer(readOnly)}.do(h.Handler()), model.ErrCodeInsufficientScope},
		{"an action without its scope", act(noActions, okRestart), model.ErrCodeInsufficientScope},
		{"an action outside the token's namespaces", act(scoped, restartIn{Namespace: "kube-system", Name: "x"}), model.ErrCodeForbidden},
		{"a viewer's token with actions:*", act(viewerActions, okRestart), model.ErrCodeForbidden},
		{"a target without a namespace under a namespace list", ping(scoped), model.ErrCodeForbidden},
		{"a namespace that only shares a prefix with an exact entry", act(scoped, restartIn{Namespace: "web-prod", Name: "x"}), model.ErrCodeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) { wantError(t, tc.w, http.StatusForbidden, tc.error) })
	}
	if got := get("/api/v1/plugins/rt/items", readOnly); got != http.StatusOK {
		t.Errorf("a read token on a read route = %d", got)
	}
	for _, ns := range []string{"apps", "web"} {
		if w := act(scoped, restartIn{Namespace: ns, Name: "x"}); w.Code != http.StatusAccepted {
			t.Errorf("an action in %s = %d %s, want 202", ns, w.Code, w.Body)
		}
	}
	if w := ping(everywhere); w.Code != http.StatusAccepted {
		t.Errorf("a target without a namespace under a list with * = %d %s, want 202", w.Code, w.Body)
	}
	if n := len(h.acts()); n != 3 {
		t.Errorf("actions run = %d, want the three allowed ones", n)
	}
	if v, ok := seriesValue(t, h.scrape(t), "dash_auth_failures_total", map[string]string{"reason": obs.AuthInsufficientScope.String()}); !ok || v != 4 {
		t.Errorf("insufficient_scope counted %v (%v), want 4", v, ok)
	}
}

// Settings (and every other interactive route) refuse a token with 403, whatever its role.
func TestSettingsRefuseBearerPrincipals(t *testing.T) {
	f := newTokenFixture(t)
	s := newServer(f.deps)
	token, _ := f.pat(t, "alice", "api", []string{"read", "logs"})
	for _, path := range []string{"/api/v1/settings", "/api/v1/settings/audit"} {
		wantError(t, req{path: path, header: bearer(token)}.do(s.Handler()), http.StatusForbidden, model.ErrCodeForbidden)
	}
}

// An api token gets 300 requests a minute; the 301st is 429, and another token is not affected.
func TestAPITokenRateLimit(t *testing.T) {
	f := newTokenFixture(t)
	s := newServer(f.deps)
	busy, _ := f.pat(t, "alice", "api", []string{"read"})
	calm, _ := f.pat(t, "alice", "api", []string{"read"})
	h := s.Handler()
	for i := range tokenBurst {
		if w := (req{path: "/api/v1/me", header: bearer(busy)}).do(h); w.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i+1, w.Code)
		}
	}
	w := req{path: "/api/v1/me", header: bearer(busy)}.do(h)
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if w := (req{path: "/api/v1/me", header: bearer(calm)}).do(h); w.Code != http.StatusOK {
		t.Errorf("another token = %d", w.Code)
	}
	// The bucket refills at 5 a second.
	s.now = func() time.Time { return fixedNow.Add(time.Second) }
	if w := (req{path: "/api/v1/me", header: bearer(busy)}).do(h); w.Code != http.StatusOK {
		t.Errorf("a second later = %d, want 200", w.Code)
	}
}

// An action by a token is recorded with the token: type token, surface api, its ID (contract
// D11).
func TestTokenActionIsAuditedWithTheToken(t *testing.T) {
	e := newActionEnv(t, nil)
	f := newTokenFixture(t)
	f.deps(&e.d)
	token, id := f.pat(t, "alice", "api", []string{"actions:aa"})
	if w := (req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", raw: true, header: bearer(token)}).do(e.Handler()); w.Code != http.StatusAccepted {
		t.Fatalf("ping = %d %s", w.Code, w.Body)
	}
	recs := auditRecords(t, e.dir)
	want := audit.Actor{Type: "token", User: "alice", TokenID: id, Surface: "api"}
	if len(recs) != 2 || recs[0].Actor != want || recs[1].Actor != want {
		t.Errorf("records = %+v, want intent and outcome by %+v", recs, want)
	}
}

// The audit view shows the token a record was made with (contract D11).
func TestAuditViewShowsTheToken(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	e.s.d.Audit.Event(audit.Record{Action: "ping", Actor: audit.Actor{Type: "token", User: "alice", TokenID: "abcdefghij", Surface: "api"}})
	page := e.auditPage(t, "")
	if len(page.Records) == 0 || page.Records[0].Actor != (model.AuditActor{Type: "token", User: "alice", TokenID: "abcdefghij", Surface: "api"}) {
		t.Errorf("records = %+v", page.Records)
	}
}

// A canary token's secret is never written anywhere: not to the log, not in an answer, not to
// the audit file, whether it is valid, unknown, for the other audience or revoked.
func TestCanaryTokenNeverLeaks(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newActionEnv(t, nil)
	f := newTokenFixture(t)
	f.deps(&e.d)
	var probe bytes.Buffer
	e.probe.log = slog.New(slog.NewTextHandler(&probe, nil))
	canary, id := f.pat(t, "alice", "api", []string{"read", "actions:aa"})
	mcp, _ := f.pat(t, "alice", "mcp", []string{"read"})
	unknown, _, _, _ := credentials.NewPAT()
	secretOf := func(token string) string { return token[strings.LastIndexByte(token[:len(token)-43], '_')+1:] }

	var answers bytes.Buffer
	send := func(token string) {
		for _, r := range []req{
			{path: "/api/v1/me", header: bearer(token)},
			{path: "/api/v1/settings", header: bearer(token)},
			{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", raw: true, header: bearer(token)},
			{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/restart", body: "{bad", raw: true, header: bearer(token)},
		} {
			w := r.do(e.Handler())
			answers.Write(w.Body.Bytes())
			for k, v := range w.Header() {
				answers.WriteString(k + ": " + strings.Join(v, ",") + "\n")
			}
		}
	}
	send(canary)
	send(mcp)
	send(unknown)
	if _, _, err := f.store.RevokePAT(id); err != nil {
		t.Fatal(err)
	}
	send(canary)

	auditFile, err := os.ReadFile(filepath.Join(e.dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for where, text := range map[string]string{"log": logs.String(), "probe": probe.String(), "answers": answers.String(), "audit": string(auditFile)} {
		for _, token := range []string{canary, mcp, unknown} {
			if strings.Contains(text, secretOf(token)) {
				t.Errorf("a token's secret is in the %s:\n%s", where, text)
			}
		}
	}
	if !strings.Contains(string(auditFile), id) {
		t.Error("the audit file does not name the token by its ID")
	}
}

// capabilities.tokens: a person may create tokens where the store keeps them (a data dir, or the
// in-memory store of dev and demo), never a token principal, and not without a store.
func TestMeSaysWhetherTokensCanBeCreated(t *testing.T) {
	persistent, err := credentials.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = persistent.Close() })
	memory, err := credentials.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memory.Close() })
	res := tokens.New(memory, nil, nil, nil)
	for _, tc := range []struct {
		name string
		mod  func(*Deps)
		want bool
	}{
		{"a data dir", func(d *Deps) { d.Credentials, d.Tokens = persistent, res }, true},
		{"no writable data dir", func(d *Deps) { d.Credentials, d.Tokens = memory, res }, false},
		{"dev mode in memory", func(d *Deps) { d.Credentials, d.Tokens, d.Cfg.Dev = memory, res, true }, true},
		{"no store (none)", nil, false},
	} {
		s := newServer(tc.mod)
		if me := meOf(t, req{path: "/api/v1/me"}.do(s.Handler())); me.Capabilities.Tokens != tc.want {
			t.Errorf("%s: capabilities.tokens = %v, want %v", tc.name, me.Capabilities.Tokens, tc.want)
		}
	}
}

// For a person, /me's scopes are what they may put on a token: read and logs, and for an
// administrator actions:<plugin> for each plugin with mounted actions.
func TestMeScopesAreWhatAPersonMayMint(t *testing.T) {
	e := minRoleEnv(t, nil)
	if got := meAs(t, e.Server, auth.RoleAdmin).Scopes; !slices.Equal(got, []string{"read", "logs", "actions:adm", "actions:vwr"}) {
		t.Errorf("admin scopes = %v", got)
	}
	if got := meAs(t, e.Server, auth.RoleViewer).Scopes; !slices.Equal(got, []string{"read", "logs"}) {
		t.Errorf("viewer scopes = %v", got)
	}
}

// A token's request is named by the server's mode, never "token": /me's authMode and the audit
// record's in the proxy mode and in demo mode. A refused bearer credential is recorded on the
// api surface.
func TestTokenRequestsCarryTheServersMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		auth func(*Deps)
	}{
		{"proxy", nil},
		{"demo", func(d *Deps) {
			d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin})
		}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newTokenFixture(t)
			s, dir := auditedServer(t, func(d *Deps) {
				f.deps(d)
				if tc.auth != nil {
					tc.auth(d)
				}
			})
			token, _ := f.pat(t, "alice", "api", []string{"logs"})
			if w := (req{path: "/api/v1/me", header: bearer(token)}).do(s.Handler()); w.Code != http.StatusForbidden {
				t.Fatalf("a logs token on /me = %d", w.Code)
			}
			read, _ := f.pat(t, "alice", "api", []string{"read"})
			if me := meOf(t, req{path: "/api/v1/me", header: bearer(read)}.do(s.Handler())); me.AuthMode != tc.mode {
				t.Errorf("me.authMode = %q, want %q", me.AuthMode, tc.mode)
			}
			req{path: "/api/v1/me", header: bearer("dash_pat_nope")}.do(s.Handler())
			var denials []audit.Record
			for _, r := range auditRecords(t, dir) {
				if r.AuthMode != tc.mode {
					t.Errorf("record %+v: authMode %q, want %q", r, r.AuthMode, tc.mode)
				}
				if r.Kind == audit.KindDenial {
					denials = append(denials, r)
				}
			}
			if len(denials) != 2 || denials[0].Actor.Surface != "api" || denials[1].Actor.Surface != "api" {
				t.Errorf("denials = %+v, want the scope refusal and the invalid token on the api surface", denials)
			}
		})
	}
}

// The refusals of a token say why: a token for the other audience names it, a person-only route
// says a token cannot use it, and a missing scope is named.
func TestTokenRefusalsSayWhy(t *testing.T) {
	f := newTokenFixture(t)
	s := newServer(f.deps)
	mcp, _ := f.pat(t, "alice", "mcp", []string{"read"})
	logs, _ := f.pat(t, "alice", "api", []string{"logs"})
	read, _ := f.pat(t, "alice", "api", []string{"read"})
	unknown, _, _, _ := credentials.NewPAT()
	for _, tc := range []struct {
		token, path   string
		status        int
		code, message string
	}{
		{mcp, "/api/v1/me", 401, model.ErrCodeInvalidToken, "token not valid for this API (audience mcp)"},
		{unknown, "/api/v1/me", 401, model.ErrCodeInvalidToken, "invalid token"},
		{read, "/api/v1/settings", 403, model.ErrCodeForbidden, "this route needs a signed-in user, not a token"},
		{logs, "/api/v1/me", 403, model.ErrCodeInsufficientScope, "this token lacks the read scope"},
	} {
		got := wantError(t, req{path: tc.path, header: bearer(tc.token)}.do(s.Handler()), tc.status, tc.code)
		if got.Error != tc.message {
			t.Errorf("%s: message %q, want %q", tc.path, got.Error, tc.message)
		}
	}
}

// The challenge's resource_metadata documents arrive with the OAuth server; until then every
// /.well-known/ path is a JSON 404, never the web UI's index.html.
func TestWellKnownIsAJSON404(t *testing.T) {
	s := newServer(nil)
	for _, path := range []string{"/.well-known/oauth-protected-resource/api", "/.well-known/oauth-authorization-server", "/.well-known/"} {
		w := req{path: path}.do(s.Handler())
		wantError(t, w, http.StatusNotFound, model.ErrCodeNotFound)
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: Content-Type %q", path, ct)
		}
	}
}
