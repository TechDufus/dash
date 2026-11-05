package auth

import (
	"slices"
	"strings"
)

// Principal is who a request acts as. Role is derived from a mapping (see RoleMap), never taken
// from the credential. The token and scope fields are declared for bearer tokens and are empty
// for every interactive kind. Principal holds slices, so it is not comparable with ==.
type Principal struct {
	Kind Kind
	User string
	Role Role
	// Via is what carried the identity: the header name, "cookie" or "fixed". Only the probe
	// logs it.
	Via              string
	Groups           []string
	RoleClaim        []string
	TokenID          string
	GrantID          string
	ClientID         string
	Audience         string
	Scopes           []string
	ActionNamespaces []string
}

// Interactive reports whether the principal is a person at a browser or behind an identity
// proxy: proxy, session, dev, demo and none. Tokens, the anonymous viewer and an unknown or empty
// kind are not interactive.
func (p Principal) Interactive() bool {
	switch p.Kind {
	case KindProxy, KindSession, KindDev, KindDemo, KindNone:
		return true
	}
	return false
}

// HasScope reports whether a token principal holds scope (design M.3). Every other kind holds
// every scope its role allows, which the role checks decide, so it reports true for them.
func (p Principal) HasScope(scope string) bool {
	if p.Kind != KindToken {
		return true
	}
	return slices.Contains(p.Scopes, scope)
}

// ActionNamespaceAllowed reports whether a token principal's actionNamespaces admit ns: an
// empty list admits every namespace, otherwise an entry must name ns exactly or be a prefix*
// glob it starts with. A non-empty list restricts the token to those namespaces, so an action
// without a namespace (a silence, a cluster-wide target) is refused unless the list holds "*".
// Other kinds have no such list.
func (p Principal) ActionNamespaceAllowed(ns string) bool {
	if p.Kind != KindToken || len(p.ActionNamespaces) == 0 {
		return true
	}
	if ns == "" {
		return slices.Contains(p.ActionNamespaces, "*")
	}
	for _, e := range p.ActionNamespaces {
		if prefix, glob := strings.CutSuffix(e, "*"); glob && strings.HasPrefix(ns, prefix) || e == ns {
			return true
		}
	}
	return false
}
