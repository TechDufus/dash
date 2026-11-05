package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/secret"
)

// Source is the layer an effective value came from (design B.2).
type Source string

const (
	SourceDefault Source = "default"
	SourceUI      Source = "ui"
	SourceFile    Source = "file"
	SourceEnv     Source = "env"
)

// Provenance says where one effective value came from. Ref is the environment variable, the
// config file path or the UI store file; it is empty for defaults.
type Provenance struct {
	Source Source `json:"source"`
	Ref    string `json:"ref,omitempty"`
}

// File is the config file layer. Path is only used in messages and provenance.
type File struct {
	Path string
	Data []byte
}

// Sources are the layers of one resolution: defaults < UI < file < environment (design B.2).
type Sources struct {
	// Environ is the process environment in os.Environ form. DASH_DEV and DASH_DEMO select the
	// defaults; the rest is the environment layer.
	Environ []string
	// File is the config file, or nil when there is none.
	File *File
	// UI is the sparse, nested map of admin-saved values (settings.Store.Values), or nil. UIRef
	// is the store file, recorded as the provenance ref of the values taken from it.
	UI    map[string]any
	UIRef string
	// Registry learns every secret the resolution reads, so the log handler and the API can
	// scrub them. Nil is fine.
	Registry *secret.Registry
	// Keys opens the enc:v1: secrets of the UI layer (SecretKeys); nil means no key is
	// configured, which fails resolution if the store holds any.
	Keys *secret.Keyring
	// Log receives warnings; nil means slog.Default().
	Log *slog.Logger
	// SkipSecrets leaves the env: and file: references of secret fields unread: they count as
	// set, but the variable or file need not exist. `dash config validate` uses it to check a
	// file on a machine other than the one dash runs on.
	SkipSecrets bool
	// K8s hands out the holders of k8s: references (P3.10): the same one for the same reference
	// on every resolution, so a reload sees no change where only the reading progressed. Nil
	// means no kubernetes plugin to read them: a k8s: reference is then an error.
	// `dash config validate` passes a throwaway one, which checks the syntax only.
	K8s *secret.K8sRefs

	// strictUI makes the resolution a candidate's (Reloader.Candidate). A problem with a UI
	// value is an error naming its path when the value differs from the one strictUI (the
	// store's values) holds there, and stays a warning where they agree, so a stored key of
	// another version cannot refuse every later write. A k8s: reference that K8s has not
	// handed out is checked for syntax and gets no handle. Nil, the default: every UI problem
	// is a warning.
	strictUI map[string]any
}

// Resolved is the outcome of Resolve.
type Resolved struct {
	Tree Tree
	// Provenance is keyed by dotted path (e.g. "plugins.proxmox.url"). Lists are one entry.
	Provenance map[string]Provenance
	// Config is the flat view of Tree.
	Config Config
	// Keys is the keyring the resolution decrypted with, for sealing secrets saved from the UI
	// and re-encrypting them (RekeyStore).
	Keys *secret.Keyring

	src Sources // what produced this resolution, for the reloader
}

// FieldError is a problem with one configuration path.
type FieldError struct{ Path, Message string }

// Error is "path: message".
func (e *FieldError) Error() string { return e.Path + ": " + e.Message }

// FieldErrors returns, in order, every *FieldError in err's tree (errors.Join and %w chains);
// nil if there is none. A strict candidate (Reloader.Candidate) reports its per-path UI
// problems this way.
func FieldErrors(err error) []FieldError {
	var out []FieldError
	var walk func(error)
	walk = func(err error) {
		switch e := err.(type) {
		case nil:
		case *FieldError:
			out = append(out, *e)
		case interface{ Unwrap() []error }:
			for _, c := range e.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(e.Unwrap())
		}
	}
	walk(err)
	return out
}

// fieldError is an error whose text stays as it is ("path (env NAME): message") while
// FieldErrors and errors.As still find its path. It unwraps to the FieldError alone, not to a
// list: callers that flatten errors.Join trees (dash config validate) must see one problem.
type fieldError struct {
	error
	field *FieldError
}

func (e *fieldError) Unwrap() error { return e.field }

