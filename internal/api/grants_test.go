package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/tokens"
)

// consentFor starts an authorization request (audience, scopes) and returns its consent ID.
func (e *oauthEnv) consentFor(t *testing.T, audience string, scopes ...string) string {
	t.Helper()
	w := req{path: authorizeURL(audience, func(q url.Values) { q.Set("scope", strings.Join(scopes, " ")) })}.do(e.s.Handler())
	loc := w.Header().Get("Location")
	if w.Code != http.StatusFound || !strings.Contains(loc, "/consent/") {
		t.Fatalf("authorize = %d %s", w.Code, loc)
	}
	return loc[strings.LastIndex(loc, "/")+1:]
}

func (e *oauthEnv) do(r req) *httptest.ResponseRecorder { return r.do(e.s.Handler()) }

// approveAs posts an approval of consent id by user with scopes.
func (e *oauthEnv) approveAs(user, id string, scopes ...string) *httptest.ResponseRecorder {
	return e.do(as(user, req{method: http.MethodPost, path: "/api/v1/oauth/consents/" + id, body: model.OAuthConsentDecision{Approve: true, Scopes: scopes}}))
}

// breakAudit replaces the audit log with one whose writes fail.
func (e *oauthEnv) breakAudit(t *testing.T) {
	t.Helper()
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
}

// The consent page's data: only read is ticked, approvals and push are never offered to this
// client, and approving answers the redirect with code, state and iss; the grant's creation is
// audited before and after.
func TestConsentRoutes(t *testing.T) {
	e := newOAuthEnv(t)
	id := e.consentFor(t, "api", "read", "logs", "actions:aa")
	var c model.OAuthConsent
	decodeJSON(t, e.do(as("alice", req{path: "/api/v1/oauth/consents/" + id})), &c)
	if !slices.Equal(c.Scopes, []string{"read", "logs", "actions:aa"}) || !slices.Equal(c.DefaultScopes, []string{"read"}) ||
		c.Client.Name != "Inspector" || c.Audience != "api" || slices.Contains(c.Scopes, "approvals") {
		t.Fatalf("consent = %+v", c)
	}
	// Another person cannot see or decide it.
	wantError(t, e.do(as("bob", req{path: "/api/v1/oauth/consents/" + id})), http.StatusNotFound, model.ErrCodeNotFound)
	wantError(t, e.approveAs("bob", id, "read"), http.StatusNotFound, model.ErrCodeNotFound)
	wantError(t, e.approveAs("alice", id, "read", "push"), http.StatusBadRequest, model.ErrCodeInvalidInput)

	w := e.approveAs("alice", id, "read", "logs")
	var res model.OAuthConsentResult
	decodeJSON(t, w, &res)
	u, _ := url.Parse(res.Redirect)
	if u.Host != "127.0.0.1:4711" || u.Query().Get("code") == "" || u.Query().Get("state") != "st" || u.Query().Get("iss") != testOrigin {
		t.Fatalf("redirect = %s", res.Redirect)
	}
	grants := e.s.d.Credentials.Grants("alice")
	if len(grants) != 1 || !slices.Equal(grants[0].Scopes, []string{"read", "logs"}) {
		t.Fatalf("grants = %+v", grants)
	}
	var intent, outcome bool
	for _, r := range auditRecords(t, e.auditDir) {
		if r.Action == auditGrantCreate && r.Target.Name == grants[0].ID && r.Actor.User == "alice" && r.Actor.ClientID == "inspector" {
			intent = intent || r.Kind == audit.KindIntent
			outcome = outcome || r.Kind == audit.KindOutcome
		}
	}
	if !intent || !outcome {
		t.Errorf("audit intent %v outcome %v", intent, outcome)
	}
	// The request is used up.
	wantError(t, e.approveAs("alice", id, "read"), http.StatusNotFound, model.ErrCodeNotFound)

	// Denying answers access_denied.
	id = e.consentFor(t, "mcp", "read")
	w = e.do(as("alice", req{method: http.MethodPost, path: "/api/v1/oauth/consents/" + id, body: model.OAuthConsentDecision{}}))
	decodeJSON(t, w, &res)
	if u, _ := url.Parse(res.Redirect); u.Query().Get("error") != "access_denied" {
		t.Errorf("deny = %s", res.Redirect)
	}
}

