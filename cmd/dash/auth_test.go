package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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

// with returns the records of level whose message is msg; an empty msg matches every message.
func (c *logCapture) with(t *testing.T, level, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range c.records(t) {
		if m["level"] == level && (msg == "" || m["msg"] == msg) {
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

// roleOf authenticates user (with groups) through a and returns the role it was given.
func roleOf(t *testing.T, a auth.Authenticator, user, groups string) auth.Role {
	t.Helper()
	kv := []string{"X-Forwarded-User", user}
	if groups != "" {
		kv = append(kv, "X-Forwarded-Groups", groups)
	}
	got, f := a.Authenticate(authRequest("127.0.0.1:1", kv...))
	if f != nil {
		t.Fatalf("%s refused: %+v", user, f)
	}
	return got.Role
}

func TestRoleMappingFromConfig(t *testing.T) {
	var logs logCapture
	a, _, err := buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) {
		c.Proxy.AdminUsers = []string{"TechDufus", "  padded  ", "", "\u00a0nbsp"}
		c.Proxy.AdminGroups = []string{"ops"}
		c.Proxy.GroupsHeader = "X-Forwarded-Groups"
	}), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		user, groups string
		want         auth.Role
	}{
		{"TechDufus", "", auth.RoleAdmin},
		{"techdufus", "", auth.RoleViewer}, // exact and case-sensitive
		{"padded", "", auth.RoleAdmin},     // list entries are trimmed of space and tab
		{"\u00a0nbsp", "", auth.RoleAdmin}, // a no-break space is part of the name
		{"nbsp", "", auth.RoleViewer},      // so the plain name is another user
		{"dash-verify", "", auth.RoleViewer},
		{"dash-verify", "dev,ops", auth.RoleAdmin},
		{"dash-verify", "dev,Ops", auth.RoleViewer},
	} {
		if got := roleOf(t, a, tc.user, tc.groups); got != tc.want {
			t.Errorf("%s %q: role = %q, want %q", tc.user, tc.groups, got, tc.want)
		}
	}
}

func TestEmptyAdminListsMakeEveryUserAViewer(t *testing.T) {
	var logs logCapture
	a, _, err := buildAuthenticator(resolvedAuth(false, false, nil), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "TechDufus", "*", "admin"} {
		if got := roleOf(t, a, user, ""); got != auth.RoleViewer {
			t.Errorf("%s: role = %q, want viewer with the default (empty) lists", user, got)
		}
	}
	if w, ok := a.(auth.Warner); !ok || len(w.Warnings()) != 0 {
		t.Errorf("the default configuration has standing warnings: %v", a)
	}
}

func TestWildcardMakesEveryoneAdmin(t *testing.T) {
	var logs logCapture
	a, _, err := buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) {
		c.Proxy.AdminUsers = []string{"*"}
	}), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "TechDufus", "dash-verify"} {
		if got := roleOf(t, a, user, ""); got != auth.RoleAdmin {
			t.Errorf("%s: role = %q, want admin under the wildcard", user, got)
		}
	}
	// A WARN at start, and the standing warning the API shows in /me.
	if got := logs.with(t, "WARN", adminWildcardWarning); len(got) != 1 {
		t.Errorf("wildcard WARN lines = %d, want 1:\n%s", len(got), &logs.buf)
	}
	w, ok := a.(auth.Warner)
	if !ok || !slices.Equal(w.Warnings(), []string{"admin_wildcard"}) {
		t.Errorf("warnings = %v (Warner %v), want [admin_wildcard]", w, ok)
	}

	// An exact list neither warns nor reports.
	logs = logCapture{}
	a, _, err = buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) {
		c.Proxy.AdminUsers = []string{"alice"}
	}), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(logs.with(t, "WARN", adminWildcardWarning)); n != 0 {
		t.Errorf("an exact admin list logged the wildcard WARN")
	}
	if w := a.(auth.Warner).Warnings(); len(w) != 0 {
		t.Errorf("warnings = %v, want none", w)
	}
}

func TestProxyModeLogsCountsNotNames(t *testing.T) {
	var logs logCapture
	_, _, err := buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) {
		c.Proxy.AdminUsers = []string{"TechDufus", "second-admin-name", " ", ""} // blank entries are not counted
		c.Proxy.AdminGroups = []string{"secret-group-name", "\t"}
		c.Proxy.GroupsHeader = "X-Forwarded-Groups"
	}), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	recs := logs.with(t, "INFO", "auth: proxy mode")
	if len(recs) != 1 || recs[0]["trustedPrefixes"] != float64(2) || recs[0]["adminUsers"] != float64(2) || recs[0]["adminGroups"] != float64(1) {
		t.Fatalf("startup INFO = %v, want counts 2 prefixes, 2 users, 1 group:\n%s", recs, &logs.buf)
	}
	for _, name := range []string{"TechDufus", "second-admin-name", "secret-group-name"} {
		if strings.Contains(logs.buf.String(), name) {
			t.Errorf("the startup log names %q:\n%s", name, &logs.buf)
		}
	}
}

