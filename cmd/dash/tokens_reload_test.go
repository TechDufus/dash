package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/tokens"
)

// auth.tokens from the file is live (contract D5): removing an entry revokes it at the next
// request, with no restart, and the reloader reports nothing as restart-required.
func TestStaticTokenRemovedByAReloadIsRefused(t *testing.T) {
	token, _, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	const head = "auth:\n  mode: proxy\n"
	writeTestFile(t, cfgPath, head+"  tokens:\n    - {name: ci, role: viewer, audience: api, scopes: [read], hash: \""+hash+"\"}\n")
	startEnv(t, dir, "DASH_CONFIG="+cfgPath, "DASH_AUTH_MODE=")

	reg := secret.NewRegistry()
	res, store, err := loadConfig(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, _, err := buildAuthenticator(res, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	resolver := tokens.New(nil, nil, res.Tree.Auth.Tokens, nil)
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, false)
	fw.reloadTokens(resolver)
	events := make(chan config.Change, 8)
	fw.reloader.Subscribe(func(c config.Change) { events <- c })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	defer func() { cancel(); <-done }()

	srv := api.New(api.Deps{Cfg: res.Config, Hub: fw.hub, Secrets: reg, Health: fw.sup, Plugins: fw.sup, Auth: a, Tokens: resolver})
	me := func() int {
		r := httptest.NewRequest(http.MethodGet, res.Config.PublicURL+"/api/v1/me", nil)
		r.RemoteAddr = "127.0.0.1:41000"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, r)
		return w.Code
	}
	if got := me(); got != http.StatusOK {
		t.Fatalf("before the reload: %d, want 200", got)
	}

	writeTestFile(t, cfgPath, head)
	waitFor(t, func() bool {
		select {
		case c := <-events:
			return c.Section == "auth"
		default:
			return false
		}
	})
	if got := me(); got != http.StatusUnauthorized {
		t.Errorf("after the entry was removed: %d, want 401", got)
	}
	if r := fw.reloader.RestartRequired(); len(r) != 0 {
		t.Errorf("restart required = %v, want none: auth.tokens is live", r)
	}
}
