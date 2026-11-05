package main

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/tokens"
)

// buildAuthenticator picks the authenticator of the process. Dev and demo mode outrank
// auth.mode: they sign every request in as their fixed identity and ignore identity headers and
// the peer. Otherwise auth.mode selects it; the proxy mode returns the *auth.Proxy as well, whose
// role map the reloader swaps (applyAuthReload). The second value is nil for the fixed modes.
func buildAuthenticator(res *config.Resolved, log *slog.Logger) (auth.Authenticator, *auth.Proxy, error) {
	a := res.Tree.Auth
	if fixed, ok := fixedPrincipal(res.Config); ok {
		if a.Mode != config.AuthModeProxy {
			log.Info("auth.mode is ignored: dev and demo mode sign every request in as a fixed identity", "mode", a.Mode)
		}
		return auth.Fixed(fixed), nil, nil
	}
	switch a.Mode {
	case config.AuthModeProxy:
		return buildProxy(a.Proxy, res.Config.Listen, res.Config.DirectURL != "", log)
	case config.AuthModeNone:
		return buildNone(res.Config.Listen, log), nil, nil
	}
	return nil, nil, fmt.Errorf("auth.mode %q is not supported", a.Mode)
}

// buildNone is the authenticator of auth.mode=none: every request is signed in as the same
// administrator, whatever it carries. The WARN is logged at every start, and a stronger one when
// the listener is not bound to the loopback, where anyone who can reach dash is that administrator.
func buildNone(listen string, log *slog.Logger) auth.Authenticator {
	log.Warn("auth.mode=none: every request is signed in as an administrator")
	if !config.LoopbackAddr(listen) {
		log.Warn("auth.mode=none with server.listen not bound to the loopback: anyone who can reach dash is an administrator; bind 127.0.0.1 or use another auth.mode", "listen", listen)
	}
	return auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
}

// fixedPrincipal is the identity of the process modes that need no credential.
func fixedPrincipal(c config.Config) (auth.Principal, bool) {
	switch {
	case c.Demo:
		return auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}, true
	case c.Dev:
		return auth.Principal{Kind: auth.KindDev, User: "dev", Role: auth.RoleAdmin}, true
	}
	return auth.Principal{}, false
}

// buildProxy is the authenticator of auth.mode=proxy, with the startup lines an operator reads
// to learn what is trusted: counts only, never names. direct says server.directURL is set.
func buildProxy(c config.AuthProxyConfig, listen string, direct bool, log *slog.Logger) (auth.Authenticator, *auth.Proxy, error) {
	trust, err := auth.ParseTrust(c.TrustedProxies)
	if err != nil {
		return nil, nil, fmt.Errorf("auth.proxy.trustedProxies: %w", err)
	}
	id := auth.ProxyIdentity{UserHeaders: slices.Clone(c.UserHeaders), GroupsHeader: c.GroupsHeader}
	if err := id.Validate(); err != nil {
		return nil, nil, fmt.Errorf("auth.proxy: %w", err)
	}
	roles := roleMapOf(c)
	p := auth.NewProxy(trust, id, roles, auth.WithLogger(log))
	log.Info("auth: proxy mode", "trustedPrefixes", trust.Len(), "adminUsers", len(roles.AdminUsers), "adminGroups", len(roles.AdminGroups))
	if roles.Wildcard() {
		log.Warn(adminWildcardWarning)
	}
	for _, b := range trust.Broad() {
		if b.Bits() == 0 {
			log.Warn("auth.proxy.trustedProxies trusts every address: every client can forge an identity", "prefix", b.String())
			continue
		}
		log.Warn("auth.proxy.trustedProxies holds a very wide prefix: dash believes the identity headers of every peer in it", "prefix", b.String())
	}
	// A sidecar install that missed DASH_SERVER_LISTEN=127.0.0.1:8080 (the default before v0.14.0)
	// exposes nothing, since a remote peer's identity is refused, but it should not pass silently.
	// With server.directURL the open listener is the point (contract D20): the direct route
	// reaches it past the sidecar.
	if !direct && !config.LoopbackAddr(listen) && onlyLoopback(c.TrustedProxies) {
		log.Warn(proxyListenWarning, "listen", listen)
	}
	return p, p, nil
}

// onlyLoopback reports whether every trustedProxies entry (a CIDR or an address, as ParseTrust
// takes them) lies inside the loopback range. A list ParseTrust accepted is never empty.
func onlyLoopback(entries []string) bool {
	for _, e := range entries {
		e = strings.TrimSpace(e)
		p, err := netip.ParsePrefix(e)
		if err != nil {
			a, aerr := netip.ParseAddr(e)
			if aerr != nil {
				return false
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		a := p.Addr().Unmap()
		if !a.IsLoopback() || (a.Is4() && p.Bits() < 8) || (a.Is6() && p.Bits() < 128) {
			return false
		}
	}
	return len(entries) > 0
}

// proxyListenWarning is the startup WARN for proxy mode on a listener other machines can reach
// while only loopback peers are believed.
const proxyListenWarning = "the API listens on a non-loopback address but only loopback proxies are trusted; behind a sidecar set DASH_SERVER_LISTEN=127.0.0.1:8080"

// adminWildcardWarning is the startup and reload WARN for adminUsers: ["*"].
const adminWildcardWarning = `auth.proxy.adminUsers contains "*": every authenticated user is an administrator`

// roleMapOf is the role mapping of the proxy settings: users and groups are matched exactly, so
// the lists are only cleaned of surrounding space and empty entries.
func roleMapOf(c config.AuthProxyConfig) auth.RoleMap {
	return auth.RoleMap{AdminUsers: cleanNames(c.AdminUsers), AdminGroups: cleanNames(c.AdminGroups)}
}

func cleanNames(in []string) []string {
	var out []string
	for _, s := range in {
		if s = auth.TrimIdentity(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// applyAuthReload hands the live auth settings to the running authenticator: the next request is
// mapped with the new admin lists. p is nil for the fixed modes, which have nothing to reload.
// Every other auth key needs a restart and is not read here.
func applyAuthReload(p *auth.Proxy, a config.AuthConfig) {
	if p == nil {
		return
	}
	roles := roleMapOf(a.Proxy)
	p.Roles().Store(roles)
	slog.Info("auth: admin lists reloaded", "adminUsers", len(roles.AdminUsers), "adminGroups", len(roles.AdminGroups))
	if roles.Wildcard() {
		slog.Warn(adminWildcardWarning)
	}
}

// reloadAuth makes the two admin lists live: when the auth section changes the next request is
// mapped with the new lists, with no restart. Every other auth key is tagged restart in the
// schema, so the reloader still reports a change to it as needing one. p is nil for the fixed
// modes.
func (f *framework) reloadAuth(p *auth.Proxy) {
	f.reloader.Subscribe(func(c config.Change) {
		if c.Section == "auth" {
			applyAuthReload(p, f.reloader.Tree().Auth)
		}
	})
}

// reloadTokens makes auth.tokens live (contract D5): when the auth section changes, the next
// request is resolved against the new static tokens, so removing an entry revokes it at once.
// The environment is read at start only, so a token from DASH_AUTH_TOKENS needs a restart. t is
// nil in auth.mode=none.
func (f *framework) reloadTokens(t *tokens.Resolver) {
	if t == nil {
		return
	}
	f.reloader.Subscribe(func(c config.Change) {
		if c.Section == "auth" {
			static := f.reloader.Tree().Auth.Tokens
			t.SetStatic(static)
			slog.Info("auth: static tokens reloaded", "count", len(static))
		}
	})
}
