package session

import (
	"net/http"
	"time"
)

// Epochs is where the session epochs live; *credentials.Store implements it.
type Epochs interface {
	Epoch(user string) uint64
	GlobalEpoch() uint64
}

// Options configure a Manager.
type Options struct {
	Codec  *Codec // sealed with the PurposeSession key
	Epochs Epochs
	// Secure is true when server.publicURL is https: the cookie is CookieSecure with the Secure
	// attribute. It follows the public URL, not the connection (a TLS-terminating proxy in front
	// of plain-http dash still gets the __Host- cookie).
	Secure bool
	Now    func() time.Time // nil = time.Now
}

// Manager issues, reads and clears session cookies. Every cookie it writes is HttpOnly,
// SameSite=Lax (not Strict: the OIDC callback is a cross-site top-level navigation), Path=/,
// without Domain, and Secure when Options.Secure.
type Manager struct{}

// NewManager returns the manager of o.
func NewManager(o Options) *Manager { return &Manager{} }

// CookieName is the session cookie's name for a public URL scheme.
func CookieName(secure bool) string {
	if secure {
		return CookieSecure
	}
	return CookiePlain
}

// CookieName is the name of the cookies this manager writes.
func (m *Manager) CookieName() string { return "" }

// Identity is who a new session is for.
type Identity struct {
	User      string
	Groups    []string // as the identity provider sent them
	RoleClaim []string // the role-claim values (OIDC)
}

// Keep is what a session may carry of an identity: the groups of adminGroups and allowedGroups,
// and the role-claim values of adminRoles. Issue drops the rest (contract D8, G4-R2-S7), so a
// group added to adminGroups applies at the user's next login and a removed one at once.
type Keep struct {
	Groups    []string
	RoleClaim []string
}

// Issue seals a new session for id, filtered by keep, with iat now, exp now+IdleTimeout and the
// current epochs of the user and of everyone, and returns the cookie to set. ErrTooLarge when
// the sealed value would exceed MaxSealedBytes: the caller refuses the login.
func (m *Manager) Issue(id Identity, keep Keep) (*http.Cookie, error) { return nil, errSkeleton }

// Read returns the session of r. err is ErrNoSession without a cookie, and ErrInvalid,
// ErrExpired (past exp or iat+MaxLifetime) or ErrRevoked (ep or ge not current) for a cookie that
// must be cleared. With a valid session in the second half of its idle window, renew is the
// re-issued cookie (exp = min(now+IdleTimeout, iat+MaxLifetime), same iat and epochs); nil
// otherwise.
func (m *Manager) Read(r *http.Request) (p Payload, renew *http.Cookie, err error) {
	return Payload{}, nil, ErrNoSession
}

// Clear is the cookie that deletes the session cookie (same name, path and attributes, Max-Age
// negative).
func (m *Manager) Clear() *http.Cookie { return nil }
