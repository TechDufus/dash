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

	"sigs.k8s.io/yaml"
)

// Source is the layer an effective value came from (design B.2).
type Source string

const (
	SourceDefault Source = "default"
	SourceUI      Source = "ui"
	SourceFile    Source = "file"
	SourceEnv     Source = "env"
)

// Provenance says where one effective value came from. Ref is the environment variable or the
// config file path; it is empty for defaults and the UI layer.
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
	// UI is the sparse, nested map of admin-saved values (settings.Store.Values), or nil.
	UI map[string]any
	// Log receives warnings; nil means slog.Default().
	Log *slog.Logger
}

// Resolved is the outcome of Resolve.
type Resolved struct {
	Tree Tree
	// Provenance is keyed by dotted path (e.g. "plugins.proxmox.url"). Lists are one entry.
	Provenance map[string]Provenance
	// Config is the flat view of Tree. Its launcher file, if any, is not read yet (FromEnv does).
	Config Config
}

// Resolve layers the sources, validates the result and maps it onto the flat Config. An invalid
// file or environment is an error; stale or UI-ineligible UI values are ignored with a warning
// so that a stored setting can never keep dash from starting.
func Resolve(src Sources) (*Resolved, error) {
	log := src.Log
	if log == nil {
		log = slog.Default()
	}
	env := envMap(src.Environ)
	dev, demo := envTrue(env["DASH_DEV"]), envTrue(env["DASH_DEMO"])

	r := &resolver{tree: defaults(dev, demo), prov: map[string]Provenance{}, log: log}
	for _, l := range schema.leaves {
		r.prov[l.path] = Provenance{Source: SourceDefault}
	}
	if src.UI != nil {
		r.applyMap(schema.root, "", src.UI, Provenance{Source: SourceUI}, true)
	}
	if src.File != nil {
		if err := r.applyFile(src.File); err != nil {
			return nil, err
		}
	}
	entries, envErr := envLayer(env, log)
	errs := []error{envErr}
	for _, e := range entries {
		if err := r.set(schema.byPath[e.path], e.data, Provenance{Source: SourceEnv, Ref: e.ref}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.ref, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	r.tree.normalize()
	if err := validate(r.tree, dev, demo, r.prov); err != nil {
		return nil, err
	}
	return &Resolved{Tree: r.tree, Provenance: r.prov, Config: r.tree.config(dev, demo)}, nil
}

type resolver struct {
	tree Tree
	prov map[string]Provenance
	log  *slog.Logger
}

// applyFile parses the YAML file strictly and applies it. Every problem is reported, each with
// its full path ("plugins.proxmox.urll: unknown field").
func (r *resolver) applyFile(f *File) error {
	var doc map[string]any
	if err := yaml.UnmarshalStrict(f.Data, &doc); err != nil {
		return fmt.Errorf("config file %s: %w", f.Path, err)
	}
	if errs := r.applyMap(schema.root, "", doc, Provenance{Source: SourceFile, Ref: f.Path}, false); len(errs) > 0 {
		return fmt.Errorf("config file %s: %w", f.Path, errors.Join(errs...))
	}
	return nil
}

// applyMap walks a nested map against the schema. For the UI layer (ui) problems are warnings:
// that store is written by dash and may hold keys of another version.
func (r *resolver) applyMap(n *node, prefix string, m map[string]any, p Provenance, ui bool) []error {
	var errs []error
	problem := func(path, msg string) {
		if ui {
			r.log.Warn("ignoring UI setting", "path", path, "reason", msg)
			return
		}
		errs = append(errs, fmt.Errorf("%s: %s", path, msg))
	}
	for _, key := range slices.Sorted(maps.Keys(m)) {
		path, v := prefix+key, m[key]
		if ui && uiIneligible(path) {
			r.warnIneligible(path, v)
			continue
		}
		c := n.child(key)
		switch {
		case c == nil:
			problem(path, "unknown field")
		case v == nil:
			// "key:" with no value leaves the lower layers alone.
		case c.leaf != nil:
			data, err := json.Marshal(v)
			if err == nil {
				err = r.set(c.leaf, data, p)
			}
			if err != nil {
				problem(path, strings.TrimPrefix(err.Error(), c.leaf.path+": "))
			}
		default:
			sub, ok := v.(map[string]any)
			if !ok {
				problem(path, "expected a section (a map of settings)")
				continue
			}
			errs = append(errs, r.applyMap(c, path+".", sub, p, ui)...)
		}
	}
	return errs
}

// warnIneligible warns once per leaf under a UI value at a path the UI may not set.
func (r *resolver) warnIneligible(path string, v any) {
	if m, ok := v.(map[string]any); ok {
		for _, k := range slices.Sorted(maps.Keys(m)) {
			r.warnIneligible(path+"."+k, m[k])
		}
		return
	}
	r.log.Warn("ignoring UI setting", "path", path, "reason", "server, auth, settings.ui and dataDir cannot be set from the UI")
}

// set decodes JSON data into the leaf, strictly, and records its provenance.
func (r *resolver) set(l *leaf, data []byte, p Provenance) error {
	v := reflect.New(l.typ)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err := dec.Decode(v.Interface())
	if err == nil {
		if _, e := dec.Token(); e != io.EOF {
			err = errors.New("unexpected data after the value")
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %s", l.path, describeDecode(l, err))
	}
	l.field(&r.tree).Set(v.Elem())
	r.prov[l.path] = p
	return nil
}

// describeDecode turns a encoding/json error into one an operator can act on.
func describeDecode(l *leaf, err error) string {
	var te *json.UnmarshalTypeError
	var se *json.SyntaxError
	switch {
	case errors.As(err, &te):
		return fmt.Sprintf("expected %s, got %s", humanType(l.typ), te.Value)
	case errors.As(err, &se):
		return "invalid JSON: " + se.Error()
	}
	return strings.TrimPrefix(err.Error(), "json: ")
}

func humanType(t reflect.Type) string {
	switch {
	case t.Kind() == reflect.String:
		return "a string"
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return "a list of strings"
	case t.Kind() == reflect.Slice:
		return "a list of objects"
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
