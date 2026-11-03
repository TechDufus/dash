package auth

import "testing"

func TestRoleFor(t *testing.T) {
	for _, c := range []struct {
		name   string
		m      RoleMap
		user   string
		groups []string
		want   Role
	}{
		{"both empty is viewer", RoleMap{}, "alice", nil, RoleViewer},
		{"both empty with groups is viewer", RoleMap{}, "alice", []string{"ops"}, RoleViewer},
		{"admin user exact", RoleMap{AdminUsers: []string{"alice"}}, "alice", nil, RoleAdmin},
		{"admin among several", RoleMap{AdminUsers: []string{"bob", "alice"}}, "alice", nil, RoleAdmin},
		{"other user is viewer", RoleMap{AdminUsers: []string{"alice"}}, "bob", nil, RoleViewer},
		{"case-sensitive user", RoleMap{AdminUsers: []string{"Alice"}}, "alice", nil, RoleViewer},
		{"case-sensitive user reverse", RoleMap{AdminUsers: []string{"alice"}}, "Alice", nil, RoleViewer},
		{"prefix is not a match", RoleMap{AdminUsers: []string{"alice"}}, "alice2", nil, RoleViewer},
		{"user with space is not trimmed", RoleMap{AdminUsers: []string{"alice"}}, "alice ", nil, RoleViewer},
		{"admin group", RoleMap{AdminGroups: []string{"ops"}}, "bob", []string{"dev", "ops"}, RoleAdmin},
		{"case-sensitive group", RoleMap{AdminGroups: []string{"Ops"}}, "bob", []string{"ops"}, RoleViewer},
		{"no matching group", RoleMap{AdminGroups: []string{"ops"}}, "bob", []string{"dev"}, RoleViewer},
		{"groups nil", RoleMap{AdminGroups: []string{"ops"}}, "bob", nil, RoleViewer},
		{"group list does not name users", RoleMap{AdminGroups: []string{"bob"}}, "bob", nil, RoleViewer},
		{"user list does not name groups", RoleMap{AdminUsers: []string{"ops"}}, "bob", []string{"ops"}, RoleViewer},
		{"user or group", RoleMap{AdminUsers: []string{"alice"}, AdminGroups: []string{"ops"}}, "alice", []string{"dev"}, RoleAdmin},
		{"wildcard", RoleMap{AdminUsers: []string{"*"}}, "anyone", nil, RoleAdmin},
		{"wildcard among others", RoleMap{AdminUsers: []string{"bob", "*"}}, "anyone", nil, RoleAdmin},
		{"wildcard in groups is literal", RoleMap{AdminGroups: []string{"*"}}, "anyone", []string{"ops"}, RoleViewer},
		{"empty user no role", RoleMap{AdminUsers: []string{"alice"}}, "", nil, ""},
		{"empty user no role even with admin group", RoleMap{AdminGroups: []string{"ops"}}, "", []string{"ops"}, ""},
		{"wildcard with empty user still no role", RoleMap{AdminUsers: []string{"*"}}, "", nil, ""},
		{"empty user, empty map", RoleMap{}, "", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.m.RoleFor(c.user, c.groups); got != c.want {
				t.Errorf("RoleFor(%q, %v) = %q, want %q", c.user, c.groups, got, c.want)
			}
		})
	}
}

func TestRoleMapWildcard(t *testing.T) {
	for _, c := range []struct {
		m    RoleMap
		want bool
	}{
		{RoleMap{}, false},
		{RoleMap{AdminUsers: []string{"alice"}}, false},
		{RoleMap{AdminUsers: []string{"alice", "*"}}, true},
		{RoleMap{AdminUsers: []string{"*"}}, true},
		{RoleMap{AdminGroups: []string{"*"}}, false},
	} {
		if got := c.m.Wildcard(); got != c.want {
			t.Errorf("%+v.Wildcard() = %v, want %v", c.m, got, c.want)
		}
	}
}

// The role claim of an OIDC identity grants admin only through AdminRoles, exactly; RoleFor never
// looks at AdminRoles, so a proxy or local mapping cannot be widened by it.
func TestRoleForClaims(t *testing.T) {
	m := RoleMap{AdminUsers: []string{"alice"}, AdminGroups: []string{"ops"}, AdminRoles: []string{"admin"}}
	for _, c := range []struct {
		name              string
		user              string
		groups, roleClaim []string
		want              Role
	}{
		{"role claim value grants admin", "bob", nil, []string{"reader", "admin"}, RoleAdmin},
		{"role claim is case-sensitive", "bob", nil, []string{"Admin"}, RoleViewer},
		{"no role claim is viewer", "bob", nil, nil, RoleViewer},
		{"user still grants admin", "alice", nil, nil, RoleAdmin},
		{"group still grants admin", "bob", []string{"ops"}, nil, RoleAdmin},
		{"a group is not a role value", "bob", []string{"admin"}, nil, RoleViewer},
		{"empty user has no role", "", nil, []string{"admin"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := m.RoleForClaims(c.user, c.groups, c.roleClaim); got != c.want {
				t.Errorf("RoleForClaims(%q, %v, %v) = %q, want %q", c.user, c.groups, c.roleClaim, got, c.want)
			}
		})
	}
	if got := (RoleMap{AdminRoles: []string{"admin"}}).RoleFor("bob", []string{"admin"}); got != RoleViewer {
		t.Errorf("RoleFor used AdminRoles: %q", got)
	}
}

// The holder maps with the map last stored, AdminRoles included, and copies what it is given.
func TestRoleHolderClaimsAndWildcard(t *testing.T) {
	var h RoleHolder
	if h.Wildcard() || h.RoleForClaims("bob", nil, []string{"admin"}) != RoleViewer {
		t.Fatal("the zero holder must map everyone to viewer")
	}
	roles := []string{"admin"}
	h.Store(RoleMap{AdminRoles: roles})
	roles[0] = "changed"
	if got := h.RoleForClaims("bob", nil, []string{"admin"}); got != RoleAdmin {
		t.Errorf("after Store: %q, want admin (the holder must copy AdminRoles)", got)
	}
	if got := h.Load().AdminRoles; len(got) != 1 || got[0] != "admin" {
		t.Errorf("Load().AdminRoles = %q", got)
	}
	h.Store(RoleMap{AdminUsers: []string{"*"}})
	if !h.Wildcard() {
		t.Error("Wildcard() is false after storing [\"*\"]")
	}
}
