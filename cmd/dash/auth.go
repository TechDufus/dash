package main

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
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
		return buildProxy(a.Proxy, log)
	}
	return nil, nil, fmt.Errorf("auth.mode %q is not supported", a.Mode)
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
// to learn what is trusted: counts only, never names.
func buildProxy(c config.AuthProxyConfig, log *slog.Logger) (auth.Authenticator, *auth.Proxy, error) {
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
	return p, p, nil
}

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
		if s = strings.TrimSpace(s); s != "" {
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
