package auth

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
