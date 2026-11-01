// Package auth is the identity the request chain and plugins act with. G2 resolves only the
// interactive kinds; P4.2 adds fields to Principal (groups, tokenID, scopes, …) without
// changing the ones below.
package auth

// Role is what a principal may do. Roles are ordered: admin includes everything viewer does.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleAdmin  Role = "admin"
)

// AtLeast reports whether r grants everything min does (admin ⊇ viewer). An unknown role
// grants nothing, and nothing is granted by it: AtLeast is false whenever either side is not a
// known role.
func (r Role) AtLeast(min Role) bool {
	rr, ok := rank(r)
	mr, ok2 := rank(min)
	return ok && ok2 && rr >= mr
}

func rank(r Role) (int, bool) {
	switch r {
	case RoleViewer:
		return 1, true
	case RoleAdmin:
		return 2, true
	}
	return 0, false
}

// Kind says how a principal was established.
type Kind string

const (
	KindProxy Kind = "proxy" // forwarded headers behind the identity proxy (today's mode)
	KindDev   Kind = "dev"   // DASH_DEV
	KindDemo  Kind = "demo"  // DASH_DEMO
)

// Principal is who a request acts as. G2 gives every authenticated principal RoleAdmin,
// preserving today's behaviour (design E.1); P4.2 replaces that with the role mapping.
type Principal struct {
	Kind Kind
	User string
	Role Role
}
