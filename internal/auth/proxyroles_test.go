package auth

import (
	"slices"
	"testing"
)

func groupsIdentity() ProxyIdentity {
	return ProxyIdentity{UserHeaders: testIdentity.UserHeaders, GroupsHeader: "X-Forwarded-Groups"}
}

func TestAuthenticateMapsTheRoleFromTheRoleMap(t *testing.T) {
	p := NewProxy(loopbackOnly, groupsIdentity(), RoleMap{AdminUsers: []string{"Bob"}, AdminGroups: []string{"ops"}})
	for _, tc := range []struct {
		name string
		kv   []string
		want Role
	}{
		{"listed user", []string{"X-Forwarded-User", "Bob"}, RoleAdmin},
		{"user match is case-sensitive", []string{"X-Forwarded-User", "bob"}, RoleViewer},
		{"unlisted user", []string{"X-Forwarded-User", "dash-verify"}, RoleViewer},
		{"admin group", []string{"X-Forwarded-User", "dash-verify", "X-Forwarded-Groups", "dev,ops"}, RoleAdmin},
		{"group match is exact", []string{"X-Forwarded-User", "dash-verify", "X-Forwarded-Groups", "Ops,ops-team"}, RoleViewer},
		// The role is never read from the request.
		{"a role header grants nothing", []string{"X-Forwarded-User", "dash-verify", "X-Forwarded-Role", "admin", "X-Dash-Role", "admin"}, RoleViewer},
	} {
		got, f := p.Authenticate(request("127.0.0.1:1", tc.kv...))
		if f != nil || got.Role != tc.want {
			t.Errorf("%s: role %q (%+v), want %q", tc.name, got.Role, f, tc.want)
		}
	}
}

func TestEmptyRoleMapMakesEveryAuthenticatedUserAViewer(t *testing.T) {
	for name, m := range map[string]RoleMap{"zero": {}, "empty lists": {AdminUsers: []string{}, AdminGroups: []string{}}} {
		p := NewProxy(loopbackOnly, groupsIdentity(), m)
		got, f := p.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice", "X-Forwarded-Groups", "admins,ops"))
		if f != nil || got.Role != RoleViewer {
			t.Errorf("%s: %+v %+v, want a viewer", name, got, f)
		}
	}
}

func TestRoleHolderStoreAppliesToTheNextAuthenticate(t *testing.T) {
	p := NewProxy(loopbackOnly, groupsIdentity(), RoleMap{AdminUsers: []string{"alice"}})
	role := func(user string) Role {
		t.Helper()
		got, f := p.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", user))
		if f != nil {
			t.Fatal(f)
		}
		return got.Role
	}
	if role("alice") != RoleAdmin || role("bob") != RoleViewer {
		t.Fatal("initial map not applied")
	}
	p.Roles().Store(RoleMap{AdminUsers: []string{"bob"}})
	if role("alice") != RoleViewer || role("bob") != RoleAdmin {
		t.Error("the stored map did not apply to the next request")
	}
}

func TestProxyWarnings(t *testing.T) {
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"alice"}})
	var w Warner = p
	if got := w.Warnings(); len(got) != 0 {
		t.Errorf("warnings = %q, want none for an exact list", got)
	}
	p.Roles().Store(RoleMap{AdminUsers: []string{"alice", "*"}})
	if got := w.Warnings(); !slices.Equal(got, []string{"admin_wildcard"}) {
		t.Errorf("warnings = %q, want [admin_wildcard]", got)
	}
	// A wildcard in the group list is just a group named "*": it is not the admin wildcard.
	p.Roles().Store(RoleMap{AdminGroups: []string{"*"}})
	if got := w.Warnings(); len(got) != 0 {
		t.Errorf("warnings = %q, want none: only adminUsers has a wildcard", got)
	}
}
