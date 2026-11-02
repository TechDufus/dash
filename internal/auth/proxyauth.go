package auth

import (
	"log/slog"
	"net/http"
	"time"
)

// Proxy authenticates requests that an identity proxy (oauth2-proxy, Authelia, ...) forwarded.
// Identity headers are believed only when the TCP peer is in the trusted set (never
// X-Forwarded-For), and the role comes from the RoleMap, never from the headers. The role map
// sits behind an atomic pointer (Roles), so a live config reload swaps it without a lock on the
// request path.
//
// Authenticate is a skeleton in the scaffold commit: it refuses every request, and nothing
// calls it until the request chain is rewired (P4.1).
type Proxy struct {
	trust Trust
	id    ProxyIdentity
	roles RoleHolder
	log   *slog.Logger
	now   func() time.Time
}

// ProxyOption adjusts a Proxy under construction.
type ProxyOption func(*Proxy)

// WithLogger sets where the proxy authenticator logs its rate-limited warnings (disagreeing user
// headers). The default discards them.
func WithLogger(l *slog.Logger) ProxyOption {
	return func(p *Proxy) {
		if l != nil {
			p.log = l
		}
	}
}

// WithClock sets the clock that spaces the warnings, for tests.
func WithClock(now func() time.Time) ProxyOption {
	return func(p *Proxy) {
		if now != nil {
			p.now = now
		}
	}
}

// NewProxy builds the authenticator of auth.mode=proxy: trust is the peer set whose headers are
// believed, id names the headers, roles is the initial user-to-role mapping.
func NewProxy(trust Trust, id ProxyIdentity, roles RoleMap, opts ...ProxyOption) *Proxy {
	p := &Proxy{trust: trust, id: id, log: slog.New(slog.DiscardHandler), now: time.Now}
	p.roles.Store(roles)
	for _, o := range opts {
		o(p)
	}
	return p
}

// Roles is the holder of the live role map: Store replaces it, and the next request is mapped
// with the new one.
func (p *Proxy) Roles() *RoleHolder { return &p.roles }

// Authenticate implements Authenticator.
func (p *Proxy) Authenticate(r *http.Request) (Principal, *Failure) {
	return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Reason: ReasonNoIdentity}
}

var _ Authenticator = (*Proxy)(nil)
