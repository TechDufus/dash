package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/tokens"
)

// In the proxy mode dash sees a person's groups at every request: a group change reaches their
// tokens at the next request, so the role derived for the token follows it (design M.8); an
// unchanged set writes nothing to credentials.yaml.
func TestProxyGroupChangeReachesTheTokens(t *testing.T) {
	proxy := auth.NewProxy(authtest.Trust(), auth.ProxyIdentity{
		UserHeaders: []string{"X-Forwarded-Preferred-Username"}, GroupsHeader: "X-Forwarded-Groups",
	}, auth.RoleMap{AdminGroups: []string{"ops"}})
	dir := t.TempDir()
	store, err := credentials.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := newServer(func(d *Deps) {
		d.Auth, d.Credentials, d.Tokens = proxy, store, tokens.New(store, proxy.Roles(), nil, nil)
	})
	asBob := func(groups string, r req) *httptest.ResponseRecorder {
		r.header = map[string]string{"X-Forwarded-Preferred-Username": "bob", "X-Forwarded-Groups": groups}
		return r.do(s.Handler())
	}
	w := asBob("ops", req{method: http.MethodPost, path: "/api/v1/tokens", body: model.TokenCreateRequest{Name: "ci", Audience: "api", Scopes: []string{"read"}}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", w.Code, w.Body)
	}
	var c model.TokenCreated
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	role := func() string { return meOf(t, req{path: "/api/v1/me", header: bearer(c.Token)}.do(s.Handler())).Role }
	if got := role(); got != "admin" {
		t.Fatalf("token role with ops = %s", got)
	}

	path := filepath.Join(dir, credentials.FileName)
	before, _ := os.Stat(path)
	asBob("ops", req{path: "/api/v1/me"})
	if after, _ := os.Stat(path); !os.SameFile(before, after) {
		t.Error("an unchanged group set wrote credentials.yaml")
	}

	asBob("dev", req{path: "/api/v1/me"})
	if got := role(); got != "viewer" {
		t.Errorf("token role after bob left ops = %s, want viewer", got)
	}
	if p, _ := store.PATByID(c.Info.ID); !slices.Equal(p.Groups, []string{"dev"}) {
		t.Errorf("recorded groups = %v", p.Groups)
	}
}

// In the oidc mode a login records the groups the session keeps on the user's tokens: a group
// the IdP added reaches the token at the next login, not before.
func TestOIDCLoginRewritesTheTokensGroups(t *testing.T) {
	e := newOIDCEnv(t, oidcOpts{allowedUsers: []string{"carol"}, roles: auth.RoleMap{AdminGroups: []string{"ops"}}})
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.sh.Store.AddPAT(credentials.PAT{ID: id, Name: "ci", User: "carol", Hash: hash, Audience: "api", Scopes: []string{"read"},
		CreatedAt: fixedNow, ExpiresAt: fixedNow.AddDate(1, 0, 0)}); err != nil {
		t.Fatal(err)
	}
	role := func() string {
		w := req{path: "/api/v1/me", header: bearer(token)}.do(e.h)
		if w.Code != http.StatusOK {
			t.Fatalf("me = %d %s", w.Code, w.Body)
		}
		return meOf(t, w).Role
	}
	if got := role(); got != "viewer" {
		t.Fatalf("before = %s", got)
	}
	e.session(e.login(claims("preferred_username", "carol", "groups", []string{"dev", "ops"})))
	if got := role(); got != "admin" {
		t.Errorf("after a login with ops = %s, want admin", got)
	}
	if p, _ := e.sh.Store.PATByID(id); !slices.Equal(p.Groups, []string{"ops"}) {
		t.Errorf("recorded groups = %v, want only the kept ops", p.Groups)
	}
}
