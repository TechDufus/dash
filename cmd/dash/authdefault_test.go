package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
)

// The breaking default, end to end on the homelab-shaped environment: without DASH_AUTH_MODE its
// proxy admin list refuses the load (naming the line to add); without that list too it loads in
// the local mode and the start refuses with the documented message, because there is neither a
// writable data dir nor an env hash.
func TestHomelabEnvWithoutAuthModeStartsInLocalAndRefuses(t *testing.T) {
	full := deploymentEnv(t, deploymentEnvV011)
	without := func(drop ...string) []string {
		var out []string
		for _, kv := range full {
			if !hasAnyPrefix(drop, kv) {
				out = append(out, kv)
			}
		}
		return append(out, "DASH_DATA_DIR="+filepath.Join(t.TempDir(), "absent"))
	}
	if _, err := config.Resolve(config.Sources{Environ: without("DASH_AUTH_MODE=")}); err == nil || !strings.Contains(err.Error(), "DASH_AUTH_MODE=proxy") {
		t.Errorf("homelab env without its mode: %v, want the refusal naming DASH_AUTH_MODE=proxy", err)
	}
	res, err := config.Resolve(config.Sources{Environ: without("DASH_AUTH_MODE=", "DASH_AUTH_PROXY_ADMIN_USERS="), Registry: secret.NewRegistry()})
	if err != nil {
		t.Fatalf("without the mode and the admin list the env must load: %v", err)
	}
	if res.Tree.Auth.Mode != config.AuthModeLocal {
		t.Fatalf("mode = %q, want local", res.Tree.Auth.Mode)
	}
	if _, _, err := startLocal(t, res, "", nil); !errors.Is(err, errLocalNeedsState) {
		t.Errorf("start: %v, want %v", err, errLocalNeedsState)
	}
	// With the line back it is the proxy mode, as deployed.
	if res, err := config.Resolve(config.Sources{Environ: full}); err != nil || res.Tree.Auth.Mode != config.AuthModeProxy {
		t.Errorf("the deployed env: %v", err)
	}
}

// hasAnyPrefix reports whether kv starts with any of the prefixes.
func hasAnyPrefix(prefixes []string, kv string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}
