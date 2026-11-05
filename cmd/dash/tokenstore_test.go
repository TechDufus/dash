package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
)

// The credential store, which keeps the personal access tokens, opens in every mode but none
// (contract D1): the data dir's file in proxy mode as in the session modes, in memory in dev and
// demo and without a writable data dir, and not at all in auth.mode=none.
func TestCredentialStoreOpensInEveryModeButNone(t *testing.T) {
	cases := []struct {
		name       string
		res        func(dir string) *config.Resolved
		open, file bool
	}{
		{"proxy", func(string) *config.Resolved { return resolvedAuth(false, false, nil) }, true, true},
		{"proxy without a data dir", func(string) *config.Resolved { return resolvedAuth(false, false, nil) }, true, false},
		{"local", func(dir string) *config.Resolved { return localResolved(dir, nil, nil) }, true, true},
		{"dev", func(string) *config.Resolved { return resolvedAuth(true, false, nil) }, true, false},
		{"demo", func(string) *config.Resolved { return resolvedAuth(false, true, nil) }, true, false},
		{"none", func(string) *config.Resolved {
			return resolvedAuth(false, false, func(a *config.AuthConfig) { a.Mode = config.AuthModeNone })
		}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			res := tc.res(dir)
			if tc.name == "proxy without a data dir" {
				res.Config.DataDir = filepath.Join(dir, "absent")
			} else {
				res.Config.DataDir = dir
			}
			pa, _, err := startLocal(t, res, hashOf(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			if (pa.store != nil) != tc.open {
				t.Fatalf("store open = %v, want %v", pa.store != nil, tc.open)
			}
			if pa.store != nil && pa.store.Persistent() != tc.file {
				t.Errorf("Persistent = %v, want %v", pa.store.Persistent(), tc.file)
			}
			_, err = os.Stat(filepath.Join(dir, credentials.FileName))
			if exists := err == nil; exists != tc.file {
				t.Errorf("%s exists = %v, want %v", credentials.FileName, exists, tc.file)
			}
		})
	}
}

// auth.mode=none resolves no bearer token, so auth.tokens and server.corsOrigins do nothing there:
// each is warned about at start.
func TestNoneModeWarnsAboutTokenSettings(t *testing.T) {
	res := resolvedAuth(false, false, func(a *config.AuthConfig) {
		a.Mode = config.AuthModeNone
		a.Tokens = []config.StaticToken{{Name: "ci"}}
	})
	res.Config.CORSOrigins = []string{"https://inspector.example.com"}
	var logs logCapture
	if _, _, err := startLocal(t, res, "", &logs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth.tokens is ignored: auth.mode=none resolves no bearer token", "server.corsOrigins is ignored: auth.mode=none has no bearer clients, and every other request stays same-origin"} {
		if len(logs.with(t, "WARN", want)) != 1 {
			t.Errorf("no WARN %q", want)
		}
	}
}
