package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
)

// The two ways out the refusal names: bind the loopback, or opt in knowing the cost.
var noneRefusalNames = []string{"auth.mode=none", "DASH_SERVER_LISTEN=127.0.0.1:8080", "DASH_AUTH_NONE_ALLOW_REMOTE=true", "anyone who can reach dash is an administrator", "--network host"}

func wantNoneRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want the refusal of auth.mode=none off loopback", what)
	}
	for _, s := range noneRefusalNames {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("%s: error lacks %q: %v", what, s, err)
		}
	}
}

// auth.mode=none on the default listener (0.0.0.0:8080) would make every client that reaches dash
// an administrator, so the start refuses it; before v0.14.0's fix a remote peer with no
// credentials was signed in as the anonymous administrator.
func TestNoneModeOnTheDefaultListenRefusesToStart(t *testing.T) {
	_, err := config.Resolve(config.Sources{
		Environ:  []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_AUTH_MODE=none", "DASH_DATA_DIR=" + t.TempDir()},
		Registry: secret.NewRegistry(),
	})
	if err == nil {
		// What the refusal prevents, said plainly when it is missing.
		res := resolveOnDefaultListen(t, "DASH_AUTH_MODE=none", "DASH_DATA_DIR="+t.TempDir())
		a, _, berr := buildAuthenticator(res, (&logCapture{}).logger())
		if berr == nil {
			if p, f := a.Authenticate(authRequest("192.0.2.1:1234")); f == nil && p.Role == auth.RoleAdmin {
				t.Errorf("a remote peer with no credentials is %s %q", p.Role, p.User)
			}
		}
	}
	wantNoneRefusal(t, "resolve", err)

	// The start's own path refuses too.
	startEnv(t, t.TempDir(), "DASH_AUTH_MODE=none")
	_, _, err = loadConfig(secret.NewRegistry(), nil)
	wantNoneRefusal(t, "loadConfig", err)
}

// dash config validate reports the same refusal and exits non-zero.
func TestValidateReportsNoneModeOffLoopback(t *testing.T) {
	cleanEnv(t)
	t.Setenv("DASH_AUTH_MODE", "none")
	out, err := validate(t)
	if !errors.Is(err, errConfigInvalid) || exitCode(err) != 1 {
		t.Fatalf("err = %v (exit %d), want errConfigInvalid and exit 1\n%s", err, exitCode(err), out)
	}
	wantNoneRefusal(t, "config validate", errors.New(out))

	t.Setenv("DASH_SERVER_LISTEN", "127.0.0.1:8080")
	if out, err := validate(t); err != nil {
		t.Errorf("none on loopback: %v\n%s", err, out)
	}
}

// none starts on a loopback listener, and with auth.none.allowRemote on any listener, then with
// the stronger WARN that names the address.
func TestNoneModeStartsOnLoopbackOrWithTheOptIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      []string
		listen   string
		stronger bool
	}{
		{"loopback", []string{"DASH_SERVER_LISTEN=127.0.0.1:8080"}, "127.0.0.1:8080", false},
		{"ipv6 loopback", []string{"DASH_SERVER_LISTEN=[::1]:8080"}, "[::1]:8080", false},
		{"allowRemote on the default", []string{"DASH_AUTH_NONE_ALLOW_REMOTE=true"}, "0.0.0.0:8080", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := config.Resolve(config.Sources{
				Environ:  append([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_AUTH_MODE=none"}, tc.env...),
				Registry: secret.NewRegistry(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.Config.Listen != tc.listen {
				t.Fatalf("Listen = %q, want %q", res.Config.Listen, tc.listen)
			}
			var logs logCapture
			if _, _, err := buildAuthenticator(res, logs.logger()); err != nil {
				t.Fatal(err)
			}
			var stronger []map[string]any
			for _, m := range logs.records(t) {
				if m["level"] == "WARN" && m["msg"] != noneWarning {
					stronger = append(stronger, m)
				}
			}
			if got := len(stronger) == 1 && stronger[0]["listen"] == tc.listen; got != tc.stronger {
				t.Errorf("stronger WARN = %v, want one naming %s: %v", got, tc.listen, stronger)
			}
		})
	}
}

// Dev and demo sign in their fixed user whatever auth.mode says; the none rule does not touch
// them (dev keeps its own loopback rule, demo may bind every interface).
func TestNoneModeRuleLeavesDevAndDemoAlone(t *testing.T) {
	for _, env := range [][]string{
		{"DASH_DEV=true", "DASH_AUTH_MODE=none"},
		{"DASH_DEMO=true", "DASH_AUTH_MODE=none"},
		{"DASH_DEMO=true", "DASH_AUTH_MODE=none", "DASH_SERVER_LISTEN=0.0.0.0:8080"},
	} {
		if _, err := config.Resolve(config.Sources{Environ: env, Registry: secret.NewRegistry()}); err != nil {
			t.Errorf("%v: %v", env, err)
		}
	}
	_, err := config.Resolve(config.Sources{Environ: []string{"DASH_DEV=true", "DASH_AUTH_MODE=none", "DASH_SERVER_LISTEN=0.0.0.0:8080"}})
	if err == nil || !strings.Contains(err.Error(), "DASH_DEV=true") || slices.ContainsFunc(noneRefusalNames[2:], func(s string) bool { return strings.Contains(err.Error(), s) }) {
		t.Errorf("dev off loopback: %v, want only the dev rule", err)
	}
}
