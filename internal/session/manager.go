package session

import (
	"errors"
	"net/http"
	"slices"
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
type Manager struct {
	codec  *Codec
	epochs Epochs
	secure bool
	now    func() time.Time
}

// errUnconfigured is what a manager without a codec or epochs answers: it fails closed.
var errUnconfigured = errors.New("session: manager without a codec or epochs")

// NewManager returns the manager of o.
func NewManager(o Options) *Manager {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{codec: o.Codec, epochs: o.Epochs, secure: o.Secure, now: now}
}

// CookieName is the session cookie's name for a public URL scheme.
func CookieName(secure bool) string {
	if secure {
		return CookieSecure
	}
	return CookiePlain
}

// CookieName is the name of the cookies this manager writes.
func (m *Manager) CookieName() string { return CookieName(m.secure) }

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

// kept is the values of have that are in keep (exact), in the order of have, without repeats.
func kept(have, keep []string) []string {
	var out []string
	for _, v := range have {
		if slices.Contains(keep, v) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// Apply is id as a session keeps it: the groups and role-claim values of k only. Login records
// the same on the user's tokens (design M.8).
func (k Keep) Apply(id Identity) Identity {
	return Identity{User: id.User, Groups: kept(id.Groups, k.Groups), RoleClaim: kept(id.RoleClaim, k.RoleClaim)}
}

// Issue seals a new session for id, filtered by keep, with iat now, exp now+IdleTimeout and the
// current epochs of the user and of everyone, and returns the cookie to set. ErrTooLarge when
// the sealed value would exceed MaxSealedBytes: the caller refuses the login.
func (m *Manager) Issue(id Identity, keep Keep) (*http.Cookie, error) {
	if m.codec == nil || m.epochs == nil {
		return nil, errUnconfigured
	}
	if id.User == "" {
		return nil, errors.New("session: a session needs a user")
	}
	now := m.now()
	kid := keep.Apply(id)
	p := Payload{
		User:        id.User,
		Groups:      kid.Groups,
		RoleClaim:   kid.RoleClaim,
		IssuedAt:    now.Unix(),
		ExpiresAt:   now.Add(IdleTimeout).Unix(),
		UserEpoch:   m.epochs.Epoch(id.User),
		GlobalEpoch: m.epochs.GlobalEpoch(),
	}
	return m.cookie(p, now)
}

// cookie seals p into the session cookie, valid in the browser until p's exp.
func (m *Manager) cookie(p Payload, now time.Time) (*http.Cookie, error) {
	v, err := m.codec.Seal(p)
	if err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name:     m.CookieName(),
		Value:    v,
		Path:     "/",
		MaxAge:   int(p.ExpiresAt - now.Unix()),
		Secure:   m.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, nil
}

// Read returns the session of r. err is ErrNoSession without a cookie, and ErrInvalid,
// ErrExpired (past exp or iat+MaxLifetime) or ErrRevoked (ep or ge not current) for a cookie that
// must be cleared. With a valid session in the second half of its idle window, renew is the
// re-issued cookie (exp = min(now+IdleTimeout, iat+MaxLifetime), same iat and epochs); nil
// otherwise.
func (m *Manager) Read(r *http.Request) (p Payload, renew *http.Cookie, err error) {
	cs := r.CookiesNamed(m.CookieName())
	switch {
	case len(cs) == 0:
		return Payload{}, nil, ErrNoSession
	case len(cs) > 1:
		// Two cookies of the name: one was planted (cookie tossing on the plain-http name).
		// Neither is believed.
		return Payload{}, nil, ErrInvalid
	}
	if m.codec == nil || m.epochs == nil {
		return Payload{}, nil, errUnconfigured
	}
	if err := m.codec.Open(cs[0].Value, &p); err != nil {
		return Payload{}, nil, ErrInvalid
	}
	limit := p.IssuedAt + int64(MaxLifetime/time.Second)
	if p.User == "" || p.IssuedAt <= 0 || p.ExpiresAt <= p.IssuedAt || p.ExpiresAt > limit {
		return Payload{}, nil, ErrInvalid
	}
	now := m.now()
	n := now.Unix()
	// exp never passes iat+MaxLifetime (checked above), so it is the absolute limit as well.
	if n >= p.ExpiresAt {
		return Payload{}, nil, ErrExpired
	}
	if p.UserEpoch != m.epochs.Epoch(p.User) || p.GlobalEpoch != m.epochs.GlobalEpoch() {
		return Payload{}, nil, ErrRevoked
	}
	// The second half of the idle window: at most IdleTimeout/2 left before exp.
	if p.ExpiresAt-n <= int64(IdleTimeout/2/time.Second) {
		next := p
		next.ExpiresAt = min(now.Add(IdleTimeout).Unix(), limit)
		if next.ExpiresAt > p.ExpiresAt {
			if c, err := m.cookie(next, now); err == nil {
				return next, c, nil
			}
		}
	}
	return p, nil, nil
}

// Clear is the cookie that deletes the session cookie (same name, path and attributes, Max-Age
// negative).
func (m *Manager) Clear() *http.Cookie {
	return &http.Cookie{
		Name:     m.CookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0).UTC(),
		Secure:   m.secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}