// The consent and grant routes are for people: a bearer token, even its owner's with every
// scope, gets 403.
func TestConsentRoutesRefuseBearer(t *testing.T) {
	e := newOAuthEnv(t)
	tr := e.grant(t, aliceAdmin, "api", "read")
	id := e.consentFor(t, "api", "read")
	for _, r := range []req{
		{path: "/api/v1/oauth/consents/" + id},
		{method: http.MethodPost, path: "/api/v1/oauth/consents/" + id, body: model.OAuthConsentDecision{Approve: true, Scopes: []string{"read"}}},
		{path: "/api/v1/oauth/grants"},
		{method: http.MethodDelete, path: "/api/v1/oauth/grants?user=me"},
	} {
		r.header, r.raw = bearer(tr.AccessToken), true
		if w := e.do(r); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a token = %d", r.method, r.path, w.Code)
		}
	}
	if n := len(e.s.d.Credentials.Grants("alice")); n != 1 {
		t.Errorf("grants = %d", n)
	}
}

// With the audit log failing, approving and revoking answer 503 and change nothing.
func TestGrantWritesFailClosedOnAudit(t *testing.T) {
	e := newOAuthEnv(t)
	tr := e.grant(t, aliceAdmin, "api", "read")
	before, _ := os.ReadFile(e.s.d.Credentials.Path())
	id := e.consentFor(t, "api", "read")
	e.breakAudit(t)
	wantError(t, e.approveAs("alice", id, "read"), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	g := e.s.d.Credentials.Grants("alice")[0]
	wantError(t, e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/oauth/grants/" + g.ID})), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	wantError(t, e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/oauth/grants?user=me"})), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	if after, _ := os.ReadFile(e.s.d.Credentials.Path()); string(after) != string(before) {
		t.Error("credentials.yaml changed")
	}
	if w := e.do(req{path: "/api/v1/me", header: bearer(tr.AccessToken)}); w.Code != http.StatusOK {
		t.Errorf("the access token after refused revocations = %d", w.Code)
	}
}

// A user holds at most 20 grants: the next consent is 409 grant_limit and creates nothing.
func TestGrantLimit(t *testing.T) {
	e := newOAuthEnv(t)
	for range credentials.MaxActiveGrants {
		if w := e.approveAs("alice", e.consentFor(t, "mcp", "read"), "read"); w.Code != http.StatusOK {
			t.Fatalf("approve = %d %s", w.Code, w.Body)
		}
	}
	wantError(t, e.approveAs("alice", e.consentFor(t, "mcp", "read"), "read"), http.StatusConflict, model.ErrCodeGrantLimit)
	if n := len(e.s.d.Credentials.Grants("alice")); n != credentials.MaxActiveGrants {
		t.Errorf("grants = %d", n)
	}
}