// Resolve layers the sources, validates the result and maps it onto the flat Config. An invalid
// file or environment is an error; stale or UI-ineligible UI values are ignored with a warning
// so that a stored setting can never keep dash from starting.
func Resolve(src Sources) (*Resolved, error) {
	r, dev, demo, err := layer(src)
	if err != nil {
		return nil, err
	}
	if err := r.resolveSecrets(src.Registry); err != nil {
		return nil, err
	}
	if err := validate(r.tree, dev, demo, r.prov); err != nil {
		return nil, err
	}
	if _, ignored := proxyKeysOutsideProxyMode(r.tree, r.prov, dev, demo); len(ignored) > 0 {
		r.log.Warn("auth.proxy settings are ignored: auth.mode is not proxy; set DASH_AUTH_MODE=proxy if dash runs behind an identity proxy",
			"mode", r.tree.Auth.Mode, "settings", ignored)
	}
	cfg := r.tree.config(dev, demo)
	cfg.DemoClock = r.demoClock
	return &Resolved{Tree: r.tree, Provenance: r.prov, Config: cfg, Keys: src.Keys, src: src}, nil
}

// Bootstrap is what the file and the environment alone say about the settings store. The store
// is opened before the UI layer can be read, and it is located by dataDir and settings.ui,
// which the UI may not set (S.14), so these two cannot depend on it.
type Bootstrap struct {
	DataDir    string
	SettingsUI string
	Demo       bool // demo mode has no data directory and opens no store
}

// BootstrapOf layers the file and the environment of src, ignoring src.UI. It reads no secret
// and validates nothing beyond what layering rejects, and it stays silent: Resolve reports
// the warnings once the UI layer is there.
func BootstrapOf(src Sources) (Bootstrap, error) {
	src.UI, src.Log = nil, slog.New(slog.DiscardHandler)
	r, _, demo, err := layer(src)
	if err != nil {
		return Bootstrap{}, err
	}
	return Bootstrap{DataDir: r.tree.DataDir, SettingsUI: r.tree.Settings.UI, Demo: demo}, nil
}

// HealthListenOf is server.healthListen as the file and the environment of src set it over the
// defaults, ignoring src.UI: the UI may not set server.* (S.14), so this is the address dash
// binds. Like BootstrapOf it reads no secret, validates nothing beyond what layering rejects and
// stays silent; `dash healthcheck` reads it.
func HealthListenOf(src Sources) (string, error) {
	src.UI, src.Log = nil, slog.New(slog.DiscardHandler)
	r, _, _, err := layer(src)
	if err != nil {
		return "", err
	}
	return r.tree.Server.HealthListen, nil
}

