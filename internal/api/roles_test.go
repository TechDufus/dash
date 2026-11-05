package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// d2Chain is the request chain of the design (D2), authentication ahead of CSRF, built from the
// middleware itself so these tests do not depend on how Handler wires it.
func d2Chain(s *Server) http.Handler {
	return s.d.Metrics.Instrument(s.requestID(s.hosts(s.authn(s.csrf(s.mux())))))
}

// withGroupsProxy is an authenticator that reads X-Forwarded-Groups and maps roles with m.
func withGroupsProxy(m auth.RoleMap, opts ...auth.ProxyOption) *auth.Proxy {
	id := authtest.Identity()
	id.GroupsHeader = "X-Forwarded-Groups"
	return auth.NewProxy(authtest.Trust(), id, m, opts...)
}

// roleOnMe asks /api/v1/me as user (with groups, when not empty) and returns the role it reports.
func roleOnMe(t *testing.T, h http.Handler, user, groups string) string {
	t.Helper()
	hdr := map[string]string{"X-Forwarded-Preferred-Username": user}
	if groups != "" {
		hdr["X-Forwarded-Groups"] = groups
	}
	w := req{path: "/api/v1/me", header: hdr}.do(h)
	if w.Code != http.StatusOK {
		t.Fatalf("me as %q = %d %s", user, w.Code, w.Body)
	}
	var me model.Me
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.User != user {
		t.Errorf("me.user = %q, want %q", me.User, user)
	}
	return me.Role
}

func TestRoleMapping(t *testing.T) {
	m := auth.RoleMap{AdminUsers: []string{"Bob"}, AdminGroups: []string{"ops"}}
	h := d2Chain(newServer(withAuth(withGroupsProxy(m))))
	for _, tc := range []struct {
		name, user, groups, want string
	}{
		{"a listed user is admin", "Bob", "", "admin"},
		{"user names are case-sensitive", "bob", "", "viewer"},
		{"an unlisted user is a viewer", "dash-verify", "", "viewer"},
		{"a member of an admin group is admin", "dash-verify", "dev, ops", "admin"},
		{"group names are exact", "dash-verify", "Ops, operations", "viewer"},
		{"a group list without an admin group", "dash-verify", "dev,qa", "viewer"},
	} {
		if got := roleOnMe(t, h, tc.user, tc.groups); got != tc.want {
			t.Errorf("%s: role = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The groups header is read only when it is configured: with it off, a header the client sends
// grants nothing, even for a group on the admin list.
func TestRoleMappingIgnoresGroupsWhenTheHeaderIsNotConfigured(t *testing.T) {
	m := auth.RoleMap{AdminGroups: []string{"ops"}}
	h := d2Chain(newServer(withAuth(authtest.ProxyWith(m))))
	if got := roleOnMe(t, h, "dash-verify", "ops"); got != "viewer" {
		t.Errorf("role = %q, want viewer: the groups header is off", got)
	}
}

func TestPrincipalCarriesGroups(t *testing.T) {
	s := newServer(withAuth(withGroupsProxy(auth.RoleMap{})))
	var seen seenByNext
	w := req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Groups": " ops, dev ,ops"}}.do(behindAuthn(s, &seen))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d", w.Code)
	}
	if want := []string{"ops", "dev"}; !slices.Equal(seen.p.Groups, want) {
		t.Errorf("groups = %q, want %q", seen.p.Groups, want)
	}
}

func TestEmptyListsMakeEveryUserAViewer(t *testing.T) {
	s := newServer(withAuth(authtest.Proxy()))
	h := d2Chain(s)
	for _, user := range []string{"alice", "carol", "admin", "root"} {
		if got := roleOnMe(t, h, user, ""); got != "viewer" {
			t.Errorf("%s: role = %q, want viewer with both lists empty", user, got)
		}
	}
}

// A swapped role holder applies to the very next request, with no restart and no new
// authenticator: this is what the reload subscription does.
func TestSwappedRoleHolderAppliesOnTheNextRequest(t *testing.T) {
	p := authtest.Proxy("alice")
	h := d2Chain(newServer(withAuth(p)))
	if got := roleOnMe(t, h, "alice", ""); got != "admin" {
		t.Fatalf("before the swap: role = %q, want admin", got)
	}
	p.Roles().Store(auth.RoleMap{AdminUsers: []string{"bob"}})
	if got := roleOnMe(t, h, "alice", ""); got != "viewer" {
		t.Errorf("alice after the swap: role = %q, want viewer", got)
	}
	if got := roleOnMe(t, h, "bob", ""); got != "admin" {
		t.Errorf("bob after the swap: role = %q, want admin", got)
	}
	p.Roles().Store(auth.RoleMap{AdminUsers: []string{"*"}})
	if got := roleOnMe(t, h, "alice", ""); got != "admin" {
		t.Errorf("alice under the wildcard: role = %q, want admin", got)
	}
}

// The action framework is unchanged: Act receives the principal the authenticator mapped, with
// its role, user and groups, and a viewer's request never reaches it.
func TestActorActReceivesTheMappedRole(t *testing.T) {
	e := newActionEnv(t, nil)
	e.d.Auth = withGroupsProxy(auth.RoleMap{AdminUsers: []string{"alice"}, AdminGroups: []string{"ops"}})
	h := d2Chain(e.Server)
	do := func(user, groups string) *httptest.ResponseRecorder {
		hdr := map[string]string{"X-Forwarded-Preferred-Username": user}
		if groups != "" {
			hdr["X-Forwarded-Groups"] = groups
		}
		return req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", header: hdr}.do(h)
	}

	if w := do("alice", ""); w.Code != http.StatusAccepted {
		t.Fatalf("admin ping = %d %s", w.Code, w.Body)
	}
	if w := do("carol", "dev,ops"); w.Code != http.StatusAccepted {
		t.Fatalf("admin-by-group ping = %d %s", w.Code, w.Body)
	}
	if w := do("vera", "dev"); w.Code != http.StatusForbidden || apiError(t, w).Code != model.ErrCodeForbidden {
		t.Fatalf("viewer ping = %d %s, want 403 forbidden", w.Code, w.Body)
	}

	calls := e.acts()
	if len(calls) != 2 {
		t.Fatalf("Act ran %d times, want 2 (the viewer never reaches it): %+v", len(calls), calls)
	}
	for i, want := range []auth.Principal{
		{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin, Via: "X-Forwarded-Preferred-Username"},
		{Kind: auth.KindProxy, User: "carol", Role: auth.RoleAdmin, Via: "X-Forwarded-Preferred-Username", Groups: []string{"dev", "ops"}},
	} {
		got := calls[i].p
		if got.Kind != want.Kind || got.User != want.User || got.Role != want.Role || got.Via != want.Via || !slices.Equal(got.Groups, want.Groups) {
			t.Errorf("Act %d got principal %+v, want %+v", i, got, want)
		}
	}
	var denials []audit.Record
	for _, r := range auditRecords(t, e.dir) {
		if r.Kind == audit.KindDenial {
			denials = append(denials, r)
		}
	}
	if len(denials) != 1 || denials[0].Reason != obs.AuthForbidden.String() || denials[0].Actor.User != "vera" {
		t.Errorf("denials = %+v, want one forbidden denial naming vera", denials)
	}
}
