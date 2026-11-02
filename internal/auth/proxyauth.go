package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// warnEvery spaces the warnings the authenticator logs about requests an operator should look at.
const warnEvery = time.Minute

// warnGate lets one event through per warnEvery. The zero value lets the first one through.
type warnGate struct {
	mu    sync.Mutex
	last  time.Time
	fired bool
}

func (g *warnGate) allow(now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fired && now.Sub(g.last) < warnEvery {
		return false
	}
	g.fired, g.last = true, now
	return true
}

// Proxy authenticates requests that an identity proxy (oauth2-proxy, Authelia, ...) forwarded.
// Identity headers are believed only when the TCP peer is in the trusted set (never
// X-Forwarded-For), and the role comes from the RoleMap, never from the headers. The role map
// sits behind an atomic pointer (Roles), so a live config reload swaps it without a lock on the
// request path.
type Proxy struct {
	trust Trust
	id    ProxyIdentity
	roles RoleHolder
	log   *slog.Logger
	now   func() time.Time

	untrustedWarn warnGate
	disagreeWarn  warnGate
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

// PeerTruster is implemented by authenticators that believe the headers of a set of peers.
type PeerTruster interface {
	// TrustsPeer reports whether the TCP peer of r is in that set.
	TrustsPeer(r *http.Request) bool
}

// TrustsPeer implements PeerTruster.
func (p *Proxy) TrustsPeer(r *http.Request) bool {
	a, ok := PeerOf(r)
	return ok && p.trust.Trusts(a)
}

// Authenticate implements Authenticator. The order is the one of the design: a Bearer credential
// is refused first and the identity headers play no part in it; then the TCP peer must be
// trusted before any identity header is read; then the first user header names the user.
func (p *Proxy) Authenticate(r *http.Request) (Principal, *Failure) {
	if _, ok := Bearer(r); ok {
		return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeInvalidToken, Reason: ReasonBearerRefused}
	}
	peer, ok := PeerOf(r)
	if !ok || !p.trust.Trusts(peer) {
		if !p.carriesIdentity(r.Header) {
			return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Reason: ReasonNoIdentity}
		}
		if p.untrustedWarn.allow(p.now()) {
			from := r.RemoteAddr
			if ok {
				from = peer.String()
			}
			p.log.Warn("identity headers from a peer outside auth.proxy.trustedProxies", "peer", from)
		}
		return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Reason: ReasonUntrustedPeer}
	}
	user, groups, via, err := p.id.From(r.Header)
	if errors.Is(err, ErrAmbiguousIdentity) {
		return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Reason: ReasonAmbiguousIdentity}
	}
	if err != nil || user == "" {
		return Principal{}, &Failure{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Reason: ReasonNoIdentity}
	}
	p.warnIfDisagree(r.Header, user, via)
	return Principal{Kind: KindProxy, User: user, Role: p.roles.roleFor(user, groups), Via: via, Groups: groups}, nil
}

// carriesIdentity reports whether h has any line of a configured user header.
func (p *Proxy) carriesIdentity(h http.Header) bool {
	for _, name := range p.id.UserHeaders {
		if len(h.Values(http.CanonicalHeaderKey(name))) > 0 {
			return true
		}
	}
	return false
}

// warnIfDisagree logs, at most once a minute, that two configured user headers are present with
// different values. The first one wins either way; the line names the headers, never a value.
func (p *Proxy) warnIfDisagree(h http.Header, user, via string) {
	var clash []string
	for _, name := range p.id.UserHeaders {
		name = http.CanonicalHeaderKey(name)
		if name == via || slices.Contains(clash, name) {
			continue
		}
		for _, l := range h.Values(name) {
			if v := strings.TrimSpace(l); v != "" && v != user {
				clash = append(clash, name)
				break
			}
		}
	}
	if len(clash) > 0 && p.disagreeWarn.allow(p.now()) {
		p.log.Warn("identity headers disagree; the first configured header wins", "used", via, "disagreeing", clash)
	}
}

var _ Authenticator = (*Proxy)(nil)