// layer applies defaults, the UI store, the file and the environment, in that order, to a tree
// whose secret leaves still hold their raw references.
func layer(src Sources) (r *resolver, dev, demo bool, err error) {
	log := src.Log
	if log == nil {
		log = slog.Default()
	}
	env := envMap(src.Environ)
	dev, demo = envTrue(env["DASH_DEV"]), envTrue(env["DASH_DEMO"])
	clock, err := demoClockOf(env, demo)
	if err != nil {
		return nil, false, false, err
	}

	r = &resolver{tree: defaults(dev, demo), prov: map[string]Provenance{}, raw: map[string]string{}, plain: map[string]string{}, keys: src.Keys, log: log, skipSecrets: src.SkipSecrets, k8s: src.K8s, strictUI: src.strictUI, demoClock: clock}
	for _, l := range schema.leaves {
		r.prov[l.path] = Provenance{Source: SourceDefault}
	}
	var uiErrs []error // only a strict resolution has any
	if src.UI != nil {
		uiErrs = r.applyMap(schema.root, "", src.UI, Provenance{Source: SourceUI, Ref: src.UIRef}, true)
	}
	if src.File != nil {
		if err := r.applyFile(src.File); err != nil {
			if len(uiErrs) > 0 {
				err = errors.Join(append(uiErrs, err)...)
			}
			return nil, false, false, err
		}
	}
	entries, envErr := envLayer(env, log)
	errs := append(uiErrs, envErr)
	for _, e := range entries {
		if err := r.set(schema.byPath[e.path], e.data, Provenance{Source: SourceEnv, Ref: e.ref}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.ref, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, false, false, err
	}
	r.tree.normalize()
	return r, dev, demo, nil
}

type resolver struct {
	tree  Tree
	prov  map[string]Provenance
	raw   map[string]string // secret leaves as the winning layer gave them: a literal or a reference
	plain map[string]string // secret leaves the UI layer gave encrypted, already decrypted; they win over raw
	keys  *secret.Keyring
	log   *slog.Logger
	// skipSecrets leaves env: and file: references unread (Sources.SkipSecrets).
	skipSecrets bool
	k8s         *secret.K8sRefs
	// strictUI turns UI problems into errors (Sources.strictUI).
	strictUI map[string]any
	// demoClock is DASH_DEMO_CLOCK (Config.DemoClock); zero when unset.
	demoClock time.Time

	// sealedErrs are the stored enc:v1: secrets that cannot be decrypted. They are collected,
	// not logged as ignored UI settings like other stale values: a stored credential is never
	// dropped, blanked or regenerated, so startup fails with all of them (design C.4).
	sealedErrs []error
}

// applyFile parses the YAML file strictly and applies it. Every problem is reported, each with
// its full path ("plugins.proxmox.urll: unknown field").
func (r *resolver) applyFile(f *File) error {
	var doc map[string]any
	if err := yaml.UnmarshalStrict(f.Data, &doc); err != nil {
		return fmt.Errorf("config file %s: %w", f.Path, err)
	}
	if errs := r.applyMap(schema.root, "", doc, Provenance{Source: SourceFile, Ref: f.Path}, false); len(errs) > 0 {
		// One problem per line, each naming the file, so `dash config validate` output stands alone.
		for i, err := range errs {
			errs[i] = fmt.Errorf("config file %s: %w", f.Path, err)
		}
		return errors.Join(errs...)
	}
	return nil
}

// applyMap walks a nested map against the schema. For the UI layer (ui) problems are warnings:
// that store is written by dash and may hold keys of another version. A strict resolution
// reports those of changed values as errors (Sources.strictUI).
func (r *resolver) applyMap(n *node, prefix string, m map[string]any, p Provenance, ui bool) []error {
	var errs []error
	problem := func(path string, v any, msg string) {
		switch {
		case !ui:
			errs = append(errs, fmt.Errorf("%s: %s", path, msg))
		case r.strictChanged(path, v):
			errs = append(errs, &FieldError{Path: path, Message: msg})
		default:
			r.log.Warn("ignoring UI setting", "path", path, "reason", msg)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		path, v := prefix+key, m[key]
		if ui && uiIneligible(path) {
			errs = append(errs, r.ineligible(path, v)...)
			continue
		}
		c := n.child(key)
		switch {
		case c == nil:
			problem(path, v, "unknown field")
		case v == nil:
			// "key:" with no value leaves the lower layers alone.
		case c.leaf != nil:
			data, err := json.Marshal(v)
			if err == nil {
				err = r.set(c.leaf, data, p)
			}
			if err != nil {
				problem(path, v, strings.TrimPrefix(err.Error(), c.leaf.path+": "))
			}
		default:
			sub, ok := v.(map[string]any)
			if !ok {
				problem(path, v, "expected a section (a map of settings)")
				continue
			}
			errs = append(errs, r.applyMap(c, path+".", sub, p, ui)...)
		}
	}
	return errs
}

// ineligible warns once per leaf under a UI value at a path the UI may not set. In a strict
// resolution a leaf whose value changed is an error instead. It descends into sections only: a
// map given for a leaf (a secret, say) is that leaf's value, and its keys never become part of a
// path.
func (r *resolver) ineligible(path string, v any) []error {
	if m, ok := v.(map[string]any); ok && schema.byPath[path] == nil {
		var errs []error
		for _, k := range slices.Sorted(maps.Keys(m)) {
			errs = append(errs, r.ineligible(path+"."+k, m[k])...)
		}
		return errs
	}
	reason := "server, auth, settings.ui and dataDir cannot be set from the UI"
	if path == "settings.secretNamespaces" || strings.HasPrefix(path, "settings.secretNamespaces.") {
		reason = "settings.secretNamespaces cannot be set from the UI"
	}
	if r.strictChanged(path, v) {
		return []error{&FieldError{Path: path, Message: reason}}
	}
	r.log.Warn("ignoring UI setting", "path", path, "reason", reason)
	return nil
}

// strictChanged reports whether a strict resolution refuses the UI value v at path: v differs
// from what the store holds there. Values compare by their JSON, so 1 and 1.0 are equal.
func (r *resolver) strictChanged(path string, v any) bool {
	if r.strictUI == nil {
		return false
	}
	var cur any = r.strictUI
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return true
		}
		if cur, ok = m[k]; !ok {
			return true
		}
	}
	was, errWas := json.Marshal(cur)
	now, errNow := json.Marshal(v)
	return errWas != nil || errNow != nil || !bytes.Equal(was, now)
}

