package session

import (
	"errors"
	"net/http"
	"slices"

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
	// SetupNeeded, when set, reports that the local account does not exist yet
	// (localauth.Local.SetupNeeded). While it is true the anonymous viewer is not granted: the 401
	// carries details.setup, so the SPA points to the setup link instead of a login form nobody
	// can use yet.
	SetupNeeded func() bool
	// Warnings are standing warnings of the mode's configuration (oidc_open), reported through
	// auth.Warner with admin_wildcard (from Roles) and anonymous_viewer (from Anonymous).
	Warnings []string
}

// Authenticator is the authenticator of auth.mode=local and auth.mode=oidc. The order (contract
// D3): an Authorization Bearer line is refused 401 invalid_token (reason bearer_refused) and
// nothing else is read; a valid session cookie is a Principal{Kind: session, Via: "cookie"} with
// the cookie's user, groups and role-claim values and the role Roles maps them to (never one from
// the cookie); a cookie that fails is cleared and the request continues without it; then, with
// Anonymous and unless SetupNeeded reports true, a GET or HEAD is Principal{Kind: anonymous, User:
// "anonymous", Role: viewer, Via: "anonymous"}; everything else is 401 unauthenticated (reason
// no_identity).
type Authenticator struct {
	mode     string
	sessions *Manager
	roles    *auth.RoleHolder
	// anonymous is AuthOptions.Anonymous in local mode only: the viewer is never on in oidc.
	anonymous   bool
	setupNeeded func() bool
	warnings    []string
}

// NewAuthenticator returns the authenticator of o.
func NewAuthenticator(o AuthOptions) *Authenticator {
	roles := o.Roles
	if roles == nil {
		roles = &auth.RoleHolder{} // maps everyone to viewer
	}
	return &Authenticator{
		mode:        o.Mode,
		sessions:    o.Sessions,
		roles:       roles,
		anonymous:   o.Anonymous && o.Mode == auth.ModeLocal,
		warnings:    slices.Clone(o.Warnings),
		setupNeeded: o.SetupNeeded,
	}
}

// Authenticate implements auth.Authenticator.
func (a *Authenticator) Authenticate(r *http.Request) (auth.Principal, *auth.Failure) {
	p, _, f := a.AuthenticateCookie(r)
	return p, f
}

// AuthenticateCookie implements auth.CookieAuthenticator: the cookie is the renewal of a valid
// session in the second half of its idle window, or the clearing cookie of one that failed.
func (a *Authenticator) AuthenticateCookie(r *http.Request) (auth.Principal, *http.Cookie, *auth.Failure) {
	if _, ok := auth.Bearer(r); ok {
		return auth.Principal{}, nil, &auth.Failure{Status: http.StatusUnauthorized, Code: auth.CodeInvalidToken, Reason: auth.ReasonBearerRefused}
	}
	var clear *http.Cookie
	if a.sessions != nil {
		s, renew, err := a.sessions.Read(r)
		if err == nil {
			if role := a.roles.RoleForClaims(s.User, s.Groups, s.RoleClaim); role != "" {
				return auth.Principal{
					Kind:      auth.KindSession,
					User:      s.User,
					Role:      role,
					Via:       "cookie",
					Groups:    s.Groups,
					RoleClaim: s.RoleClaim,
				}, renew, nil
			}
			err = ErrInvalid
		}
		if !errors.Is(err, ErrNoSession) {
			clear = a.sessions.Clear()
		}
	}
	if a.anonymous && (r.Method == http.MethodGet || r.Method == http.MethodHead) && (a.setupNeeded == nil || !a.setupNeeded()) {
		return auth.Principal{Kind: auth.KindAnonymous, User: "anonymous", Role: auth.RoleViewer, Via: "anonymous"}, clear, nil
	}
	return auth.Principal{}, clear, &auth.Failure{Status: http.StatusUnauthorized, Code: auth.CodeUnauthenticated, Reason: auth.ReasonNoIdentity}
}

// Mode implements auth.Moder.
func (a *Authenticator) Mode() string { return a.mode }

// Warnings implements auth.Warner: admin_wildcard while Roles makes every user an administrator,
// anonymous_viewer while the anonymous viewer is on, then the configured ones.
func (a *Authenticator) Warnings() []string {
	var out []string
	if a.roles.Wildcard() {
		out = append(out, auth.WarningAdminWildcard)
	}
	if a.anonymous {
		out = append(out, auth.WarningAnonymousViewer)
	}
	for _, w := range a.warnings {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

var (
	_ auth.CookieAuthenticator = (*Authenticator)(nil)
	_ auth.Moder               = (*Authenticator)(nil)
	_ auth.Warner              = (*Authenticator)(nil)
)