func TestApplyAuthReloadAppliesOnTheNextRequest(t *testing.T) {
	var logs logCapture
	a, p, err := buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) {
		c.Proxy.AdminUsers = []string{"alice"}
		c.Proxy.GroupsHeader = "X-Forwarded-Groups"
	}), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	if roleOf(t, a, "alice", "") != auth.RoleAdmin || roleOf(t, a, "bob", "") != auth.RoleViewer {
		t.Fatal("initial lists not applied")
	}

	reload := config.DefaultAuth()
	reload.Proxy.AdminUsers = []string{"bob"}
	reload.Proxy.AdminGroups = []string{"ops"}
	reload.Proxy.GroupsHeader = "X-Forwarded-Groups"
	applyAuthReload(p, reload)
	for _, tc := range []struct {
		user, groups string
		want         auth.Role
	}{
		{"alice", "", auth.RoleViewer},
		{"bob", "", auth.RoleAdmin},
		{"carol", "ops", auth.RoleAdmin},
	} {
		if got := roleOf(t, a, tc.user, tc.groups); got != tc.want {
			t.Errorf("after the reload %s %q: role = %q, want %q", tc.user, tc.groups, got, tc.want)
		}
	}

	// Both lists emptied: everyone is a viewer.
	applyAuthReload(p, config.DefaultAuth())
	if got := roleOf(t, a, "bob", ""); got != auth.RoleViewer {
		t.Errorf("after emptying the lists bob = %q, want viewer", got)
	}

	// A reload to the wildcard warns, as the start does, and shows in the standing warnings.
	prev := slog.Default()
	slog.SetDefault(logs.logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	wild := config.DefaultAuth()
	wild.Proxy.AdminUsers = []string{"*"}
	applyAuthReload(p, wild)
	if n := len(logs.with(t, "WARN", adminWildcardWarning)); n != 1 {
		t.Errorf("wildcard WARN lines after the reload = %d, want 1", n)
	}
	if !slices.Equal(a.(auth.Warner).Warnings(), []string{"admin_wildcard"}) {
		t.Errorf("warnings after the wildcard reload = %v", a.(auth.Warner).Warnings())
	}
}

// The fixed modes have no proxy to reload: a nil proxy is accepted and nothing happens.
func TestApplyAuthReloadIgnoresTheFixedModes(t *testing.T) {
	var logs logCapture
	a, p, err := buildAuthenticator(resolvedAuth(true, false, nil), logs.logger())
	if err != nil || p != nil {
		t.Fatalf("dev: %v %v", p, err)
	}
	reload := config.DefaultAuth()
	reload.Proxy.AdminUsers = []string{"*"}
	applyAuthReload(nil, reload)
	if got, f := a.Authenticate(authRequest("127.0.0.1:1")); f != nil || got.Role != auth.RoleAdmin || got.User != "dev" {
		t.Errorf("dev principal changed: %+v %+v", got, f)
	}
}

const noneWarning = "auth.mode=none: every request is signed in as an administrator"

// noneResolved is a hand-built configuration for auth.mode=none: the config enum only allows the
// value once the mode is released, and buildAuthenticator reads the string.
func noneResolved(listen string) *config.Resolved {
	res := resolvedAuth(false, false, func(a *config.AuthConfig) { a.Mode = "none" })
	res.Config.Listen = listen
	return res
}

func TestNoneModeWarnsAtStart(t *testing.T) {
	var logs logCapture
	a, p, err := buildAuthenticator(noneResolved("127.0.0.1:8080"), logs.logger())
	if err != nil {
		t.Fatal(err)
	}
	if p != nil {
		t.Error("none mode returned a proxy to reload")
	}
	got, f := a.Authenticate(authRequest("192.0.2.1:1", "X-Forwarded-User", "mallory"))
	if f != nil || got.Kind != auth.KindNone || got.User != "anonymous" || got.Role != auth.RoleAdmin {
		t.Errorf("principal = %+v %+v, want the anonymous admin", got, f)
	}
	if w, ok := a.(auth.Warner); !ok || !slices.Equal(w.Warnings(), []string{"auth_none"}) {
		t.Errorf("warnings = %v, want [auth_none]", w)
	}
	// On a loopback listener it is the one WARN, and nothing else is logged above INFO.
	if n := len(logs.with(t, "WARN", noneWarning)); n != 1 {
		t.Errorf("the none WARN was logged %d times, want once:\n%s", n, &logs.buf)
	}
	var warns int
	for _, m := range logs.records(t) {
		if m["level"] == "WARN" {
			warns++
		}
	}
	if warns != 1 {
		t.Errorf("%d WARN lines on a loopback listener, want 1:\n%s", warns, &logs.buf)
	}

	// Every start warns, not only the first.
	buildAuthenticator(noneResolved("127.0.0.1:8080"), logs.logger())
	if n := len(logs.with(t, "WARN", noneWarning)); n != 2 {
		t.Errorf("after a second start the WARN count = %d, want 2", n)
	}
}

