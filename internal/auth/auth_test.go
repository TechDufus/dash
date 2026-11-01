package auth

import "testing"

func TestRoleAtLeast(t *testing.T) {
	for _, c := range []struct {
		r, min Role
		want   bool
	}{
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleViewer, true},
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleAdmin, false},
		{"", RoleViewer, false},
		{RoleAdmin, "", false},
		{"owner", RoleViewer, false},
	} {
		if got := c.r.AtLeast(c.min); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.r, c.min, got, c.want)
		}
	}
}
