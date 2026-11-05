package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/secret"
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

// writeTestFile replaces path with body in one rename of a temp file from the same directory, so
// a reloader polling path sees the old file or the new one, never the empty or half-written file
// that os.WriteFile leaves between its truncation and the end of its write.
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmp := f.Name()
	_, werr := f.WriteString(body)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp, 0o600)); err != nil {
		_ = os.Remove(tmp)
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		t.Fatal(err)
	}
}

// fakePVE is a Proxmox API with one node and one VM that records the Authorization header of
// every request.
type fakePVE struct {
	*httptest.Server
	mu   sync.Mutex
	auth []string
}

func newFakePVE(t *testing.T) *fakePVE { return newFakePVEFor(t, "pve") }

// newFakePVEFor is newFakePVE for a node of the given name.
func newFakePVEFor(t *testing.T, node string) *fakePVE {
	t.Helper()
	n := "/api2/json/nodes/" + node
	bodies := map[string]string{
		"/api2/json/nodes":             `{"data":[{"node":"` + node + `","status":"online"}]}`,
		n + "/status":                  `{"data":{"uptime":1,"pveversion":"pve-manager/9.0.5/x","current-kernel":{"release":"6.8"},"cpuinfo":{"model":"Example CPU"}}}`,
		"/api2/json/cluster/resources": `{"data":[{"id":"qemu/101","vmid":101,"name":"vm-101","status":"running","template":0,"maxcpu":2,"maxmem":1073741824}]}`,
		"/api2/json/cluster/tasks":     `{"data":[]}`,
		n + "/storage":                 `{"data":[]}`,

		n + "/qemu/101/config":                       `{"data":{"name":"vm-101","cores":2,"memory":"1024"}}`,
		n + "/qemu/101/agent/network-get-interfaces": `{"data":{"result":[]}}`,
		n + "/qemu/101/snapshot":                     `{"data":[]}`,
	}
	f := &fakePVE{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// lastAuth is the Authorization header of the latest request.
func (f *fakePVE) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) == 0 {
		return ""
	}
	return f.auth[len(f.auth)-1]
}

// The wiring as main runs it, with the proxmox plugin of the framework: a rotated secret file
// reaches the credential of the plugin's client (its next request carries the new token), an
// invalid edit changes nothing, a proxmox change is applied live (no restart is asked for), and
// /readyz answers 200 throughout (P1.7 acceptance).
func TestReloadedSecretReachesTheProxmoxPluginWhileReadinessHolds(t *testing.T) {
	pve := newFakePVE(t)
	dir := t.TempDir()
	cfgPath, tokenPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "token")
	writeTestFile(t, cfgPath, "server:\n  limits:\n    streams: 100\n")
	writeTestFile(t, tokenPath, "token-one-0123456789\n")
	startEnv(t, dir,
		"DASH_CONFIG="+cfgPath, "DASH_PLUGINS_PROXMOX_URL="+pve.URL, `DASH_SERVER_READY_REQUIRES=["kubernetes"]`,
		"DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE="+tokenPath)

	var logs lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	reg := secret.NewRegistry()
	res, store, err := loadConfig(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Readiness as main wires it: the supervisor, waiting for the kubernetes plugin to settle
	// (server.readyRequires = [kubernetes]; here it has no cluster and is unavailable), not the
	// reloader.
	cfg := res.Config
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, false)
	sup, reloader := fw.sup, fw.reloader
	ov := overview.New(overview.Deps{Hosts: sup, Contributors: sup.Contributors})
	ctx, cancel := context.WithCancel(context.Background())
	if err := fw.start(ctx, registry(nil), func() { ov.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	srv := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Secrets: reg, Health: sup, Plugins: sup, Auth: authtest.Proxy("alice")})
	srv.MarkServing()
	health := srv.HealthHandler()
	ready := func() int {
		rec := httptest.NewRecorder()
		health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	// detail asks the plugin's route for VM 101, which makes the plugin call Proxmox now.
	detail := func() int {
		r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/plugins/proxmox/vms/101", nil)
		r.RemoteAddr = "127.0.0.1:41000" // the proxy next to dash, the peer whose identity header dash believes
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	lastAuth := func() string {
		waitFor(t, func() bool { return detail() == http.StatusOK })
		return pve.lastAuth()
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
	writeTestFile(t, cfgPath, "server:\n  limits:\n    streamss: 200\n")
	waitFor(t, func() bool { return strings.Contains(logs.String(), "configuration reload failed") })
	writeTestFile(t, tokenPath, "token-two-9876543210\n")
	time.Sleep(20 * time.Millisecond) // many ticks at 1 ms
	if got := lastAuth(); !strings.HasSuffix(got, "=token-one-0123456789") || ready() != 200 {
		t.Errorf("an invalid edit changes nothing: %q, ready %d", got, ready())
	}

	// Fixed: the rotation applies live to the plugin's client.
	writeTestFile(t, cfgPath, "server:\n  limits:\n    streams: 200\n")
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case c := <-events:
			seen[c.Section] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("change events so far: %v\n%s", seen, logs.String())
		}
	}
	if !seen["plugins.proxmox"] || !seen["server"] {
		t.Errorf("sections = %v", seen)
	}
	if got := lastAuth(); !strings.HasSuffix(got, "=token-two-9876543210") || ready() != 200 {
		t.Errorf("after the reload: %q, ready %d", got, ready())
	}
	// Only the startup setting needs a restart: the proxmox plugin applied its change.
	if r := reloader.RestartRequired(); len(r) != 1 || r[0] != "server.limits.streams" {
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