// k8sHandle is the holder of a k8s: reference. A strict resolution must not change what runs,
// and a new handle is announced to the kubernetes plugin, which would watch a Secret nobody
// saved; so a reference not handed out yet is only parsed, into a pending Value of its own.
func (r *resolver) k8sHandle(raw string) (secret.Value, error) {
	if r.strictUI != nil && !slices.ContainsFunc(r.k8s.Refs(), func(k secret.K8sRef) bool { return k.Raw == raw }) {
		if _, err := secret.ParseK8sRef(raw); err != nil {
			return secret.Value{}, err
		}
		return secret.New("", raw), nil
	}
	return r.k8s.Handle(raw)
}

// set decodes JSON data into the leaf, strictly, and records its provenance.
func (r *resolver) set(l *leaf, data []byte, p Provenance) error {
	if l.secret {
		return r.setSecret(l, data, p)
	}
	v, err := decodeLeaf(l, data)
	if err != nil {
		msg := describeDecode(l, err)
		if i := unknownFieldEntry(l, data); i > 0 {
			msg = fmt.Sprintf("entry %d %s", i, msg)
		}
		return fmt.Errorf("%s: %s", l.path, msg)
	}
	l.field(&r.tree).Set(v)
	r.prov[l.path] = p
	return nil
}

// unknownFieldEntry is the position (from 1) of the first entry of a list-of-objects leaf that
// has a field the entry's type does not know, or 0. The key itself is never named (it can come
// from the UI), so the position is what lets an operator find it in the file.
func unknownFieldEntry(l *leaf, data []byte) int {
	if l.typ.Kind() != reflect.Slice || l.typ.Elem().Kind() != reflect.Struct {
		return 0
	}
	var entries []json.RawMessage
	if json.Unmarshal(data, &entries) != nil {
		return 0
	}
	for i, raw := range entries {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(reflect.New(l.typ.Elem()).Interface()); err != nil && strings.HasPrefix(err.Error(), "json: unknown field ") {
			return i + 1
		}
	}
	return 0
}

// errTrailingData is decodeLeaf's error for data after the JSON value.
var errTrailingData = errors.New("unexpected data after the value")

// decodeLeaf reads one JSON value of the leaf's type, strictly: unknown fields and trailing data
// are errors.
func decodeLeaf(l *leaf, data []byte) (reflect.Value, error) {
	v := reflect.New(l.typ)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v.Interface())
	if err == nil {
		if _, e := dec.Token(); e != io.EOF {
			err = errTrailingData
		}
	}
	return v.Elem(), err
}

// setSecret keeps what a layer says about a secret leaf, a literal or a reference, until every
// layer is applied; resolveSecrets reads it once. The UI layer may only hold what is safe to
// keep in ui-settings.yaml: an env: or file: reference from the UI would let anyone who can
// edit settings read any variable or file of the dash process (S.14), and a literal would be
// stored in clear. What it holds is an encrypted value (enc:v1:, P1.5) or a k8s: reference.
func (r *resolver) setSecret(l *leaf, data []byte, p Provenance) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("%s: expected a string", l.path)
	}
	s = strings.TrimSpace(s)
	if p.Source == SourceUI && secret.IsEncrypted(s) {
		r.openSealed(l, s, p)
		return nil
	}
	if p.Source == SourceUI && !strings.HasPrefix(s, "k8s:") {
		return fmt.Errorf("%s: only an encrypted (enc:v1:) value or a k8s: reference can be set from the UI", l.path)
	}
	delete(r.plain, l.path)
	r.raw[l.path] = s
	r.prov[l.path] = p
	return nil
}

// openSealed decrypts a stored secret with the field path as the associated data, so a value
// moved to another field fails like a wrong key does. A value that does not open is recorded
// for resolveSecrets to fail on, whether or not a later layer overrides the field.
func (r *resolver) openSealed(l *leaf, sealed string, p Provenance) {
	plain, err := r.keys.Decrypt(l.path, sealed)
	if err != nil {
		r.sealedErrs = append(r.sealedErrs, fmt.Errorf("%s: %w", l.path, err))
		return
	}
	delete(r.raw, l.path)
	r.plain[l.path] = plain
	r.prov[l.path] = p
}

