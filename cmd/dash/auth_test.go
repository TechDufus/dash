package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
)

// logCapture is a slog.Logger that keeps its records for the test to read.
type logCapture struct{ buf bytes.Buffer }

func (c *logCapture) logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&c.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// records are the log records written so far, as their JSON objects.
func (c *logCapture) records(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(c.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// with returns the records of level whose message is msg.
func (c *logCapture) with(t *testing.T, level, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range c.records(t) {
		if m["level"] == level && m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

// resolvedAuth is a resolved configuration with the default auth section, changed by mod, and the
// process flags dev and demo.
func resolvedAuth(dev, demo bool, mod func(*config.AuthConfig)) *config.Resolved {
	a := config.DefaultAuth()
	if mod != nil {
		mod(&a)
	}
	return &config.Resolved{Tree: config.Tree{Auth: a}, Config: config.Config{Dev: dev, Demo: demo}}
}

// authRequest is a request from peer carrying the user header.
func authRequest(peer string, kv ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.RemoteAddr = peer
	for i := 0; i < len(kv); i += 2 {
		r.Header.Add(kv[i], kv[i+1])
	}
	return r
}

func TestDevAndDemoIgnoreHeadersAndPeers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dev, demo bool
		kind      auth.Kind
		user      string
	}{
		{"dev", true, false, auth.KindDev, "dev"},
		{"demo", false, true, auth.KindDemo, "demo"},
		{"demo wins over dev", true, true, auth.KindDemo, "demo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs logCapture
			a, p, err := buildAuthenticator(resolvedAuth(tc.dev, tc.demo, nil), logs.logger())
			if err != nil {
				t.Fatal(err)
			}
			if p != nil {
				t.Error("a fixed mode returned a proxy to reload")
			}
			for _, r := range []*http.Request{
				authRequest("192.0.2.1:1"),                                // outside the default trusted set, no header
				authRequest("127.0.0.1:1", "X-Forwarded-User", "mallory"), // a header names someone else
				authRequest("192.0.2.1:1", "X-Forwarded-User", "mallory", "Authorization", "Bearer abc"),
			} {
				got, f := a.Authenticate(r)
				if f != nil || got.Kind != tc.kind || got.User != tc.user || got.Role != auth.RoleAdmin || got.Via != "fixed" {
					t.Errorf("%s %v: %+v %+v, want %s %s admin via fixed", r.RemoteAddr, r.Header, got, f, tc.kind, tc.user)
				}
			}
		})
	}
}

func TestAuthModeSelectsTheAuthenticator(t *testing.T) {
	var logs logCapture
	res := resolvedAuth(false, false, func(a *config.AuthConfig) {
		a.Proxy.UserHeaders = []string{"X-Auth-User"}
		a.Proxy.TrustedProxies = []string{"10.20.0.0/16", "fd00::/8"}
	})
	a, p, err := buildAuthenticator(res, logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("proxy mode returned no proxy to reload")
	}
	if _, ok := a.(*auth.Proxy); !ok || a != auth.Authenticator(p) {
		t.Fatalf("authenticator = %T, want the returned *auth.Proxy", a)
	}

	// The configured headers and peers are the ones in force.
	got, f := a.Authenticate(authRequest("10.20.1.1:1", "X-Auth-User", "alice"))
	if f != nil || got.Kind != auth.KindProxy || got.User != "alice" || got.Via != "X-Auth-User" {
		t.Errorf("configured peer and header: %+v %+v", got, f)
	}
	if _, f := a.Authenticate(authRequest("[fd00::1]:1", "X-Auth-User", "alice")); f != nil {
		t.Errorf("configured v6 prefix: %+v", f)
	}
	if _, f := a.Authenticate(authRequest("127.0.0.1:1", "X-Auth-User", "alice")); f == nil || f.Reason != auth.ReasonUntrustedPeer {
		t.Errorf("the default loopback peer is no longer trusted once the list is set: %+v", f)
	}
	if _, f := a.Authenticate(authRequest("10.20.1.1:1", "X-Forwarded-User", "alice")); f == nil || f.Reason != auth.ReasonNoIdentity {
		t.Errorf("a header outside userHeaders was read: %+v", f)
	}

	// The defaults are the sidecar's: loopback and the two oauth2-proxy headers.
	a, _, err = buildAuthenticator(resolvedAuth(false, false, nil), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	if got, f := a.Authenticate(authRequest("127.0.0.1:1", "X-Forwarded-User", "alice")); f != nil || got.User != "alice" {
		t.Errorf("default sidecar request: %+v %+v", got, f)
	}

	// An unknown mode or unusable settings are an error, never a silent fallback to another mode.
	for name, mod := range map[string]func(*config.AuthConfig){
		"unknown mode":      func(a *config.AuthConfig) { a.Mode = "sideways" },
		"bad trusted proxy": func(a *config.AuthConfig) { a.Proxy.TrustedProxies = []string{"not-a-cidr"} },
		"bad header name":   func(a *config.AuthConfig) { a.Proxy.UserHeaders = []string{"Authorization"} },
		"no user header":    func(a *config.AuthConfig) { a.Proxy.UserHeaders = nil },
	} {
		if a, p, err := buildAuthenticator(resolvedAuth(false, false, mod), logs.logger()); err == nil || a != nil || p != nil {
			t.Errorf("%s: %v %v %v, want an error and no authenticator", name, a, p, err)
		}
	}
}

func TestProxyModeLogsCountsAtStart(t *testing.T) {
	var logs logCapture
	if _, _, err := buildAuthenticator(resolvedAuth(false, false, nil), logs.logger()); err != nil {
		t.Fatal(err)
	}
	recs := logs.with(t, "INFO", "auth: proxy mode")
	if len(recs) != 1 {
		t.Fatalf("startup INFO lines = %d, want 1:\n%s", len(recs), &logs.buf)
	}
	if recs[0]["trustedPrefixes"] != float64(2) {
		t.Errorf("trustedPrefixes = %v, want 2 (the default loopback pair)", recs[0]["trustedPrefixes"])
	}
	if n := len(logs.with(t, "WARN", "")); n != 0 {
		t.Errorf("the default configuration warned %d times:\n%s", n, &logs.buf)
	}
}

func TestWideTrustedProxiesWarn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		proxies []string
		warns   int
		forge   bool // a warning says every client can forge an identity
	}{
		{"the default is quiet", []string{"127.0.0.1/32", "::1/128"}, 0, false},
		{"a /8 is the limit and quiet", []string{"10.0.0.0/8"}, 0, false},
		{"a /7 is wide", []string{"10.0.0.0/7"}, 1, false},
		{"a v6 /15 is wide", []string{"fe00::/15"}, 1, false},
		{"a v6 /16 is quiet", []string{"fe80::/16"}, 0, false},
		{"every v4 address", []string{"0.0.0.0/0"}, 1, true},
		{"every v6 address", []string{"::/0"}, 1, true},
		{"each wide prefix has its own warning", []string{"0.0.0.0/0", "::/0", "10.0.0.0/7", "127.0.0.1"}, 3, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs logCapture
			res := resolvedAuth(false, false, func(a *config.AuthConfig) {
				a.Proxy.TrustedProxies = tc.proxies
				a.Proxy.AllowAnyPeer = true
			})
			if _, _, err := buildAuthenticator(res, logs.logger()); err != nil {
				t.Fatal(err)
			}
			var warns, forge int
			for _, m := range logs.records(t) {
				if m["level"] != "WARN" {
					continue
				}
				warns++
				if msg, _ := m["msg"].(string); strings.Contains(msg, "forge") {
					forge++
				}
				if m["prefix"] == nil {
					t.Errorf("a warning without the prefix it is about: %v", m)
				}
			}
			if warns != tc.warns || (forge > 0) != tc.forge {
				t.Errorf("%d warnings (%d about forging), want %d (forging %v):\n%s", warns, forge, tc.warns, tc.forge, &logs.buf)
			}
		})
	}
}
