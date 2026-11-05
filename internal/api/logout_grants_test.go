package api

import (
	"net/http"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/session"
)

// addGrant stores an active grant of user directly.
func addGrant(t *testing.T, store *credentials.Store, user string) credentials.Grant {
	t.Helper()
	id, err := credentials.NewGrantID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, _ := credentials.NewRefreshToken(id)
	now := fixedNow.UTC()
	g := credentials.Grant{ID: id, User: user, ClientID: "inspector", Audience: "mcp", Scopes: []string{"read"}, RefreshHash: hash,
		CreatedAt: now, ExpiresAt: now.Add(credentials.GrantLifetime), RefreshedAt: now}
	if err := store.AddGrant(g); err != nil {
		t.Fatal(err)
	}
	return g
}

// Logout scopes (contract D23): "this" leaves the grants; "user" ("Log out everywhere")
// revokes the caller's grants and nobody else's; "all" (an administrator's "Sign out all
// users") revokes every grant. Personal access tokens survive every scope, and each grant
// revocation is audited.
func TestLogoutScopesRevokeGrants(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	store := e.sess.Store
	login := func() *http.Cookie {
		t.Helper()
		w := e.login(localAdmin, localPassword)
		if w.Code != http.StatusNoContent {
			t.Fatalf("login: %d", w.Code)
		}
		return sessionCookie(t, w)
	}
	_, _, hash, _ := credentials.NewPAT()
	pat := credentials.PAT{ID: "abcdefghij", Name: "ci", User: localAdmin, Hash: hash, Audience: "api", Scopes: []string{"read"}, CreatedAt: fixedNow, ExpiresAt: fixedNow.AddDate(1, 0, 0)}
	if err := store.AddPAT(pat); err != nil {
		t.Fatal(err)
	}
	mine := addGrant(t, store, localAdmin)
	other := addGrant(t, store, "bob")

	if w := e.post("/auth/logout", map[string]string{"scope": "this"}, login()); w.Code != http.StatusNoContent {
		t.Fatalf("logout this = %d", w.Code)
	}
	if len(store.Grants("")) != 2 {
		t.Fatal("logout of this session revoked a grant")
	}
	if w := e.post("/auth/logout", map[string]string{"scope": "user"}, login()); w.Code != http.StatusNoContent {
		t.Fatalf("logout user = %d %s", w.Code, w.Body)
	}
	if _, ok := store.GrantByID(mine.ID); ok {
		t.Error("Log out everywhere left the caller's grant")
	}
	if _, ok := store.GrantByID(other.ID); !ok {
		t.Error("Log out everywhere revoked another user's grant")
	}
	found := false
	for _, r := range e.records(auditGrantRevoke) {
		found = found || (r.Kind == audit.KindIntent && r.Actor.User == localAdmin && r.Target.Namespace == localAdmin && r.Target.Name == "*")
	}
	if !found {
		t.Error("no audit intent of the grant revocation")
	}

	// A viewer's "Log out everywhere" takes only their own grants.
	addGrant(t, store, "viewer")
	viewer, err := e.sess.Sessions.Issue(session.Identity{User: "viewer"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.post("/auth/logout", map[string]string{"scope": "user"}, viewer); w.Code != http.StatusNoContent {
		t.Fatalf("viewer logout user = %d", w.Code)
	}
	if len(store.Grants("viewer")) != 0 || len(store.Grants("bob")) != 1 {
		t.Errorf("after the viewer's logout: viewer %d bob %d", len(store.Grants("viewer")), len(store.Grants("bob")))
	}

	addGrant(t, store, localAdmin)
	if w := e.post("/auth/logout", map[string]string{"scope": "all"}, login()); w.Code != http.StatusNoContent {
		t.Fatalf("logout all = %d", w.Code)
	}
	if n := len(store.Grants("")); n != 0 {
		t.Errorf("Sign out all users left %d grants", n)
	}
	if _, ok := store.PATByID(pat.ID); !ok {
		t.Error("a logout revoked a personal access token")
	}
}
