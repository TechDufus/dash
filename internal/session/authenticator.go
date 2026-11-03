package session

import (
	"net/http"

	"github.com/TechDufus/dash/internal/auth"
)

// AuthOptions configure the authenticator of a session mode.
type AuthOptions struct {
	// Mode is auth.ModeLocal or auth.ModeOIDC, reported through auth.Moder.
	Mode     string
	Sessions *Manager
	// Roles is the live mapping applied to every request's user, groups and role-claim values:
	// local holds {AdminUsers: [auth.local.adminUser]}; oidc the auth.oidc admin lists and
	// adminRoles, which a reload replaces.
	Roles *auth.RoleHolder
	// Anonymous turns on the anonymous viewer (auth.local.anonymousViewer; local mode only).
	Anonymous bool
	// Warnings are standing warnings of the mode's configuration (oidc_open), reported through
	// auth.Warner with admin_wildcard (from Roles) and anonymous_viewer (from Anonymous).
	Warnings []string
}

// Authenticator is the authenticator of auth.mode=local and auth.mode=oidc. The order (contract
// D3): an Authorization Bearer line is refused 401 invalid_token (reason bearer_refused) and
// nothing else is read; a valid session cookie is a Principal{Kind: session, Via: "cookie"} with
// the cookie's user, groups and role-claim values and the role Roles maps them to (never one from
// the cookie); a cookie that fails is cleared and the request continues without it; then, with
// Anonymous, a GET or HEAD is Principal{Kind: anonymous, User: "anonymous", Role: viewer,
// Via: "anonymous"}; everything else is 401 unauthenticated (reason no_identity).
type Authenticator struct{}

// NewAuthenticator returns the authenticator of o.
func NewAuthenticator(o AuthOptions) *Authenticator { return &Authenticator{} }

// Authenticate implements auth.Authenticator.
func (a *Authenticator) Authenticate(r *http.Request) (auth.Principal, *auth.Failure) {
	p, _, f := a.AuthenticateCookie(r)
	return p, f
}

// AuthenticateCookie implements auth.CookieAuthenticator: the cookie is the renewal of a valid
// session in the second half of its idle window, or the clearing cookie of one that failed.
func (a *Authenticator) AuthenticateCookie(r *http.Request) (auth.Principal, *http.Cookie, *auth.Failure) {
	return auth.Principal{}, nil, &auth.Failure{Status: http.StatusUnauthorized, Code: auth.CodeUnauthenticated, Reason: auth.ReasonNoIdentity}
}

// Mode implements auth.Moder.
func (a *Authenticator) Mode() string { return "" }

// Warnings implements auth.Warner.
func (a *Authenticator) Warnings() []string { return nil }

var (
	_ auth.CookieAuthenticator = (*Authenticator)(nil)
	_ auth.Moder               = (*Authenticator)(nil)
	_ auth.Warner              = (*Authenticator)(nil)
)