// Listing and revoking: one's own grants; an administrator anyone's (?user=, ?user=*); a viewer
// sees another user's grant as missing. ?user=me revokes all of one's grants and no token.
func TestGrantListAndRevoke(t *testing.T) {
	e := newOAuthEnv(t)
	e.approveAs("alice", e.consentFor(t, "mcp", "read"), "read")
	e.approveAs("bob", e.consentFor(t, "mcp", "read"), "read")
	e.approveAs("bob", e.consentFor(t, "api", "read"), "read")
	list := func(user, q string) (int, []model.OAuthGrantInfo) {
		w := e.do(as(user, req{path: "/api/v1/oauth/grants" + q}))
		var l model.OAuthGrantList
		_ = json.Unmarshal(w.Body.Bytes(), &l)
		return w.Code, l.Grants
	}
	if code, l := list("bob", ""); code != http.StatusOK || len(l) != 2 || l[0].User != "bob" || l[0].Client.ID != "inspector" || l[0].Client.Kind != "config" || l[0].LastUsedAt != nil {
		t.Errorf("bob's = %d %+v", code, l)
	}
	if code, _ := list("bob", "?user=alice"); code != http.StatusForbidden {
		t.Errorf("a viewer listing another user = %d", code)
	}
	if _, l := list("alice", "?user=*"); len(l) != 3 {
		t.Errorf("everyone's = %d", len(l))
	}
	aliceGrant := e.s.d.Credentials.Grants("alice")[0]
	wantError(t, e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/oauth/grants/" + aliceGrant.ID})), http.StatusNotFound, model.ErrCodeNotFound)
	if w := e.do(as("alice", req{method: http.MethodDelete, path: "/api/v1/oauth/grants/" + e.s.d.Credentials.Grants("bob")[0].ID})); w.Code != http.StatusNoContent {
		t.Errorf("an admin revoking bob's = %d", w.Code)
	}
	_, _, hash, _ := credentials.NewPAT()
	pat := credentials.PAT{ID: "abcdefghij", Name: "ci", User: "bob", Hash: hash, Audience: "api", Scopes: []string{"read"}, CreatedAt: fixedNow, ExpiresAt: fixedNow.AddDate(0, 1, 0)}
	if err := e.s.d.Credentials.AddPAT(pat); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/oauth/grants?user=alice"})), http.StatusBadRequest, model.ErrCodeInvalidInput)
	if w := e.do(as("bob", req{method: http.MethodDelete, path: "/api/v1/oauth/grants?user=me"})); w.Code != http.StatusNoContent {
		t.Fatalf("revoke mine = %d", w.Code)
	}
	if n := len(e.s.d.Credentials.Grants("bob")); n != 0 {
		t.Errorf("bob keeps %d grants", n)
	}
	if n := len(e.s.d.Credentials.Grants("alice")); n != 1 {
		t.Errorf("alice's grant went too")
	}
	if _, ok := e.s.d.Credentials.PATByID(pat.ID); !ok {
		t.Error("revoking grants took a personal access token")
	}
}

// Without the authorization server the consent and grant routes are 404.
func TestGrantRoutesOff(t *testing.T) {
	s := newServer(nil)
	for _, p := range []string{"/api/v1/oauth/consents/x", "/api/v1/oauth/grants"} {
		if w := (req{path: p}).do(s.Handler()); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
}

// In the proxy mode a group change seen at the next request reaches the person's grants, so
// the role of their OAuth access token follows it; an unchanged set writes nothing.
func TestProxyGroupChangeReachesTheGrants(t *testing.T) {
	proxy := auth.NewProxy(authtest.Trust(), auth.ProxyIdentity{
		UserHeaders: []string{"X-Forwarded-Preferred-Username"}, GroupsHeader: "X-Forwarded-Groups",
	}, auth.RoleMap{AdminGroups: []string{"ops"}})
	e := &oauthEnv{}
	e.s, e.auditDir = auditedServer(t, func(d *Deps) {
		store, err := credentials.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		d.Auth, d.Credentials, d.Tokens = proxy, store, tokens.New(store, proxy.Roles(), nil, nil)
		d.OAuth = testOAuth(t, d)
	})
	asBob := func(groups string, r req) *httptest.ResponseRecorder {
		r.header = map[string]string{"X-Forwarded-Preferred-Username": "bob", "X-Forwarded-Groups": groups}
		return e.do(r)
	}
	id := e.consentFor(t, "api", "read")
	if w := asBob("ops", req{method: http.MethodPost, path: "/api/v1/oauth/consents/" + id, body: model.OAuthConsentDecision{Approve: true, Scopes: []string{"read"}}}); w.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", w.Code, w.Body)
	}
	g := e.s.d.Credentials.Grants("bob")[0]
	if !slices.Equal(g.Groups, []string{"ops"}) {
		t.Fatalf("grant groups = %v", g.Groups)
	}
	before, _ := os.Stat(e.s.d.Credentials.Path())
	asBob("ops", req{path: "/api/v1/me"})
	if after, _ := os.Stat(e.s.d.Credentials.Path()); !os.SameFile(before, after) {
		t.Error("an unchanged group set wrote credentials.yaml")
	}
	asBob("dev", req{path: "/api/v1/me"})
	if g, _ := e.s.d.Credentials.GrantByID(g.ID); !slices.Equal(g.Groups, []string{"dev"}) {
		t.Errorf("grant groups after the change = %v", g.Groups)
	}
}
