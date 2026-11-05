// Package settingstest is the fake api.SettingsBackend the settings routes are tested with: the
// real settings store in a temporary data directory and a real config.Reloader over a config
// file and an environment, wired the way cmd/dash wires them, plus hooks a test sets before its
// first request. It does not import package api, so the API's own tests can use it.
package settingstest

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// PublicURL is the server.publicURL of every fake.
const PublicURL = "https://dash.example.com"

// SecretKey is the DASH_SECRET_KEY of a fake with Options.SecretKey: 32 bytes, base64.
var SecretKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))

// Options describe the configuration a fake starts from.
type Options struct {
	// File is the content of config.yaml in the data directory; "" writes no file.
	File string
	// Env is the environment layer, KEY=VALUE, after the fake's own: DASH_DATA_DIR,
	// DASH_SERVER_PUBLIC_URL, DASH_AUTH_MODE=proxy and, with SecretKey, DASH_SECRET_KEY. A
	// later entry wins.
	Env []string
	// Stored is written to the store before the configuration is resolved: dotted path → value
	// (an enc:v1: value needs SecretKey and the path as its associated data; use Encrypt).
	Stored map[string]any
	// ReadOnly sets settings.ui to readonly (the GitOps store); the store's reason is config.
	ReadOnly bool
	// NoDataDir points DASH_DATA_DIR at a directory that does not exist: the store is
	// read-only with reason data_dir and nothing is stored.
	NoDataDir bool
	// SecretKey configures DASH_SECRET_KEY (SecretKey above), so secrets can be saved.
	SecretKey bool
	// Live says which sections apply without a restart (config.ReloadOptions.Live). Nil: auth
	// and every plugins.* section, as the supervisor answers for the shipped plugins.
	Live func(section string) bool
	// Checker stands in for the supervisor's connection checks: nil tests nothing. Pass a real
	// *plugin.Supervisor, or Checks.
	Checker Checker
	// Namespace is what Namespace answers ("" outside a cluster).
	Namespace string
	// Audit records the secret writes; nil is a log without a file (its in-memory rings).
	// Pass the same log as api.Deps.Audit.
	Audit *audit.Log
	// Registry learns every secret; nil is a new one. Pass the same one as api.Deps.Secrets.
	Registry *secret.Registry
	// Log receives the resolver's and the reloader's messages; nil discards them.
	Log *slog.Logger
}

// Checker is the slice of plugin.Supervisor the fake's Testable and Check call.
type Checker interface {
	Testable(id plugin.ID) bool
	Check(ctx context.Context, id plugin.ID, candidate any, fromUI bool) (plugin.CheckReport, error)
}

// Checks is a Checker of one function per testable plugin id; any other id is not testable.
type Checks map[plugin.ID]func(ctx context.Context, cfg any, fromUI bool) (plugin.CheckReport, error)

// Testable implements Checker.
func (c Checks) Testable(id plugin.ID) bool { return c[id] != nil }

// Check implements Checker: plugin.ErrNotCheckable for an id without a function.
func (c Checks) Check(ctx context.Context, id plugin.ID, cfg any, fromUI bool) (plugin.CheckReport, error) {
	fn := c[id]
	if fn == nil {
		return plugin.CheckReport{}, plugin.ErrNotCheckable
	}
	return fn(ctx, cfg, fromUI)
}

// Fake is an api.SettingsBackend. Its fields are the real parts, for a test to inspect or to
// change behind the API's back; the hooks are set before the first request.
type Fake struct {
	Dir        string // the data directory
	ConfigPath string // config.yaml in Dir: WriteConfig replaces it, the next Reload reads it
	St         *settings.Store
	Rel        *config.Reloader
	Writer     config.SecretWriter
	Registry   *secret.Registry
	Audit      *audit.Log
	Refs       *secret.K8sRefs

	// BeforeReload, when set, runs inside Reload before the real reload; an error is Reload's
	// answer and nothing reloads. A test makes Reload fail after a candidate passed by
	// returning an error, or by writing a config file the reload refuses (WriteConfig).
	BeforeReload func() error

	opts     Options
	info     model.SettingsStore
	locked   bool // some WithLock's fn is running; guarded by mu
	mu       sync.Mutex
	unlocked []string
}

