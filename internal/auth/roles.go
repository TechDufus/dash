package auth

import "slices"

// RoleMap maps an authenticated user to a role. Roles come from this mapping, never from the
// credential.
type RoleMap struct {
	AdminUsers  []string
	AdminGroups []string
}

// RoleFor returns "" (no role) for an empty user. Otherwise the user is admin when AdminUsers
// holds its name exactly (case-sensitive) or "*", or when any of groups is in AdminGroups
// (exact); everyone else is a viewer, so empty lists make every authenticated user a viewer.
func (m RoleMap) RoleFor(user string, groups []string) Role {
	if user == "" {
		return ""
	}
	if m.Wildcard() || slices.Contains(m.AdminUsers, user) {
		return RoleAdmin
	}
	for _, g := range groups {
		if slices.Contains(m.AdminGroups, g) {
			return RoleAdmin
		}
	}
	return RoleViewer
}

// Wildcard reports whether AdminUsers contains "*" (every user is admin).
func (m RoleMap) Wildcard() bool {
	return slices.Contains(m.AdminUsers, "*")
}
