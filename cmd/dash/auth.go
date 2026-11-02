package main

import (
	"fmt"
	"log/slog"
	"slices"

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
	// The mapping keys arrive with the role leaf; until then every authenticated user is an admin,
	// as before.
	roles := auth.RoleMap{AdminUsers: []string{"*"}}
	p := auth.NewProxy(trust, id, roles, auth.WithLogger(log))
	log.Info("auth: proxy mode", "trustedPrefixes", trust.Len(), "adminUsers", len(roles.AdminUsers), "adminGroups", len(roles.AdminGroups))
	for _, b := range trust.Broad() {
		if b.Bits() == 0 {
			log.Warn("auth.proxy.trustedProxies trusts every address: every client can forge an identity", "prefix", b.String())
			continue
		}
		log.Warn("auth.proxy.trustedProxies holds a very wide prefix: dash believes the identity headers of every peer in it", "prefix", b.String())
	}
	return p, p, nil
}
