// Package authtest builds authenticators for tests in other packages, so the API and command
// tests that need a principal do not each rebuild the proxy trust and header set.
package authtest

import "github.com/TechDufus/dash/internal/auth"

// Trust is the peer set the test requests come from: the loopback addresses, which is also the
// default of auth.proxy.trustedProxies.
func Trust() auth.Trust {
	t, err := auth.ParseTrust([]string{"127.0.0.1/32", "::1/128"})
	if err != nil {
		panic(err)
	}
	return t
}

// Identity is the default header set: X-Forwarded-Preferred-Username, then X-Forwarded-User, and
// no groups header.
func Identity() auth.ProxyIdentity {
	return auth.ProxyIdentity{UserHeaders: []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User"}}
}

// Proxy is the proxy authenticator with the loopback peers trusted, the default headers, and
// admins as the admin users. With no admins every user is a viewer.
func Proxy(admins ...string) *auth.Proxy {
	return ProxyWith(auth.RoleMap{AdminUsers: admins})
}

// ProxyWith is Proxy with a full role map and the options of auth.NewProxy.
func ProxyWith(roles auth.RoleMap, opts ...auth.ProxyOption) *auth.Proxy {
	return auth.NewProxy(Trust(), Identity(), roles, opts...)
}
