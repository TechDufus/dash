package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
)

// A file reload of auth.proxy.adminUsers reaches the running authenticator: the role of the next
// request changes with no restart, and the reloader reports nothing as restart-required.
func TestAdminUsersReloadAppliesOnTheNextRequest(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, cfgPath, "auth:\n  mode: proxy\n  proxy:\n    adminUsers: [alice]\n")
	startEnv(t, dir, "DASH_CONFIG="+cfgPath, "DASH_AUTH_MODE=") // the file sets the mode

	reg := secret.NewRegistry()
	res, store, err := loadConfig(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, proxy, err := buildAuthenticator(res, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, false)
	fw.reloadAuth(proxy)
	// Subscribers run in the order they were added, so once this one has seen the change the
	// authenticator has applied it.
	events := make(chan config.Change, 8)
	fw.reloader.Subscribe(func(c config.Change) { events <- c })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done }()

	srv := api.New(api.Deps{Cfg: res.Config, Hub: fw.hub, Secrets: reg, Health: fw.sup, Plugins: fw.sup, Auth: a})
	meAs := func(user string) string {
		r := httptest.NewRequest(http.MethodGet, res.Config.PublicURL+"/api/v1/me", nil)
		r.RemoteAddr = "127.0.0.1:41000"
		r.Header.Set("X-Forwarded-Preferred-Username", user)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("me as %s = %d %s", user, w.Code, w.Body)
		}
		return w.Body.String()
	}
	if got := meAs("alice"); !strings.Contains(got, `"role":"admin"`) {
		t.Fatalf("before the reload alice = %s", got)
	}

	writeTestFile(t, cfgPath, "auth:\n  mode: proxy\n  proxy:\n    adminUsers: [bob]\n")
	waitFor(t, func() bool {
		select {
		case c := <-events:
			return c.Section == "auth"
		default:
			return false
		}
	})
	if got := meAs("alice"); !strings.Contains(got, `"role":"viewer"`) {
		t.Errorf("after the reload alice = %s, want viewer", got)
	}
	if got := meAs("bob"); !strings.Contains(got, `"role":"admin"`) {
		t.Errorf("after the reload bob = %s, want admin", got)
	}
	if r := fw.reloader.RestartRequired(); len(r) != 0 {
		t.Errorf("restart required = %v, want none: the admin lists are live", r)
	}
}

// The other auth keys are not live: a change to the mode or the trusted proxies is reported as
// needing a restart, and the running authenticator keeps what it started with: still the proxy
// mode, still believing only the startup peers.
func TestOtherAuthKeysNeedARestart(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, cfgPath, "auth:\n  mode: proxy\n  proxy:\n    adminUsers: [alice]\n")
	startEnv(t, dir, "DASH_CONFIG="+cfgPath, "DASH_AUTH_MODE=") // the file sets the mode, and the reload changes it

	reg := secret.NewRegistry()
	res, store, err := loadConfig(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, proxy, err := buildAuthenticator(res, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, false)
	fw.reloadAuth(proxy)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done }()

	srv := api.New(api.Deps{Cfg: res.Config, Hub: fw.hub, Secrets: reg, Health: fw.sup, Plugins: fw.sup, Auth: a})
	// meFrom asks /api/v1/me from peer, as user when it is not empty.
	meFrom := func(peer, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, res.Config.PublicURL+"/api/v1/me", nil)
		r.RemoteAddr = peer
		if user != "" {
			r.Header.Set("X-Forwarded-Preferred-Username", user)
		}
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w
	}
	check := func(when string) {
		t.Helper()
		if w := meFrom("127.0.0.1:41000", "alice"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"authMode":"proxy"`) || !strings.Contains(w.Body.String(), `"role":"admin"`) {
			t.Errorf("%s: alice from the startup peer = %d %s, want the proxy-mode admin", when, w.Code, w.Body)
		}
		// A peer the new file would trust, and no identity at all (what none mode would accept):
		// both are still refused.
		if w := meFrom("10.0.0.5:41000", "alice"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: alice from 10.0.0.5 = %d %s, want 401 (not a startup peer)", when, w.Code, w.Body)
		}
		if w := meFrom("127.0.0.1:41000", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: no identity header = %d %s, want 401 (the mode is still proxy)", when, w.Code, w.Body)
		}
	}
	check("before the reload")

	// none off loopback needs its opt-in to load at all; that key is restart-only too.
	writeTestFile(t, cfgPath, "auth:\n  mode: none\n  none:\n    allowRemote: true\n  proxy:\n    adminUsers: [alice]\n    trustedProxies: [\"10.0.0.0/8\"]\n")
	waitFor(t, func() bool { return len(fw.reloader.RestartRequired()) > 0 })
	if got, want := fw.reloader.RestartRequired(), []string{"auth.mode", "auth.none.allowRemote", "auth.proxy.trustedProxies"}; !slices.Equal(got, want) {
		t.Errorf("restart required = %v, want %v", got, want)
	}
	check("after the reload")
}
