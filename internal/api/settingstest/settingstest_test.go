package settingstest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The fake resolves the three layers with their provenance, and its store and reloader are the
// real ones: an Apply plus Reload inside WithLock is the running configuration at once.
func TestFakeResolvesLayersAndAppliesUnderTheLock(t *testing.T) {
	f := New(t, Options{
		File:   "plugins:\n  grafana:\n    url: https://grafana.example.com\n",
		Env:    []string{"DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com"},
		Stored: map[string]any{"plugins.proxmox.node": "pve"},
	})
	res := f.Resolved()
	for path, want := range map[string]config.Source{
		"plugins.grafana.url":  config.SourceFile,
		"plugins.headlamp.url": config.SourceEnv,
		"plugins.proxmox.node": config.SourceUI,
		"plugins.gatus.url":    config.SourceDefault,
	} {
		if got := res.Provenance[path].Source; got != want {
			t.Errorf("%s: source %q, want %q", path, got, want)
		}
	}
	if s := f.Store(); s.Readonly || s.Reason != "" || !s.Writable || s.SecretKey {
		t.Errorf("store = %+v, want writable without a key", s)
	}

	rev := f.Revision()
	err := f.WithLock(func() error {
		if _, err := f.Apply(map[string]any{"plugins.proxmox.node": "pve2"}, nil, nil); err != nil {
			return err
		}
		return f.Reload()
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Resolved().Tree.Plugins.Proxmox.Node; got != "pve2" || f.Revision() != rev+1 {
		t.Errorf("after the write: node %q, revision %d (was %d)", got, f.Revision(), rev)
	}
	if u := f.Unlocked(); len(u) != 0 {
		t.Errorf("unlocked calls %v, want none", u)
	}
	if err := f.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Apply(nil, []string{"plugins.proxmox.node"}, nil); err != nil {
		t.Fatal(err)
	}
	if u := f.Unlocked(); !slices.Equal(u, []string{"Reload", "Apply"}) {
		t.Errorf("unlocked calls %v, want [Reload Apply]", u)
	}
}

// BeforeReload makes Reload fail after the store changed, as a test of the rollback needs.
func TestFakeBeforeReloadFailsTheReload(t *testing.T) {
	f := New(t, Options{})
	boom := errors.New("boom")
	f.BeforeReload = func() error { return boom }
	if err := f.WithLock(f.Reload); !errors.Is(err, boom) {
		t.Fatalf("Reload = %v, want the hook's error", err)
	}
}

// The store's reason follows what made it read-only, and a key makes secrets writable.
func TestFakeStoreDescription(t *testing.T) {
	for name, tc := range map[string]struct {
		o    Options
		want model.SettingsStore
	}{
		"settings.ui readonly": {Options{ReadOnly: true, SecretKey: true}, model.SettingsStore{Readonly: true, Reason: model.SettingsStoreReasonConfig, Writable: true, SecretKey: true}},
		"no data dir":          {Options{NoDataDir: true}, model.SettingsStore{Readonly: true, Reason: model.SettingsStoreReasonDataDir}},
	} {
		t.Run(name, func(t *testing.T) {
			f := New(t, tc.o)
			got := f.Store()
			tc.want.DataDir = f.Dir
			if got != tc.want {
				t.Errorf("store = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A stored secret written with Encrypt opens with the fake's key, and ReplaceSecret goes through
// the real secret writer, which audits the field path.
func TestFakeSecrets(t *testing.T) {
	const path = "plugins.proxmox.tokenSecret"
	a, err := audit.Open("", audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := New(t, Options{SecretKey: true, Audit: a, Stored: map[string]any{
		"plugins.proxmox.url": "https://pve.example.com:8006", "plugins.proxmox.tokenID": "u@pve!t",
		path: Encrypt(t, path, "first-secret-0123456789"),
	}})
	if got := f.Resolved().Tree.Plugins.Proxmox.TokenSecret.Reveal(); got != "first-secret-0123456789" {
		t.Fatalf("stored secret = %q", got)
	}
	err = f.WithLock(func() error {
		if err := f.ReplaceSecret(path, "second-secret-9876543210", audit.Actor{User: "alice"}); err != nil {
			return err
		}
		return f.Reload()
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Resolved().Tree.Plugins.Proxmox.TokenSecret.Reveal(); got != "second-secret-9876543210" {
		t.Errorf("replaced secret = %q", got)
	}
	recs, _, _ := a.Recent(10, "", audit.RingMain)
	if len(recs) != 2 || recs[0].Action != config.ActionSecretReplace || !slices.Equal(recs[0].Fields, []string{path}) {
		t.Errorf("audit = %+v", recs)
	}
}

// PluginConfig is the section's configuration as the plugin receives it, and Check reaches the
// checker only for a testable id.
func TestFakePluginConfigAndChecks(t *testing.T) {
	var got any
	f := New(t, Options{
		Stored: map[string]any{"plugins.grafana.url": "https://grafana.example.com"},
		Checker: Checks{"grafana": func(_ context.Context, cfg any, fromUI bool) (plugin.CheckReport, error) {
			got = cfg
			if !fromUI {
				t.Error("a URL from the store must be checked as from the UI")
			}
			return plugin.CheckReport{Checks: []plugin.CheckResult{{Name: "read", OK: true}}}, nil
		}},
	})
	cfg, fromUI, ok := f.PluginConfig(f.Resolved(), "grafana")
	if g, isGrafana := cfg.(*pluginconf.Grafana); !ok || !fromUI || !isGrafana || g.URL != "https://grafana.example.com" {
		t.Fatalf("PluginConfig = %#v, %v, %v", cfg, fromUI, ok)
	}
	if _, _, ok := f.PluginConfig(f.Resolved(), "nope"); ok {
		t.Error("an unknown id has no configuration")
	}
	if !f.Testable("grafana") || f.Testable("gatus") {
		t.Error("only grafana has a check")
	}
	if rep, err := f.Check(context.Background(), "grafana", cfg, fromUI); err != nil || len(rep.Checks) != 1 || got != cfg {
		t.Errorf("Check = %+v, %v", rep, err)
	}
	if _, err := f.Check(context.Background(), "gatus", nil, false); !errors.Is(err, plugin.ErrNotCheckable) {
		t.Errorf("Check of an untestable id = %v", err)
	}
}
