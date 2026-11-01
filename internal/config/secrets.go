package config

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// ErrNotUISecret is returned for a path that is not a secret field the UI may set.
var ErrNotUISecret = errors.New("not a secret field that can be set from the UI")

// Audit actions of the secret write path. The record names the field path, never a value.
const (
	ActionSecretReplace = "settings.secret.replace"
	ActionSecretClear   = "settings.secret.clear"
	ActionSecretRekey   = "secrets.rekey"
)

// SecretWriter stores the secrets an admin types into the UI (design C.4): encrypted with the
// current key into the settings store, with the field path as the associated data. The
// settings API (P4.7) calls it; it maps secret.ErrNoKey (no DASH_SECRET_KEY) and
// settings.ErrReadOnly to 409.
type SecretWriter struct {
	Store *settings.Store
	Keys  *secret.Keyring
	Audit *audit.Log
	// Registry learns each new plaintext so it is scrubbed from logs and API output at once.
	Registry *secret.Registry
}

// uiSecretLeaf checks that path is a secret leaf of the tree whose value may come from the UI.
func uiSecretLeaf(path string) error {
	if l := schema.byPath[path]; l == nil || !l.secret || uiIneligible(path) {
		return fmt.Errorf("%s: %w", path, ErrNotUISecret)
	}
	return nil
}

// Replace encrypts plaintext for path and stores it. Nothing is written when the path is not a
// UI secret field, the store is read-only, there is no key (the error names DASH_SECRET_KEY) or
// the audit intent cannot be recorded. Every retained backup of the store is rewritten without
// the old ciphertext, so a restore cannot bring the old secret back (design K.24).
func (w SecretWriter) Replace(path, plaintext string, actor audit.Actor) error {
	if err := uiSecretLeaf(path); err != nil {
		return err
	}
	if plaintext == "" {
		return fmt.Errorf("%s: a secret cannot be empty; clear it instead", path)
	}
	if w.Store.ReadOnly() {
		return settings.ErrReadOnly
	}
	sealed, err := w.Keys.Encrypt(path, plaintext)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	pending, err := w.Audit.Intent(audit.Record{Action: ActionSecretReplace, Fields: []string{path}, Source: "ui", Actor: actor})
	if err != nil {
		return err
	}
	if w.Registry != nil {
		w.Registry.AddString(plaintext)
	}
	err = w.Store.SetScrubbed(path, sealed)
	pending.Outcome(err)
	return err
}

// Clear removes the stored secret of path, and its old ciphertext from every retained backup.
// It needs no key.
func (w SecretWriter) Clear(path string, actor audit.Actor) error {
	if err := uiSecretLeaf(path); err != nil {
		return err
	}
	if w.Store.ReadOnly() {
		return settings.ErrReadOnly
	}
	pending, err := w.Audit.Intent(audit.Record{Action: ActionSecretClear, Fields: []string{path}, Source: "ui", Actor: actor})
	if err != nil {
		return err
	}
	err = w.Store.DeleteScrubbed(path)
	pending.Outcome(err)
	return err
}

// RekeyStore re-encrypts, with the current key, every stored secret that the previous key
// sealed (design C.4: DASH_SECRET_KEY_PREVIOUS). It runs on startup and is a no-op when no
// value needs it. The change is audited as secrets.rekey, intent before and outcome after; if
// the intent cannot be recorded nothing is rewritten, which is safe because the previous key
// still opens every value. A read-only store cannot be rewritten: one warning says the rekey
// is pending, and dash runs with both keys until the store is writable.
func RekeyStore(store *settings.Store, keys *secret.Keyring, alog *audit.Log, log *slog.Logger) {
	if store == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	var paths []string
	sealed := map[string]string{}
	for _, l := range schema.leaves {
		if !l.secret || uiIneligible(l.path) {
			continue
		}
		v, _ := store.Get(l.path)
		if s, ok := v.(string); ok && keys.NeedsRekey(s) {
			paths, sealed[l.path] = append(paths, l.path), s
		}
	}
	if len(paths) == 0 {
		return
	}
	slices.Sort(paths)
	if store.ReadOnly() {
		log.Warn("secret rekey is pending: the settings store is read-only, so values stay encrypted with the previous key", "fields", len(paths))
		return
	}
	pending, err := alog.Intent(audit.Record{
		Action: ActionSecretRekey, Fields: paths, Source: "startup",
		Actor: audit.Actor{Type: "system", User: "dash"},
	})
	if err != nil {
		log.Error("secret rekey skipped: the audit log cannot record it; values still decrypt with the previous key", "error", err.Error())
		return
	}
	var errs []error
	for _, p := range paths {
		plain, err := keys.Decrypt(p, sealed[p])
		if err == nil {
			var fresh string
			if fresh, err = keys.Encrypt(p, plain); err == nil {
				err = store.SetScrubbed(p, fresh)
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	err = errors.Join(errs...)
	pending.Outcome(err)
	if err != nil {
		log.Error("secret rekey failed for some fields; they still decrypt with the previous key", "error", err.Error())
		return
	}
	log.Info("secrets re-encrypted with the current key", "fields", len(paths))
}
