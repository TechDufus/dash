package main

import (
	"log/slog"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// loadConfig resolves the configuration layers (design B.2): defaults, the UI settings store,
// the config file, the environment. The store is located by the data directory and settings.ui,
// which only the file and the environment can set, so it is opened between two resolutions: the
// first finds the store, the second adds its values as the UI layer. Demo mode has no data
// directory and opens no store (the returned store is nil). The caller closes the store.
func loadConfig(reg *secret.Registry) (*config.Resolved, *settings.Store, error) {
	src, err := config.ProcessSources(reg)
	if err != nil {
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
	if err == nil {
		err = res.LoadLauncherFile()
	}
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, nil, err
	}
	return res, store, nil
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