func TestNoneModeWarnsHarderOnNonLoopback(t *testing.T) {
	for _, tc := range []struct {
		listen   string
		stronger bool
	}{
		{"127.0.0.1:8080", false},
		{"127.0.0.9:8080", false},
		{"[::1]:8080", false},
		{"localhost:8080", false},
		{"0.0.0.0:8080", true},
		{"[::]:8080", true},
		{":8080", true}, // an empty host binds every interface
		{"192.0.2.10:8080", true},
		{"dash.internal:8080", true},
		{"not-an-address", true}, // anything that does not parse is not known to be loopback
	} {
		var logs logCapture
		if _, _, err := buildAuthenticator(noneResolved(tc.listen), logs.logger()); err != nil {
			t.Fatal(err)
		}
		if n := len(logs.with(t, "WARN", noneWarning)); n != 1 {
			t.Errorf("%s: the none WARN was logged %d times, want once", tc.listen, n)
		}
		var stronger []map[string]any
		for _, m := range logs.records(t) {
			if m["level"] == "WARN" && m["msg"] != noneWarning {
				stronger = append(stronger, m)
			}
		}
		switch {
		case tc.stronger && (len(stronger) != 1 || stronger[0]["listen"] != tc.listen):
			t.Errorf("%s: stronger WARNs = %v, want one naming the listen address", tc.listen, stronger)
		case !tc.stronger && len(stronger) != 0:
			t.Errorf("%s: unexpected extra WARN: %v", tc.listen, stronger)
		}
	}
}

// none is impossible without the setting: the default section builds the proxy authenticator, and
// a mode that is empty or misspelled is an error, never none.
func TestNoneModeIsNeverTheDefault(t *testing.T) {
	var logs logCapture
	a, p, err := buildAuthenticator(resolvedAuth(false, false, nil), logs.logger())
	if err != nil || p == nil {
		t.Fatalf("default: %v %v", p, err)
	}
	if _, ok := a.(*auth.Proxy); !ok || p == nil {
		t.Fatalf("default authenticator is %T, want the proxy", a)
	}
	if got, f := a.Authenticate(authRequest("192.0.2.1:1")); f == nil {
		t.Errorf("the default configuration signed a request with no credentials in: %+v", got)
	}
	for _, mode := range []string{"", "None", "off", "no-auth", "disabled"} {
		if a, _, err := buildAuthenticator(resolvedAuth(false, false, func(c *config.AuthConfig) { c.Mode = mode }), logs.logger()); err == nil || a != nil {
			t.Errorf("mode %q built %v (%v), want an error", mode, a, err)
		}
	}
}

// Dev and demo outrank auth.mode: with none set they are still their own fixed identity, and the
// none warnings are not logged (nothing is anonymous there).
func TestDevAndDemoOutrankNoneMode(t *testing.T) {
	for _, tc := range []struct {
		name      string
		dev, demo bool
		user      string
	}{{"dev", true, false, "dev"}, {"demo", false, true, "demo"}} {
		var logs logCapture
		res := noneResolved("0.0.0.0:8080")
		res.Config.Dev, res.Config.Demo = tc.dev, tc.demo
		a, _, err := buildAuthenticator(res, logs.logger())
		if err != nil {
			t.Fatal(err)
		}
		got, _ := a.Authenticate(authRequest("192.0.2.1:1"))
		if got.User != tc.user || got.Kind == auth.KindNone {
			t.Errorf("%s: principal = %+v", tc.name, got)
		}
		if _, ok := a.(auth.Warner); ok {
			t.Errorf("%s: carries the auth_none warning", tc.name)
		}
		if n := len(logs.with(t, "WARN", "")); n != 0 || len(logs.with(t, "INFO", "auth.mode is ignored: dev and demo mode sign every request in as a fixed identity")) != 1 {
			t.Errorf("%s: logs:\n%s", tc.name, &logs.buf)
		}
	}
}
