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

// ActionSecretRekey is the audit action of a startup rekey. The record names the field paths,
// never a value.
const ActionSecretRekey = "secrets.rekey"

// OperatorK8sRef reports whether a secret field that the file or the environment sets is a k8s:
// reference to the same Secret key as ref (namespace, name and key; the spelling is not
// compared). The settings API refuses such a reference from the UI.
func OperatorK8sRef(res *Resolved, ref secret.K8sRef) bool {
	for _, l := range schema.leaves {
		if !l.secret {
			continue
		}
		if src := res.Provenance[l.path].Source; src != SourceEnv && src != SourceFile {
			continue
		}
		r, err := secret.ParseK8sRef(l.field(&res.Tree).Interface().(secret.Value).Ref())
		if err == nil && r.Namespace == ref.Namespace && r.Name == ref.Name && r.Key == ref.Key {
			return true
		}
	}
	return false
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
