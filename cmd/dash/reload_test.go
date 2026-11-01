package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// lockedBuf is a log sink the test can read while the reloader writes to it.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The wiring as main runs it: a rotated secret file reaches the credential of a client built
// from the loaded config, an invalid edit changes nothing, and /readyz answers 200 throughout
// (P1.7 acceptance).
func TestReloadedSecretReachesTheProxmoxClientWhileReadinessHolds(t *testing.T) {
	pve := newFakePVE(t, always(200, nodesBody("pve")))
	dir := t.TempDir()
	cfgPath, tokenPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "token")
	writeTestFile(t, cfgPath, "plugins:\n  headlamp:\n    url: https://hl-a.example.com\n")
	writeTestFile(t, tokenPath, "token-one-0123456789\n")
	startEnv(t, dir,
		"DASH_CONFIG="+cfgPath, "DASH_PLUGINS_PROXMOX_URL="+pve.URL,
		"DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE="+tokenPath)

	var logs lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	reg := secret.NewRegistry()
	res, store, err := loadConfig(reg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	health := api.New(api.Deps{Cfg: res.Config, Hub: hub.New(), Secrets: reg, Ready: func() bool { return true }}).HealthHandler()
	ready := func() int {
		rec := httptest.NewRecorder()
		health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	pveClient := upstream.NewProxmox(res.Config.ProxmoxURL, "", res.Config.ProxmoxTokenID, res.Config.ProxmoxTokenSecret, nil)
	lastAuth := func() string {
		if _, err := pveClient.Nodes(context.Background()); err != nil {
			t.Fatal(err)
		}
		pve.mu.Lock()
		defer pve.mu.Unlock()
		return pve.auth[len(pve.auth)-1]
	}

	reloader := config.NewReloader(res, store, config.ReloadOptions{Interval: time.Millisecond})
	events := make(chan config.Change, 8)
	reloader.Subscribe(func(c config.Change) { events <- c })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); reloader.Run(ctx) }()
	defer func() { cancel(); <-done }()

	if got := lastAuth(); !strings.HasSuffix(got, "=token-one-0123456789") || ready() != 200 {
		t.Fatalf("before any change: %q, ready %d", got, ready())
	}

	// An invalid config file next to a rotated secret: the whole edit is refused.
	writeTestFile(t, tokenPath, "token-two-9876543210\n")
	writeTestFile(t, cfgPath, "plugins:\n  headlamp:\n    urll: https://hl-b.example.com\n")
	waitFor(t, func() bool { return strings.Contains(logs.String(), "configuration reload failed") })
	if got := lastAuth(); !strings.HasSuffix(got, "=token-one-0123456789") || ready() != 200 {
		t.Errorf("an invalid edit changes nothing: %q, ready %d", got, ready())
	}

	// Fixed: the rotation applies live, the changed URL is flagged for a restart.
	writeTestFile(t, cfgPath, "plugins:\n  headlamp:\n    url: https://hl-b.example.com\n")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case c := <-events:
			seen[c.Section] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("change events so far: %v\n%s", seen, logs.String())
		}
	}
	if !seen["plugins.proxmox"] || !seen["plugins.headlamp"] {
		t.Errorf("sections = %v", seen)
	}
	if got := lastAuth(); !strings.HasSuffix(got, "=token-two-9876543210") || ready() != 200 {
		t.Errorf("after the reload: %q, ready %d", got, ready())
	}
	if r := reloader.RestartRequired(); len(r) != 1 || r[0] != "plugins.headlamp.url" {
		t.Errorf("restart required = %v", r)
	}
	for _, s := range []string{"token-one-0123456789", "token-two-9876543210"} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("the log shows a secret:\n%s", logs.String())
		}
	}
}

// waitFor polls cond until it holds; the deadline only bounds a failing test.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
