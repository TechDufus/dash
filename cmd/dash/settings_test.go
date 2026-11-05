package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// startEnv is the environment of a plain production start with the given data directory, behind
// an identity proxy (auth.mode proxy, set explicitly as a deployment does since local became the
// default); more may override any of it.
func startEnv(t *testing.T, dataDir string, more ...string) {
	t.Helper()
	t.Setenv("DASH_SERVER_PUBLIC_URL", "https://dash.example.com")
	t.Setenv("DASH_DATA_DIR", dataDir)
	t.Setenv("DASH_AUTH_MODE", "proxy")
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

	res, store, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if p := res.Tree.Plugins; p.Grafana.URL != "https://grafana.ui.example.com" || p.Headlamp.URL != "https://headlamp.env.example.com" {
		t.Errorf("grafana %q headlamp %q: want the stored value, and the environment over it", p.Grafana.URL, p.Headlamp.URL)
	}
	if store.ReadOnly() {
		t.Error("a writable data directory gives a writable store")
	}
}

// A second dash on the same data directory refuses to start and says why; closing the first
// (shutdown) frees it (P1.6 acceptance).
func TestSecondInstanceOnTheSameDataDirIsRefused(t *testing.T) {
	startEnv(t, t.TempDir())
	_, first, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := loadConfig(secret.NewRegistry(), nil)
	if !errors.Is(err, settings.ErrLocked) || second != nil {
		t.Fatalf("second start: store %v, err %v; want ErrLocked", second, err)
	}
	if !strings.Contains(err.Error(), "run one dash instance per data directory") {
		t.Errorf("the message must say what to do: %v", err)
	}
	first.Close()
	_, third, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatalf("after the first instance closed its store: %v", err)
	}
	third.Close()
}

func TestUnwritableDataDirIsReadOnlyWithOneInfo(t *testing.T) {
	startEnv(t, filepath.Join(t.TempDir(), "missing"))
	log := captureLog(t)
	_, store, err := loadConfig(secret.NewRegistry(), nil)
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
	_, store, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.ReadOnly() || !strings.Contains(log.String(), "settings.ui is readonly") {
		t.Errorf("readonly=%v, log:\n%s", store.ReadOnly(), log)
	}
	// Read-only takes no lock: another instance can still start (GitOps replicas).
	_, other, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatalf("a read-only instance takes no lock: %v", err)
	}
	other.Close()
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err == nil {
		t.Error("a read-only store must not create the lock file")
	}
}

// Startup resolves the layers more than once (the data directory comes from the first pass) and
// the reloader resolves them again; an unknown variable still warns once, not once per pass.
func TestStartupWarnsOncePerUnknownVariable(t *testing.T) {
	startEnv(t, t.TempDir(),
		"DASH_PLUGINS_GRAFANA_URL=https://grafana.example.com", "DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com",
		"DASH_LISTN=127.0.0.1:1", "DASH_FOO=bar")
	log := captureLog(t)
	res, store, err := loadConfig(secret.NewRegistry(), nil)
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
	for _, name := range []string{"DASH_LISTN", "DASH_FOO"} {
		if n := strings.Count(out, "name="+name+"\n") + strings.Count(out, "name="+name+" "); n != 1 {
			t.Errorf("%s warned %d times, want once:\n%s", name, n, out)
		}
	}
	if n := strings.Count(out, "ignoring unknown environment variable"); n != 2 {
		t.Errorf("%d unknown-variable warnings, want 2:\n%s", n, out)
	}
}

func TestDemoModeOpensNoStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	startEnv(t, dir, "DASH_DEMO=true")
	log := captureLog(t)
	res, store, err := loadConfig(secret.NewRegistry(), nil)
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

// The settings API learns from the backend why the store is read-only and whether a secret
// can be saved: settings.ui readonly is config, a data dir that cannot be written is data_dir.
func TestSettingsBackendDescribesTheStore(t *testing.T) {
	key := "DASH_SECRET_KEY=" + testKey(1)
	for name, tc := range map[string]struct {
		missing bool
		env     []string
		want    model.SettingsStore
	}{
		"editable":          {env: []string{key}, want: model.SettingsStore{Writable: true, SecretKey: true}},
		"settings.ui":       {env: []string{"DASH_SETTINGS_UI=readonly"}, want: model.SettingsStore{Readonly: true, Reason: model.SettingsStoreReasonConfig, Writable: true}},
		"missing data dir":  {missing: true, want: model.SettingsStore{Readonly: true, Reason: model.SettingsStoreReasonDataDir}},
		"missing, readonly": {missing: true, env: []string{"DASH_SETTINGS_UI=readonly"}, want: model.SettingsStore{Readonly: true, Reason: model.SettingsStoreReasonConfig}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.missing {
				dir = filepath.Join(dir, "missing")
			}
			startEnv(t, dir, tc.env...)
			captureLog(t)
			reg := secret.NewRegistry()
			res, store, alog, err := openState(reg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = alog.Close(); _ = store.Close() })
			fw := newFramework(res, store, config.ReloadOptions{}, reg, nil, nil, false)
			b := newSettingsBackend(res, store, fw.reloader, fw.sup, alog, reg)
			tc.want.DataDir = dir
			if got := b.Store(); got != tc.want {
				t.Errorf("store = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// PluginConfig hands Check exactly what the supervisor would run the plugin with, so a check
// of the running configuration reaches the upstream instead of failing on its type.
func TestSettingsBackendPluginConfigIsWhatTheSupervisorChecks(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[0,"1"]}}`))
	}))
	t.Cleanup(up.Close)
	startEnv(t, t.TempDir(), "DASH_PLUGINS_PROMETHEUS_URL="+up.URL)
	captureLog(t)
	reg := secret.NewRegistry()
	res, store, alog, err := openState(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = alog.Close(); _ = store.Close() })
	fw := newFramework(res, store, config.ReloadOptions{}, reg, nil, nil, false)
	if err := fw.sup.Add(prometheus.New); err != nil {
		t.Fatal(err)
	}
	b := newSettingsBackend(res, store, fw.reloader, fw.sup, alog, reg)

	cfg, fromUI, ok := b.PluginConfig(b.Resolved(), "prometheus")
	if p, isProm := cfg.(*pluginconf.Prometheus); !ok || fromUI || !isProm || p.URL != up.URL {
		t.Fatalf("PluginConfig = %#v, fromUI %v, ok %v", cfg, fromUI, ok)
	}
	if !b.Testable("prometheus") || b.Testable("grafana") {
		t.Error("prometheus has a connection check, grafana does not")
	}
	rep, err := b.Check(context.Background(), "prometheus", cfg, fromUI)
	if err != nil || len(rep.Checks) == 0 || !rep.Checks[0].OK {
		t.Errorf("Check = %+v, %v", rep, err)
	}
	if _, _, ok := b.PluginConfig(b.Resolved(), "nope"); ok {
		t.Error("an unknown id has no configuration")
	}
}

func TestOwnNamespace(t *testing.T) {
	file := func(content string, err error) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if path != serviceAccountNamespace {
				t.Errorf("read %s", path)
			}
			return []byte(content), err
		}
	}
	env := func(v string) func(string) string { return func(string) string { return v } }
	for name, tc := range map[string]struct {
		getenv   func(string) string
		readFile func(string) ([]byte, error)
		want     string
	}{
		"POD_NAMESPACE first":  {env(" dash "), file("other", nil), "dash"},
		"service account file": {env(""), file("dash\n", nil), "dash"},
		"outside a cluster":    {env(""), file("", fs.ErrNotExist), ""},
	} {
		if got := ownNamespace(tc.getenv, tc.readFile); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
