package auth

import (
	"slices"
	"sync/atomic"
)

// RoleMap maps an authenticated user to a role. Roles come from this mapping, never from the
// credential. AdminRoles is the OIDC mode's: role-claim values that make the holder an
// administrator (auth.oidc.adminRoles); the other modes leave it empty.
type RoleMap struct {
	AdminUsers  []string
	AdminGroups []string
	AdminRoles  []string
}

// RoleFor returns "" (no role) for an empty user. Otherwise the user is admin when AdminUsers
// holds its name exactly (case-sensitive) or "*", or when any of groups is in AdminGroups
// (exact); everyone else is a viewer, so empty lists make every authenticated user a viewer.
func (m RoleMap) RoleFor(user string, groups []string) Role {
	return m.RoleForClaims(user, groups, nil)
}

// RoleForClaims is RoleFor for an identity that also carries role-claim values (OIDC): any
// value of roleClaim in AdminRoles (exact) makes the user an administrator as well.
func (m RoleMap) RoleForClaims(user string, groups, roleClaim []string) Role {
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
	for _, v := range roleClaim {
		if slices.Contains(m.AdminRoles, v) {
			return RoleAdmin
		}
	}
	return RoleViewer
}

// Wildcard reports whether AdminUsers contains "*" (every user is admin).
func (m RoleMap) Wildcard() bool {
	return slices.Contains(m.AdminUsers, "*")
}

// clone copies the lists, so a holder's map shares nothing with its callers.
func (m RoleMap) clone() RoleMap {
	return RoleMap{AdminUsers: slices.Clone(m.AdminUsers), AdminGroups: slices.Clone(m.AdminGroups), AdminRoles: slices.Clone(m.AdminRoles)}
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
	return m.clone()
}

// Store replaces the map with a copy of m.
func (h *RoleHolder) Store(m RoleMap) {
	c := m.clone()
	h.p.Store(&c)
}

// RoleForClaims maps an identity with the current map, without copying it: the stored RoleMap
// is never modified after Store. The session authenticator calls it on every request.
func (h *RoleHolder) RoleForClaims(user string, groups, roleClaim []string) Role {
	m := h.p.Load()
	if m == nil {
		return RoleMap{}.RoleForClaims(user, groups, roleClaim)
	}
	return m.RoleForClaims(user, groups, roleClaim)
}

// Wildcard reports whether the current map makes every user an administrator.
func (h *RoleHolder) Wildcard() bool {
	m := h.p.Load()
	return m != nil && m.Wildcard()
}

// roleFor maps user without copying the map: the stored RoleMap is never modified after Store.
func (h *RoleHolder) roleFor(user string, groups []string) Role {
	return h.RoleForClaims(user, groups, nil)
}
