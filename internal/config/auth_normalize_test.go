package config

import (
	"slices"
	"testing"
)

// The lists the authenticator reads reach it trimmed: Validate judges the trimmed entries, so a
// padded one that passed validation must not arrive padded.
func TestAuthListsAreTrimmedByTheResolver(t *testing.T) {
	res, err := Resolve(Sources{Environ: []string{
		"DASH_SERVER_PUBLIC_URL=https://dash.example.com",
		`DASH_AUTH_PROXY_USER_HEADERS=[" X-Forwarded-User "]`,
		`DASH_AUTH_PROXY_TRUSTED_PROXIES=[" 10.0.0.0/8 ", "192.0.2.1 "]`,
		"DASH_AUTH_PROXY_GROUPS_HEADER= X-Forwarded-Groups ",
	}})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Tree.Auth.Proxy
	if want := []string{"X-Forwarded-User"}; !slices.Equal(p.UserHeaders, want) {
		t.Errorf("userHeaders = %q, want %q", p.UserHeaders, want)
	}
	if want := []string{"10.0.0.0/8", "192.0.2.1"}; !slices.Equal(p.TrustedProxies, want) {
		t.Errorf("trustedProxies = %q, want %q", p.TrustedProxies, want)
	}
	if p.GroupsHeader != "X-Forwarded-Groups" {
		t.Errorf("groupsHeader = %q", p.GroupsHeader)
	}
}