// New builds a fake from o, closed when the test ends. It fails the test if the configuration
// does not resolve.
func New(t testing.TB, o Options) *Fake {
	t.Helper()
	root := t.TempDir()
	f := &Fake{Dir: root, opts: o, Registry: o.Registry, Audit: o.Audit}
	if o.NoDataDir {
		f.Dir = filepath.Join(root, "absent")
	}
	f.ConfigPath = filepath.Join(f.Dir, "config.yaml")
	if f.Registry == nil {
		f.Registry = secret.NewRegistry()
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if f.Audit == nil {
		a, err := audit.Open("", audit.Options{Logger: log})
		if err != nil {
			t.Fatal(err)
		}
		f.Audit = a
	}
	env := []string{"DASH_DATA_DIR=" + f.Dir, "DASH_SERVER_PUBLIC_URL=" + PublicURL, "DASH_AUTH_MODE=proxy"}
	if o.SecretKey {
		env = append(env, "DASH_SECRET_KEY="+SecretKey)
	}
	if o.ReadOnly {
		env = append(env, "DASH_SETTINGS_UI=readonly")
	}
	env = append(env, o.Env...)

	if o.File != "" && !o.NoDataDir {
		f.WriteConfig(t, o.File)
	}
	if len(o.Stored) > 0 && !o.NoDataDir {
		// A read-only store cannot be written: fill it first, as an operator would have.
		st, err := settings.Open(f.Dir, settings.Options{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.Apply(o.Stored, nil, nil)
		if cerr := st.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	f.Refs = secret.NewK8sRefs(f.Registry)
	src := config.Sources{Environ: env, Registry: f.Registry, K8s: f.Refs, Log: log}
	if b, err := os.ReadFile(f.ConfigPath); err == nil {
		src.File = &config.File{Path: f.ConfigPath, Data: b}
	} else if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	var err error
	if src.Keys, err = config.SecretKeys(env, f.Registry); err != nil {
		t.Fatal(err)
	}
	boot, err := config.BootstrapOf(src)
	if err != nil {
		t.Fatal(err)
	}
	readonly := boot.SettingsUI == config.SettingsReadOnly
	if f.St, err = settings.Open(boot.DataDir, settings.Options{ReadOnly: readonly}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.St.Close() })
	src.UI, src.UIRef = f.St.Values(), f.St.Path()
	res, err := config.Resolve(src)
	if err != nil {
		t.Fatal(err)
	}
	live := o.Live
	if live == nil {
		live = func(section string) bool { return section == "auth" || strings.HasPrefix(section, "plugins.") }
	}
	f.Rel = config.NewReloader(res, f.St, config.ReloadOptions{Log: log, Live: live})
	f.Writer = config.SecretWriter{Store: f.St, Keys: res.Keys, Audit: f.Audit, Registry: f.Registry}

	f.info = model.SettingsStore{
		Readonly: f.St.ReadOnly(), DataDir: boot.DataDir, Writable: !o.NoDataDir, SecretKey: res.Keys != nil,
	}
	switch {
	case !f.info.Readonly:
	case readonly:
		f.info.Reason = model.SettingsStoreReasonConfig
	default:
		f.info.Reason = model.SettingsStoreReasonDataDir
	}
	return f
}

// WriteConfig replaces config.yaml with content, atomically (the next Reload reads it).
func (f *Fake) WriteConfig(t testing.TB, content string) {
	t.Helper()
	tmp := f.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, f.ConfigPath); err != nil {
		t.Fatal(err)
	}
}

// Encrypt seals plaintext for path with SecretKey, as a value for Options.Stored.
func Encrypt(t testing.TB, path, plaintext string) string {
	t.Helper()
	keys, err := config.SecretKeys([]string{"DASH_SECRET_KEY=" + SecretKey}, nil)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := keys.Encrypt(path, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

// Unlocked lists, in call order, the methods that must run inside WithLock (Candidate, Reload,
// Apply, Revert, ReplaceSecret, ClearSecret) and were called outside it. Empty is the rule.
func (f *Fake) Unlocked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.unlocked)
}

// mustHoldLock records name when it is called while no WithLock's fn runs. A call from another
// goroutine while one does is not caught: the check is for the handlers' sequential code.
func (f *Fake) mustHoldLock(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.locked {
		f.unlocked = append(f.unlocked, name)
	}
}

// Resolved implements api.SettingsBackend.
func (f *Fake) Resolved() *config.Resolved { return f.Rel.Resolved() }

// RestartRequired implements api.SettingsBackend.
func (f *Fake) RestartRequired() []string { return f.Rel.RestartRequired() }

// WithLock implements api.SettingsBackend.
func (f *Fake) WithLock(fn func() error) error {
	return f.Rel.WithLock(func() error {
		f.setLocked(true)
		defer f.setLocked(false)
		return fn()
	})
}

func (f *Fake) setLocked(v bool) {
	f.mu.Lock()
	f.locked = v
	f.mu.Unlock()
}

// Candidate implements api.SettingsBackend.
func (f *Fake) Candidate(ui map[string]any) (*config.Resolved, error) {
	f.mustHoldLock("Candidate")
	return f.Rel.Candidate(ui)
}

// Reload implements api.SettingsBackend, after BeforeReload.
func (f *Fake) Reload() error {
	f.mustHoldLock("Reload")
	if f.BeforeReload != nil {
		if err := f.BeforeReload(); err != nil {
			return err
		}
	}
	return f.Rel.Reload()
}

// Store implements api.SettingsBackend.
func (f *Fake) Store() model.SettingsStore { return f.info }

// Revision implements api.SettingsBackend.
func (f *Fake) Revision() uint64 { return f.St.Revision() }

// Stored implements api.SettingsBackend.
func (f *Fake) Stored() map[string]any { return f.St.Values() }

// Apply implements api.SettingsBackend.
func (f *Fake) Apply(sets map[string]any, deletes, scrub []string) (settings.Snapshot, error) {
	f.mustHoldLock("Apply")
	return f.St.Apply(sets, deletes, scrub)
}

// Revert implements api.SettingsBackend.
func (f *Fake) Revert(prev settings.Snapshot) error {
	f.mustHoldLock("Revert")
	return f.St.Revert(prev)
}

// ReplaceSecret implements api.SettingsBackend.
func (f *Fake) ReplaceSecret(path, plaintext string, actor audit.Actor) error {
	f.mustHoldLock("ReplaceSecret")
	return f.Writer.Replace(path, plaintext, actor)
}

// ClearSecret implements api.SettingsBackend.
func (f *Fake) ClearSecret(path string, actor audit.Actor) error {
	f.mustHoldLock("ClearSecret")
	return f.Writer.Clear(path, actor)
}

// PluginConfig implements api.SettingsBackend as cmd/dash does: the section's value, which is
// the pointer of the plugin's configuration type.
func (f *Fake) PluginConfig(res *config.Resolved, id string) (any, bool, bool) {
	for _, sec := range res.Tree.PluginSections() {
		if sec.Name == id {
			return sec.Value, res.FromUI("plugins." + id), true
		}
	}
	return nil, false, false
}

// Testable implements api.SettingsBackend.
func (f *Fake) Testable(id string) bool {
	return f.opts.Checker != nil && f.opts.Checker.Testable(plugin.ID(id))
}

// Check implements api.SettingsBackend.
func (f *Fake) Check(ctx context.Context, id string, cfg any, fromUI bool) (plugin.CheckReport, error) {
	if f.opts.Checker == nil {
		return plugin.CheckReport{}, plugin.ErrNotCheckable
	}
	return f.opts.Checker.Check(ctx, plugin.ID(id), cfg, fromUI)
}

// Namespace implements api.SettingsBackend.
func (f *Fake) Namespace() string { return f.opts.Namespace }
