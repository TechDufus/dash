package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// loadConfig resolves the configuration layers (design B.2): defaults, the UI settings store,
// the config file, the environment. The store is located by the data directory and settings.ui,
// which only the file and the environment can set, so it is opened between two resolutions: the
// first finds the store, the second adds its values as the UI layer. Demo mode has no data
// directory and opens no store (the returned store is nil). The caller closes the store.
//
// refs hands out the holders of the k8s: references of every secret field (the reloader keeps it
// in its sources, so a reload sees the same holders); nil makes a k8s: reference an error.
func loadConfig(reg *secret.Registry, refs *secret.K8sRefs) (*config.Resolved, *settings.Store, error) {
	src, err := config.ProcessSources(reg)
	if err != nil {
		return nil, nil, err
	}
	src.K8s = refs
	if src.Keys, err = config.SecretKeys(src.Environ, reg); err != nil {
		return nil, nil, err
	}
	boot, err := config.BootstrapOf(src)
	if err != nil {
		return nil, nil, err
	}
	var store *settings.Store
	if !boot.Demo {
		if store, err = openSettings(boot); err != nil {
			return nil, nil, err
		}
		src.UI, src.UIRef = store.Values(), store.Path()
	}
	res, err := config.Resolve(src)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, nil, err
	}
	return res, store, nil
}

// openState is everything run needs before it serves: the resolved configuration, its settings
// store (nil in demo mode), and the audit log, with a pending key rotation carried out and
// audited. Demo has nothing to audit and no data dir: its actions go to slog only. Elsewhere a
// missing or unwritable data dir also means slog only (audit.Open logs it at INFO). In a
// writable dir a log that cannot be opened or continued, or that another dash instance holds,
// is an error: dash refuses to start rather than run without the record the 503 rule relies
// on. The file has no redacting handler in front of it, so error text is scrubbed here. The
// caller closes the log, then the store.
func openState(reg *secret.Registry, refs *secret.K8sRefs) (*config.Resolved, *settings.Store, *audit.Log, error) {
	res, store, err := loadConfig(reg, refs)
	if err != nil {
		return nil, nil, nil, err
	}
	auditDir := res.Config.DataDir
	if res.Config.Demo {
		auditDir = ""
	}
	alog, err := audit.Open(auditDir, audit.Options{Scrub: reg.ScrubLogLine})
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, nil, nil, err
	}
	config.RekeyStore(store, res.Keys, alog, nil)
	return res, store, alog, nil
}

// openSettings opens the settings store of the data directory. Another dash holding the
// directory (settings.ErrLocked) is fatal: two writers would lose each other's saves. A store
// that cannot be written is not: dash runs on file and environment alone, and says so once.
func openSettings(boot config.Bootstrap) (*settings.Store, error) {
	readonly := boot.SettingsUI == config.SettingsReadOnly
	store, err := settings.Open(boot.DataDir, settings.Options{ReadOnly: readonly})
	if err != nil {
		return nil, err
	}
	if store.ReadOnly() {
		reason := "the data directory " + boot.DataDir + " is missing or not writable"
		if readonly {
			reason = "settings.ui is readonly"
		}
		slog.Info("settings are read-only: nothing can be saved from the UI", "reason", reason)
	}
	return store, nil
}

// settingsBackend is the api.SettingsBackend of a running dash: the reloader, the settings store,
// the secret writer over both, and the supervisor's connection checks (design D14, D15).
type settingsBackend struct {
	rel       *config.Reloader
	store     *settings.Store
	writer    config.SecretWriter
	sup       *plugin.Supervisor
	info      model.SettingsStore
	namespace string
}

var _ api.SettingsBackend = (*settingsBackend)(nil)

// newSettingsBackend joins the parts run builds. res is the startup resolution: the keyring
// and the data directory do not change while dash runs. The data directory counts as writable
// when the audit log file is open there. store must not be nil (demo mode has no settings API).
func newSettingsBackend(res *config.Resolved, store *settings.Store, rel *config.Reloader, sup *plugin.Supervisor,
	alog *audit.Log, reg *secret.Registry) *settingsBackend {
	info := model.SettingsStore{
		Readonly: store.ReadOnly(), DataDir: res.Config.DataDir, Writable: alog.Enabled(), SecretKey: res.Keys != nil,
	}
	switch {
	case !info.Readonly:
	case res.Config.SettingsUI == config.SettingsReadOnly:
		info.Reason = model.SettingsStoreReasonConfig
	default:
		info.Reason = model.SettingsStoreReasonDataDir
	}
	return &settingsBackend{
		rel: rel, store: store, sup: sup, info: info, namespace: ownNamespace(os.Getenv, os.ReadFile),
		writer: config.SecretWriter{Store: store, Keys: res.Keys, Audit: alog, Registry: reg},
	}
}

// serviceAccountNamespace is where Kubernetes mounts the pod's namespace.
const serviceAccountNamespace = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// ownNamespace is the namespace dash runs in: POD_NAMESPACE, else the service account's
// namespace file, else "" (not in a cluster).
func ownNamespace(getenv func(string) string, readFile func(string) ([]byte, error)) string {
	if ns := strings.TrimSpace(getenv("POD_NAMESPACE")); ns != "" {
		return ns
	}
	if b, err := readFile(serviceAccountNamespace); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func (b *settingsBackend) Resolved() *config.Resolved     { return b.rel.Resolved() }
func (b *settingsBackend) RestartRequired() []string      { return b.rel.RestartRequired() }
func (b *settingsBackend) WithLock(fn func() error) error { return b.rel.WithLock(fn) }
func (b *settingsBackend) Candidate(ui map[string]any) (*config.Resolved, error) {
	return b.rel.Candidate(ui)
}
func (b *settingsBackend) Reload() error                       { return b.rel.Reload() }
func (b *settingsBackend) Store() model.SettingsStore          { return b.info }
func (b *settingsBackend) Revision() uint64                    { return b.store.Revision() }
func (b *settingsBackend) Stored() map[string]any              { return b.store.Values() }
func (b *settingsBackend) Revert(prev settings.Snapshot) error { return b.store.Revert(prev) }
func (b *settingsBackend) Namespace() string                   { return b.namespace }

func (b *settingsBackend) Apply(sets map[string]any, deletes, scrub []string) (settings.Snapshot, error) {
	return b.store.Apply(sets, deletes, scrub)
}

func (b *settingsBackend) ReplaceSecret(path, plaintext string, actor audit.Actor) error {
	return b.writer.Replace(path, plaintext, actor)
}

func (b *settingsBackend) ClearSecret(path string, actor audit.Actor) error {
	return b.writer.Clear(path, actor)
}

// PluginConfig is the configuration the supervisor would give plugin id under res: what
// instanceConfig makes of its section, and the provenance of its URLs: the one conversion,
// shared with resolvedSettings.Instances, so a check tests what the plugin would run with.
func (b *settingsBackend) PluginConfig(res *config.Resolved, id string) (any, bool, bool) {
	for _, sec := range res.Tree.PluginSections() {
		if sec.Name == id {
			return instanceConfig(sec).Config, res.FromUI("plugins." + sec.Name), true
		}
	}
	return nil, false, false
}

func (b *settingsBackend) Testable(id string) bool { return b.sup.Testable(plugin.ID(id)) }

func (b *settingsBackend) Check(ctx context.Context, id string, cfg any, fromUI bool) (plugin.CheckReport, error) {
	return b.sup.Check(ctx, plugin.ID(id), cfg, fromUI)
}
