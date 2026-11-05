package main

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
)

// The v0.11.0 deployment environment, the file internal/config's TestDeploymentEnvV011 pins.
var deploymentEnvV011 = filepath.Join("..", "..", "internal", "config", "testdata", "deployment-v0.11.0.env")

// The v0.11.0 deployment environment reaches the authenticator the process builds from it: the
// identity proxy on the loopback is believed, the one named administrator is an administrator
// and every other user a viewer, and a peer outside the trusted prefixes is refused.
func TestDeploymentEnvV011ReachesTheAuthenticator(t *testing.T) {
	res, err := config.Resolve(config.Sources{Environ: deploymentEnv(t, deploymentEnvV011)})
	if err != nil {
		t.Fatalf("the v0.11.0 environment does not load: %v", err)
	}
	a, proxy, err := buildAuthenticator(res, slog.New(slog.DiscardHandler))
	if err != nil || proxy == nil {
		t.Fatalf("buildAuthenticator = %v, proxy %v", err, proxy)
	}
	for _, tc := range []struct {
		name, peer, user string
		role             auth.Role
	}{
		{"the sidecar on 127.0.0.1 signs the owner in", "127.0.0.1:41000", "Bob", auth.RoleAdmin},
		{"the sidecar on ::1", "[::1]:41000", "Bob", auth.RoleAdmin},
		{"another user is a viewer", "127.0.0.1:41000", "dash-verify", auth.RoleViewer},
		{"the mapping is case-sensitive", "127.0.0.1:41000", "bob", auth.RoleViewer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f := a.Authenticate(authRequest(tc.peer, "X-Forwarded-User", tc.user))
			if f != nil || p.User != tc.user || p.Role != tc.role || p.Kind != auth.KindProxy || p.Via != "X-Forwarded-User" {
				t.Errorf("principal = %+v, failure %+v, want %s as %s via X-Forwarded-User", p, f, tc.user, tc.role)
			}
		})
	}
	for _, peer := range []string{"192.0.2.1:1234", "127.0.0.2:1234"} {
		if _, f := a.Authenticate(authRequest(peer, "X-Forwarded-User", "Bob")); f == nil || f.Status != 401 || f.Reason != auth.ReasonUntrustedPeer {
			t.Errorf("peer %s outside the trusted prefixes: failure %+v, want 401 %s", peer, f, auth.ReasonUntrustedPeer)
		}
	}
}
