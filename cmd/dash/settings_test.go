package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// startEnv is the environment of a plain production start with the given data directory.
func startEnv(t *testing.T, dataDir string, more ...string) {
	t.Helper()
	t.Setenv("DASH_PUBLIC_URL", "https://dash.example.com")
	t.Setenv("DASH_DATA_DIR", dataDir)
	for _, kv := range more {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestLoadConfigAppliesStoredSettingsBelowEnvironment(t *testing.T) {
	dir := t.TempDir()
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for path, v := range map[string]any{"plugins.grafana.url": "https://grafana.ui.example.com", "plugins.headlamp.url": "https://headlamp.ui.example.com"} {
		if err := st.Set(path, v); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	startEnv(t, dir, "DASH_PLUGINS_HEADLAMP_URL=https://headlamp.env.example.com")

	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if c := res.Config; c.GrafanaURL != "https://grafana.ui.example.com" || c.HeadlampURL != "https://headlamp.env.example.com" {
		t.Errorf("grafana %q headlamp %q: want the stored value, and the environment over it", c.GrafanaURL, c.HeadlampURL)
	}
	if store.ReadOnly() {
		t.Error("a writable data directory gives a writable store")
	}
}

// A second dash on the same data directory refuses to start and says why; closing the first
// (shutdown) frees it (P1.6 acceptance).
func TestSecondInstanceOnTheSameDataDirIsRefused(t *testing.T) {
	startEnv(t, t.TempDir())
	_, first, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := loadConfig(secret.NewRegistry())
	if !errors.Is(err, settings.ErrLocked) || second != nil {
		t.Fatalf("second start: store %v, err %v; want ErrLocked", second, err)
	}
	if !strings.Contains(err.Error(), "run one dash instance per data directory") {
		t.Errorf("the message must say what to do: %v", err)
	}
	first.Close()
	_, third, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatalf("after the first instance closed its store: %v", err)
	}
	third.Close()
}

func TestUnwritableDataDirIsReadOnlyWithOneInfo(t *testing.T) {
	startEnv(t, filepath.Join(t.TempDir(), "missing"))
	log := captureLog(t)
	_, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.ReadOnly() {
		t.Error("a missing data directory gives a read-only store")
	}
	out := log.String()
	if n := strings.Count(out, "settings are read-only"); n != 1 || !strings.Contains(out, "level=INFO") || !strings.Contains(out, "not writable") {
		t.Errorf("want exactly one INFO naming the data directory, got:\n%s", out)
	}
	if _, err := os.Stat(store.Path()); err == nil {
		t.Error("a read-only store creates nothing")
	}
}

func TestSettingsUIReadonlyIsReadOnlyEvenWhenWritable(t *testing.T) {
	dir := t.TempDir()
	startEnv(t, dir, "DASH_SETTINGS_UI=readonly")
	log := captureLog(t)
	_, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.ReadOnly() || !strings.Contains(log.String(), "settings.ui is readonly") {
		t.Errorf("readonly=%v, log:\n%s", store.ReadOnly(), log)
	}
	// Read-only takes no lock: another instance can still start (GitOps replicas).
	_, other, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatalf("a read-only instance takes no lock: %v", err)
	}
	other.Close()
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err == nil {
		t.Error("a read-only store must not create the lock file")
	}
}

// Startup resolves the layers more than once (the data directory comes from the first pass) and
// the reloader resolves them again; a legacy variable still warns once, not once per pass.
func TestStartupWarnsOncePerLegacyVariable(t *testing.T) {
	startEnv(t, t.TempDir(), // DASH_PUBLIC_URL is legacy, too
		"DASH_GRAFANA_URL=https://grafana.example.com", "DASH_HEADLAMP_URL=https://headlamp.example.com",
		"DASH_PROXMOX_URL=https://pve.example.com:8006", "DASH_PROXMOX_TOKEN=u@pve!t=legacy-secret-0123456789")
	log := captureLog(t)
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// The reloader's first poll resolves everything once more.
	reloader := config.NewReloader(res, store, config.ReloadOptions{Interval: time.Millisecond})
	events := make(chan config.Change, 8)
	reloader.Subscribe(func(c config.Change) { events <- c })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); reloader.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	select {
	case c := <-events:
		t.Errorf("nothing changed, yet the reloader reported %+v", c)
	default:
	}

	out := log.String()
	for _, name := range []string{"DASH_PUBLIC_URL", "DASH_GRAFANA_URL", "DASH_HEADLAMP_URL", "DASH_PROXMOX_URL", "DASH_PROXMOX_TOKEN"} {
		if n := strings.Count(out, "name="+name+" "); n != 1 {
			t.Errorf("%s warned %d times, want once:\n%s", name, n, out)
		}
	}
	if n := strings.Count(out, "deprecated environment variable"); n != 5 {
		t.Errorf("%d deprecation warnings, want 5:\n%s", n, out)
	}
}

func TestDemoModeOpensNoStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	startEnv(t, dir, "DASH_DEMO=true")
	log := captureLog(t)
	res, store, err := loadConfig(secret.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if store != nil || !res.Config.Demo {
		t.Errorf("store %v, demo %v", store, res.Config.Demo)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("demo mode created the data directory")
	}
	if strings.Contains(log.String(), "settings") {
		t.Errorf("demo mode says nothing about settings:\n%s", log)
	}
}