// resolveSecrets reads the references of every secret leaf into its secret.Value and registers
// it. A reference that cannot be read fails startup naming the field and the layer that set it,
// never the value. So does a stored encrypted secret that cannot be decrypted, listing every
// such field (the settings store is left as it is).
func (r *resolver) resolveSecrets(reg *secret.Registry) error {
	var errs []error
	if len(r.sealedErrs) > 0 {
		errs = append(errs, fmt.Errorf("the settings store holds encrypted secrets that cannot be decrypted (nothing was changed or dropped); fix DASH_SECRET_KEY and DASH_SECRET_KEY_PREVIOUS, or restore the store:\n%w", errors.Join(r.sealedErrs...)))
	}
	for _, l := range schema.leaves {
		var v secret.Value
		if plain, ok := r.plain[l.path]; ok {
			v = secret.New(plain, "")
		} else if raw := r.raw[l.path]; raw != "" {
			var err error
			if r.skipSecrets && (strings.HasPrefix(raw, "env:") || strings.HasPrefix(raw, "file:")) {
				// Validation only needs to know the secret is set; the variable or file it points
				// at may exist only where dash runs. The placeholder is never used for a request.
				v = secret.New("unread", raw)
			} else if r.k8s != nil && strings.HasPrefix(raw, "k8s:") {
				// A pending handle: the kubernetes plugin fills it. The error names the field
				// and never the reference.
				if v, err = r.k8sHandle(raw); err != nil {
					err = fmt.Errorf("%s%s: %w", l.path, where(r.prov, l.path), err)
					if r.strictUI != nil && r.prov[l.path].Source == SourceUI {
						// A candidate's reference: the UI form shows it at its field.
						err = &fieldError{error: err, field: &FieldError{Path: l.path, Message: errors.Unwrap(err).Error()}}
					}
					errs = append(errs, err)
					continue
				}
			} else if v, err = secret.Resolve(raw, l.path+where(r.prov, l.path)); err != nil {
				errs = append(errs, err)
				continue
			}
			if r.prov[l.path].Source == SourceFile && v.Ref() == "" {
				// Config files end up in git; a reference keeps the secret out of them.
				r.log.Warn("secret in the config file is a literal; use env:NAME or file:/path instead", "path", l.path, "file", r.prov[l.path].Ref)
			}
		} else {
			continue
		}
		if reg != nil {
			reg.Add(v)
		}
		l.field(&r.tree).Set(reflect.ValueOf(v))
	}
	return errors.Join(errs...)
}

// describeDecode turns a encoding/json error into one an operator can act on. Every text is
// fixed: an unknown field's error names the key, another decoder's error can quote the value,
// and a key or value from the UI is never repeated (design D16).
func describeDecode(l *leaf, err error) string {
	var te *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &te):
		// Value is the JSON kind, sometimes followed by the literal ("number 99999999999999999999"):
		// only the kind is named, never what was sent.
		kind, _, _ := strings.Cut(te.Value, " ")
		return fmt.Sprintf("expected %s, got %s", humanType(l.typ), kind)
	case errors.As(err, &se):
		return "invalid JSON"
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return "has a field this setting does not know"
	case errors.Is(err, errTrailingData), fixedDecodeTexts[err.Error()]:
		return err.Error()
	}
	return "is not valid for this setting"
}

// fixedDecodeTexts are the errors of the tree's own decoders whose text is fixed (never the
// value): pluginconf.Duration's. describeDecode passes them on; any other is replaced.
var fixedDecodeTexts = map[string]bool{
	`expected a duration string such as "30s"`:   true,
	"is not a duration such as 30s, 5m or 1h30m": true,
}

func humanType(t reflect.Type) string {
	switch {
	case t.Kind() == reflect.Int:
		return "a whole number"
	case t.Kind() == reflect.String:
		return "a string"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return "a list of strings"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Int:
		return "a JSON list of integers"
	case t.Kind() == reflect.Slice:
		return "a list of objects"
	case t.Kind() == reflect.Map:
		return "an object of strings"
	}
	return t.String()
}

// envMap indexes os.Environ-style entries; the last one wins, as in a shell.
func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func envTrue(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "true") }

// demoClockEnv fixes the clock of demo mode's world (Config.DemoClock).
const demoClockEnv = "DASH_DEMO_CLOCK"

// demoClockOf reads DASH_DEMO_CLOCK: an RFC 3339 instant, accepted only with DASH_DEMO=true.
// Unset or empty is the zero time (the wall clock). Outside demo mode it is refused rather than
// ignored: a fixed clock in a live deployment would freeze every timestamp it shows.
func demoClockOf(env map[string]string, demo bool) (time.Time, error) {
	v := strings.TrimSpace(env[demoClockEnv])
	if v == "" {
		return time.Time{}, nil
	}
	if !demo {
		return time.Time{}, fmt.Errorf("%s is only for demo mode: set DASH_DEMO=true or unset %s", demoClockEnv, demoClockEnv)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s=%q is not an RFC 3339 time such as 2026-01-15T12:00:00Z", demoClockEnv, v)
	}
	return t.UTC(), nil
}
