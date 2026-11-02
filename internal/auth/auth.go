// Package auth holds the identity primitives of the request chain: Role and Kind, the Principal
// a request acts as (principal.go), the Authenticator contract and bearer extraction
// (authn.go), the trusted-peer set (trust.go), proxy identity headers (proxy.go) and the
// user-to-role mapping (roles.go). It is pure: it imports neither config nor the model, and it
// never reads X-Forwarded-For.
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
	KindProxy     Kind = "proxy"     // forwarded identity headers from a trusted peer
	KindSession   Kind = "session"   // session cookie (local and oidc modes)
	KindToken     Kind = "token"     // bearer token (non-interactive)
	KindAnonymous Kind = "anonymous" // the anonymous viewer (local mode)
	KindDev       Kind = "dev"       // DASH_DEV fixed identity
	KindDemo      Kind = "demo"      // DASH_DEMO fixed identity
	KindNone      Kind = "none"      // auth.mode none fixed identity
)
