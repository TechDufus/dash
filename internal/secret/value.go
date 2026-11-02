// Package secret keeps credentials out of logs, JSON and API errors.
//
// A Value holds one secret and prints as "[redacted]" through fmt, slog and
// encoding/json. A Registry knows every resolved secret and scrubs them (and
// their base64 and URL-encoded forms) from free text. The crypto half
// (Keyring) seals secrets that an operator types into the UI so they can sit
// in ui-settings.yaml.
package secret

import (
	"encoding/json"
	"log/slog"
	"sync"
)

const redacted = "[redacted]"

// maxRetired bounds how many rotated-out values a holder remembers. They stay
// scrubbable because an error built from the old credential can still be in
// flight when a rotation lands.
const maxRetired = 4

// holder is the shared state behind every copy of a Value. Copies of a Value
// point at one holder, so an Update is seen by all of them. Keeping the secret
// behind a pointer also stops fmt from printing it when a Value sits in an
// unexported struct field (fmt prints a nested pointer as an address).
type holder struct {
	ref string // immutable after New

	mu      sync.RWMutex
	cur     string
	retired []string
	version uint64 // bumps on every change; Registry uses it to rebuild lazily
	reason  string // why a reference has not been read (K8sRefs); not part of the secret
}

// Value is one secret. The zero Value is unset. Copy it freely: copies share
// the underlying secret, so a rotation through Update reaches every holder.
type Value struct {
	h *holder
}

// New wraps a secret. ref records where it came from ("env:NAME",
// "file:/path") or is empty for a literal; it is shown in metadata, never the
// value.
func New(value, ref string) Value {
	return Value{h: &holder{ref: ref, cur: value}}
}

// Reveal returns the secret. Call it only where the credential is put on the
// wire (a header, a form field) and never log or store the result.
func (v Value) Reveal() string {
	if v.h == nil {
		return ""
	}
	v.h.mu.RLock()
	defer v.h.mu.RUnlock()
	return v.h.cur
}

// IsSet reports whether the Value holds a non-empty secret.
func (v Value) IsSet() bool { return v.Reveal() != "" }

// Pending reports whether the Value is a reference that has not been read yet: it has a ref and
// no secret. A k8s: reference is pending until the kubernetes plugin has read the Secret.
func (v Value) Pending() bool { return v.h != nil && v.h.ref != "" && !v.IsSet() }

// Present reports whether the Value holds a secret or a reference to one that is on its way.
// Validation uses it: a pending handle is configured as far as the load can tell, while
// Configured (a plugin's own decision) keeps asking IsSet.
func (v Value) Present() bool { return v.IsSet() || v.Pending() }

// Reason is the category a pending reference gives for not being read yet, "" when there is
// none. It is a category, never a name or a value, and is for logs.
func (v Value) Reason() string {
	if v.h == nil {
		return ""
	}
	v.h.mu.RLock()
	defer v.h.mu.RUnlock()
	return v.h.reason
}

// setReason records the reason and reports whether it changed.
func (v Value) setReason(reason string) bool {
	if v.h == nil {
		return false
	}
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	if v.h.reason == reason {
		return false
	}
	v.h.reason = reason
	return true
}

// Ref returns the reference the secret was resolved from, or "" for a literal
// or an unset Value.
func (v Value) Ref() string {
	if v.h == nil {
		return ""
	}
	return v.h.ref
}

// Update replaces the secret for every copy of v, for file rotation. The old
// value is remembered for scrubbing. It reports false on the zero Value, which
// shares nothing and so cannot be updated.
func (v Value) Update(value string) bool {
	if v.h == nil {
		return false
	}
	v.h.mu.Lock()
	defer v.h.mu.Unlock()
	if value == v.h.cur {
		return true
	}
	if v.h.cur != "" {
		v.h.retired = append(v.h.retired, v.h.cur)
		if len(v.h.retired) > maxRetired {
			v.h.retired = v.h.retired[len(v.h.retired)-maxRetired:]
		}
	}
	v.h.cur = value
	v.h.version++
	return true
}

// snapshot returns every value that must be scrubbed (current first) and the
// change counter they were read at.
func (h *holder) snapshot() (values []string, version uint64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	values = make([]string, 0, 1+len(h.retired))
	values = append(values, h.cur)
	values = append(values, h.retired...)
	return values, h.version
}

func (h *holder) currentVersion() uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.version
}

// String and GoString make %v, %+v, %s and %#v safe.
func (Value) String() string   { return redacted }
func (Value) GoString() string { return redacted }

// LogValue keeps a Value out of slog even without the redacting handler.
func (Value) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON emits metadata only, which is what the settings API shows (design C.1).
func (v Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Set bool   `json:"set"`
		Ref string `json:"ref,omitempty"`
	}{v.IsSet(), v.Ref()})
}
