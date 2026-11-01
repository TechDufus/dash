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
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/topics"
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
	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    suspendReasonAnnotation: example.com/a\n")
	writeTestFile(t, tokenPath, "token-one-0123456789\n")
	startEnv(t, dir,
		"DASH_CONFIG="+cfgPath, "DASH_PLUGINS_PROXMOX_URL="+pve.URL, `DASH_SERVER_READY_REQUIRES=["kubernetes"]`,
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
	// Readiness as main wires it: the supervisor, waiting for the informers of the topic manager
	// (server.readyRequires = [kubernetes]), not the reloader.
	cfg := res.Config
	pveClient := upstream.NewProxmox(cfg.ProxmoxURL, "", cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, nil)
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, false)
	sup, reloader := fw.sup, fw.reloader
	mgr := topics.New(topics.Deps{
		Cfg: cfg, Cluster: testCluster(),
		PVE: pveClient, Hosts: sup, Services: sup.Service,
		Links: links.New(cfg), Secrets: reg,
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := fw.start(ctx, mgr.Plugins(), func() { mgr.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	srv := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Secrets: reg, Health: sup})
	srv.MarkServing()
	health := srv.HealthHandler()
	ready := func() int {
		rec := httptest.NewRecorder()
		health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	lastAuth := func() string {
		if _, err := pveClient.Nodes(context.Background()); err != nil {
			t.Fatal(err)
		}
		pve.mu.Lock()
		defer pve.mu.Unlock()
		return pve.auth[len(pve.auth)-1]
	}

	events := make(chan config.Change, 8)
	reloader.Subscribe(func(c config.Change) { events <- c })
	done := make(chan struct{})
	go func() { defer close(done); reloader.Run(ctx) }()
	defer func() { cancel(); <-done }()

	waitFor(t, func() bool { return ready() == 200 })

	if got := lastAuth(); !strings.HasSuffix(got, "=token-one-0123456789") || ready() != 200 {
		t.Fatalf("before any change: %q, ready %d", got, ready())
	}

	// An invalid config file, then a rotated secret: while the file stays invalid, the rotation
	// is refused with it. The token is rotated only once the reloader has seen the invalid
	// file; a tick that read the old file before both writes could otherwise apply the rotation.
	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    suspendReasonAnnotationn: example.com/b\n")
	waitFor(t, func() bool { return strings.Contains(logs.String(), "configuration reload failed") })
	writeTestFile(t, tokenPath, "token-two-9876543210\n")
	time.Sleep(20 * time.Millisecond) // many ticks at 1 ms
	if got := lastAuth(); !strings.HasSuffix(got, "=token-one-0123456789") || ready() != 200 {
		t.Errorf("an invalid edit changes nothing: %q, ready %d", got, ready())
	}

	// Fixed: the rotation applies live, the changed URL is flagged for a restart.
	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    suspendReasonAnnotation: example.com/b\n")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case c := <-events:
			seen[c.Section] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("change events so far: %v\n%s", seen, logs.String())
		}
	}
	if !seen["plugins.proxmox"] || !seen["plugins.flux"] {
		t.Errorf("sections = %v", seen)
	}
	if got := lastAuth(); !strings.HasSuffix(got, "=token-two-9876543210") || ready() != 200 {
		t.Errorf("after the reload: %q, ready %d", got, ready())
	}
	if r := reloader.RestartRequired(); len(r) != 1 || r[0] != "plugins.flux.suspendReasonAnnotation" {
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
