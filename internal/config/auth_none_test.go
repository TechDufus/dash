package config

import (
	"bytes"
	"strings"
	"testing"
)

// The mode that turns authentication off exists only when someone sets it: with no environment
// and no file the mode is proxy, and so is the default of the built-in layer.
func TestNoneModeIsNeverTheDefaultSetting(t *testing.T) {
	if got := DefaultAuth().Mode; got != AuthModeProxy {
		t.Errorf("DefaultAuth().Mode = %q, want %q", got, AuthModeProxy)
	}
	res, err := Resolve(Sources{Environ: []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Tree.Auth.Mode; got != AuthModeProxy {
		t.Errorf("auth.mode with no file and no environment = %q, want %q", got, AuthModeProxy)
	}
	if p := res.Provenance["auth.mode"]; p.Source != SourceDefault {
		t.Errorf("auth.mode provenance = %+v, want the default", p)
	}
}

// DASH_AUTH_MODE=none selects the mode and passes validation; a value that is not a mode does not.
func TestAuthModeNoneIsAcceptedOnlyWhenSet(t *testing.T) {
	env := []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_AUTH_MODE=none"}
	res, err := Resolve(Sources{Environ: env})
	if err != nil {
		t.Fatalf("auth.mode none does not load: %v", err)
	}
	if got := res.Tree.Auth.Mode; got != AuthModeNone {
		t.Errorf("auth.mode = %q, want %q", got, AuthModeNone)
	}
	if p := res.Provenance["auth.mode"]; p.Source != SourceEnv || p.Ref != "DASH_AUTH_MODE" {
		t.Errorf("auth.mode provenance = %+v", p)
	}

	env[1] = "DASH_AUTH_MODE=off"
	if _, err := Resolve(Sources{Environ: env}); err == nil || !strings.Contains(err.Error(), "auth.mode") || !strings.Contains(err.Error(), AuthModeNone) {
		t.Errorf("auth.mode off: error = %v, want it to name auth.mode and the modes", err)
	}
}

// The generated settings page tells the operator to bind the loopback when authentication is off.
func TestNoneDocRecommendsLoopback(t *testing.T) {
	var md bytes.Buffer
	if err := WriteEnvMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(md.String(), "\n") {
		if strings.Contains(line, "DASH_AUTH_MODE") {
			if !strings.Contains(line, "127.0.0.1") || !strings.Contains(line, "none") {
				t.Errorf("the DASH_AUTH_MODE row does not recommend the loopback for none: %s", line)
			}
			return
		}
	}
	t.Fatal("docs/configuration.md has no DASH_AUTH_MODE row")
}
