package auth

import (
	"slices"
	"sync/atomic"
)

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

// RoleHolder is the live RoleMap of a running authenticator. The zero value holds the zero
// RoleMap (every user a viewer). Store replaces the map for the next request; Load is safe on
// the request path without a lock.
type RoleHolder struct{ p atomic.Pointer[RoleMap] }

// NewRoleHolder holds m.
func NewRoleHolder(m RoleMap) *RoleHolder {
	h := &RoleHolder{}
	h.Store(m)
	return h
}

// Load returns the current map (a copy: callers cannot change what other requests see).
func (h *RoleHolder) Load() RoleMap {
	m := h.p.Load()
	if m == nil {
		return RoleMap{}
	}
	return RoleMap{AdminUsers: slices.Clone(m.AdminUsers), AdminGroups: slices.Clone(m.AdminGroups)}
}

// Store replaces the map with a copy of m.
func (h *RoleHolder) Store(m RoleMap) {
	h.p.Store(&RoleMap{AdminUsers: slices.Clone(m.AdminUsers), AdminGroups: slices.Clone(m.AdminGroups)})
}
