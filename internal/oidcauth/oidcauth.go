// Package oidcauth is the built-in OIDC login (contract §4.4, P4.4): the authorization code flow
// with PKCE (S256), a state and nonce sealed in a short-lived cookie, ID-token verification
// (signature from the cached JWKS, issuer, audience, expiry, nonce), the claim mapping and the
// fail-closed allow-lists. A successful callback issues a session cookie through
// session.Manager; the session mode's authenticator (internal/session) does the rest. The HTTP
// handlers are in internal/api/authroutes_oidc.go.
//
// Only ID-token claims are used (no UserInfo call). Provider discovery is lazy, with
// DiscoveryTimeout: an unreachable IdP fails /auth/oidc/start with 503 and never stops dash from
// starting.
//
// Scaffold (G4-R2-S13): the signatures are final; the bodies are the oidc slice's (S12). The
// slice adds github.com/coreos/go-oidc/v3 and promotes golang.org/x/oauth2; its exported API
// names no type of either.
package oidcauth

import (
	"errors"
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// Timings and cookie names.
const (
	// DiscoveryTimeout bounds discovery, the JWKS fetch and the token exchange.
	DiscoveryTimeout = 10 * time.Second
	// StateTTL is the life of the state cookie between /auth/oidc/start and /auth/callback.
	StateTTL = 10 * time.Minute
	// StateCookieSecure is the state cookie's name when server.publicURL is https;
	// StateCookiePlain on http. Both are SameSite=Lax: the callback is a cross-site top-level
	// navigation, which a Strict cookie would not be sent on.
	StateCookieSecure = "__Host-dash_oidc"
	StateCookiePlain  = "dash_oidc"
	// CallbackPath is the fixed redirect path under server.publicURL.
	CallbackPath = "/auth/callback"
)

// errSkeleton is what the scaffold's bodies return until the slice implements them.
var errSkeleton = errors.New("oidcauth: not implemented in the scaffold")

// Config is the oidc mode, built by cmd/dash from auth.oidc.* (validated there: the issuer is
// https unless loopback, an allow-list or an explicit "*" is required).
type Config struct {
	Issuer   string
	ClientID string
	// ClientSecret is revealed at each token exchange, so a rotated file applies.
	ClientSecret secret.Value
	Scopes       []string
	// UserClaim names the user (preferred_username by default; email requires email_verified;
	// sub for an IdP whose users can change their name).
	UserClaim string
	// GroupsClaim holds the groups: a list of strings or one comma-separated string.
	GroupsClaim string
	// RoleClaim holds role values matched against Roles' AdminRoles; "" turns it off.
	RoleClaim string
	// AllowedUsers (exact) and AllowedGroups (any) admit an account; "*" in AllowedUsers admits
	// every account the IdP signs in. Anything else is refused: /login?error=not_allowed.
	AllowedUsers  []string
	AllowedGroups []string
	// Roles is the live mapping (auth.oidc adminUsers, adminGroups, adminRoles). The session keeps
	// only the groups of AdminGroups and AllowedGroups and the role values of AdminRoles.
	Roles *auth.RoleHolder
	// RedirectURL is server.publicURL + CallbackPath.
	RedirectURL string
	// Secure is true when server.publicURL is https (the cookie names and Secure attribute).
	Secure bool
	// HTTPClient talks to the IdP; nil = a client with DiscoveryTimeout.
	HTTPClient *http.Client
	Now        func() time.Time // nil = time.Now
}

// Provider is the OIDC login of one configured IdP. Safe for concurrent use.
type Provider struct{}

// New returns the provider of cfg. It makes no network call: discovery happens on the first
// login. sessions issues the session cookie; state seals the state cookie (a codec whose key is
// derived for session.PurposeOIDCState).
func New(cfg Config, sessions *session.Manager, state *session.Codec) (*Provider, error) {
	return nil, errSkeleton
}

// IssuerHost is the host of the issuer URL, for the 401 envelope's details.oidc.issuer.
func (p *Provider) IssuerHost() string { return "" }
