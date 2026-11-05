package config

import (
	"reflect"
	"testing"
)

// The v0.11.0 environment file, relative to this package: cmd/dash's
// TestDeploymentEnvV011ReachesTheAuthenticator reads it too.
const deploymentEnvFileV011 = "testdata/deployment-v0.11.0.env"

// TestDeploymentEnvV011 is the v0.10.0 environment plus the three variables v0.11.0 adds (contract
// 5.1): the proxy mode, the loopback as the believed peers and the one administrator. It loads
// through the real environment layer and must be valid; everything v0.10.0 resolved to still
// holds, so the new lines change nothing but the auth section.
func TestDeploymentEnvV011(t *testing.T) {
	old := deploymentEnv(t, deploymentEnvFileV010)
	env := deploymentEnv(t, deploymentEnvFileV011)

	// The file is exactly the v0.10.0 lines plus the three new ones, in this order.
	added := []string{
		"DASH_AUTH_MODE=proxy",
		`DASH_AUTH_PROXY_TRUSTED_PROXIES=["127.0.0.1/32","::1/128"]`,
		`DASH_AUTH_PROXY_ADMIN_USERS=["Bob"]`,
	}
	if want := append(append([]string(nil), old...), added...); !reflect.DeepEqual(env, want) {
		t.Fatalf("%s is not the v0.10.0 file plus the three auth lines\n got: %q\nwant: %q", deploymentEnvFileV011, env, want)
	}

	res, err := Resolve(Sources{Environ: env})
	if err != nil {
		t.Fatalf("the v0.11.0 environment does not load: %v", err)
	}
	prev, err := Resolve(Sources{Environ: old})
	if err != nil {
		t.Fatalf("the v0.10.0 environment does not load: %v", err)
	}
	assertHomelabEnvV09(t, res)

	a := res.Tree.Auth
	if a.Mode != "proxy" {
		t.Errorf("auth.mode = %q, want proxy", a.Mode)
	}
	p := a.Proxy
	if want := []string{"127.0.0.1/32", "::1/128"}; !reflect.DeepEqual(p.TrustedProxies, want) {
		t.Errorf("auth.proxy.trustedProxies = %q, want %q", p.TrustedProxies, want)
	}
	if want := []string{"Bob"}; !reflect.DeepEqual(p.AdminUsers, want) {
		t.Errorf("auth.proxy.adminUsers = %q, want %q", p.AdminUsers, want)
	}
	// What the deployment leaves alone stays at the default: the two user headers, groups off.
	def := DefaultAuth().Proxy
	if !reflect.DeepEqual(p.UserHeaders, def.UserHeaders) || p.GroupsHeader != "" || p.AllowAnyPeer || len(p.AdminGroups) != 0 {
		t.Errorf("auth.proxy = %+v, want the defaults besides the trusted proxies and adminUsers", p)
	}

	// Every v0.10.0 line resolves as before: with the auth section set aside the two trees are equal.
	got, want := res.Tree, prev.Tree
	got.Auth, want.Auth = AuthConfig{}, AuthConfig{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the three auth lines changed something besides the auth section:\n got: %+v\nwant: %+v", got, want)
	}
	if !reflect.DeepEqual(res.Config, prev.Config) {
		t.Errorf("the flat config differs from v0.10.0:\n got: %+v\nwant: %+v", res.Config, prev.Config)
	}
}
