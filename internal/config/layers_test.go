package config

import (
	"os"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/settings"
)

// savedStore opens a writable store in a temp dir with the given dotted paths saved, the way
// the settings API will save a form (one key at a time).
func savedStore(t *testing.T, kv map[string]any) *settings.Store {
	t.Helper()
	st, err := settings.Open(t.TempDir(), settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for path, v := range kv {
		if err := st.Set(path, v); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func resolveLayers(t *testing.T, st *settings.Store, file string, env ...string) (*Resolved, string) {
	t.Helper()
	src := Sources{Environ: append([]string{"DASH_DEV=true"}, env...), UI: st.Values(), UIRef: st.Path()}
	if file != "" {
		src.File = &File{Path: "/etc/dash/config.yaml", Data: []byte(file)}
	}
	res, log, err := resolveWith(t, src)
	if err != nil {
		t.Fatal(err)
	}
	return res, log
}

// The store is a sparse layer below file and env, never a snapshot: whatever the file or the
// environment says later still wins, and what they stop saying shows the stored value again
// (P1.6 acceptance).
func TestUIValuesAreARealLayerBelowFileAndEnv(t *testing.T) {
	st := savedStore(t, map[string]any{
		"plugins.grafana.url":  "https://grafana.ui.example.com",
		"plugins.headlamp.url": "https://headlamp.ui.example.com",
	})
	const file = "plugins: {grafana: {url: 'https://grafana.file.example.com'}}\n"

	res, _ := resolveLayers(t, st, "")
	if c := res.Config; c.GrafanaURL != "https://grafana.ui.example.com" || c.HeadlampURL != "https://headlamp.ui.example.com" {
		t.Fatalf("stored values must apply: grafana %q headlamp %q", c.GrafanaURL, c.HeadlampURL)
	}
	if p := res.Provenance["plugins.grafana.url"]; p != (Provenance{SourceUI, st.Path()}) {
		t.Errorf("provenance = %+v, want the store file", p)
	}

	// A file value saved after the UI save wins, and the environment wins over both.
	res, _ = resolveLayers(t, st, file)
	if res.Config.GrafanaURL != "https://grafana.file.example.com" || res.Config.HeadlampURL != "https://headlamp.ui.example.com" {
		t.Errorf("file over UI: grafana %q headlamp %q", res.Config.GrafanaURL, res.Config.HeadlampURL)
	}
	res, _ = resolveLayers(t, st, file, "DASH_PLUGINS_GRAFANA_URL=https://grafana.env.example.com", "DASH_PLUGINS_HEADLAMP_URL=https://headlamp.env.example.com")
	if res.Config.GrafanaURL != "https://grafana.env.example.com" || res.Config.HeadlampURL != "https://headlamp.env.example.com" {
		t.Errorf("env over file and UI: grafana %q headlamp %q", res.Config.GrafanaURL, res.Config.HeadlampURL)
	}
	if p := res.Provenance["plugins.grafana.url"]; p != (Provenance{SourceEnv, "DASH_PLUGINS_GRAFANA_URL"}) {
		t.Errorf("provenance = %+v", p)
	}

	// Removing the file value reveals the stored one on the next resolution.
	res, _ = resolveLayers(t, st, "")
	if res.Config.GrafanaURL != "https://grafana.ui.example.com" {
		t.Errorf("after the file value is gone: grafana %q, want the stored value", res.Config.GrafanaURL)
	}

	// Nothing the file or the environment said was copied into the store.
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"file.example.com", "env.example.com"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("the store holds %q:\n%s", leak, raw)
		}
	}
}

// What the UI may not set is ignored with a warning wherever it came from, and cannot move
// the store itself.
func TestStoredValuesAtUIIneligiblePathsAreIgnored(t *testing.T) {
	dir := t.TempDir()
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for path, v := range map[string]any{
		"server.listen":       "0.0.0.0:1",
		"server.publicURL":    "https://evil.example.com",
		"settings.ui":         "readonly",
		"dataDir":             "/tmp/elsewhere",
		"plugins.grafana.url": "https://grafana.ui.example.com",
	} {
		if err := st.Set(path, v); err != nil {
			t.Fatal(err)
		}
	}

	res, log := resolveLayers(t, st, "")
	c := res.Config
	if c.Listen != "127.0.0.1:8080" || c.PublicURL != "http://localhost:5173" || c.SettingsUI != SettingsEditable || c.DataDir != "/data" {
		t.Errorf("a stored value moved an ineligible field: %+v", c)
	}
	if c.GrafanaURL != "https://grafana.ui.example.com" {
		t.Errorf("the eligible stored value was lost: %q", c.GrafanaURL)
	}
	for _, p := range []string{"server.listen", "server.publicURL", "settings.ui", "dataDir"} {
		if !strings.Contains(log, "path="+p+" ") {
			t.Errorf("no warning for %s:\n%s", p, log)
		}
	}

	// The store is located without the UI layer: nothing stored can redirect it.
	boot, err := BootstrapOf(Sources{Environ: []string{"DASH_DEV=true"}, UI: st.Values()})
	if err != nil {
		t.Fatal(err)
	}
	if boot.DataDir != "/data" || boot.SettingsUI != SettingsEditable || boot.Demo {
		t.Errorf("Bootstrap = %+v", boot)
	}
}

func TestBootstrapReadsDataDirAndSettingsUIFromFileAndEnvOnly(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte("dataDir: /var/lib/dash\nsettings: {ui: readonly}\n")}
	boot, err := BootstrapOf(Sources{Environ: []string{"DASH_DEMO=true"}, File: file})
	if err != nil {
		t.Fatal(err)
	}
	if boot != (Bootstrap{DataDir: "/var/lib/dash", SettingsUI: SettingsReadOnly, Demo: true}) {
		t.Errorf("Bootstrap = %+v", boot)
	}
	boot, err = BootstrapOf(Sources{Environ: []string{"DASH_DEMO=true", "DASH_DATA_DIR=/srv/dash"}, File: file})
	if err != nil || boot.DataDir != "/srv/dash" {
		t.Errorf("env over file: %+v, %v", boot, err)
	}
	if _, err := BootstrapOf(Sources{Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_PROXMOX_URLL=x"}}); err == nil {
		t.Error("an unknown DASH_PLUGINS_* variable is still an error")
	}
}
