package config

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// Leaf describes one setting of the schema for the settings API (P4.7).
type Leaf struct {
	Path string
	Env  string // the environment variable that sets it
	// Type is the JSON type of the value: string, integer, number, boolean, array or object.
	Type    string
	Secret  bool
	Restart bool
	// URL: a format: uri leaf, whose host or port is a destination (design D16).
	URL bool
	// Dest: any change of the value changes where or how requests are sent (D16).
	Dest bool
	// Ineligible: the UI layer can never set it (server.*, auth.*, settings.ui,
	// settings.secretNamespaces, dataDir).
	Ineligible bool
	Enum       []string
	Doc        string
}

func (l *leaf) info() Leaf {
	return Leaf{
		Path: l.path, Env: l.env, Type: jsonType(l.typ), Secret: l.secret, Restart: l.restart, URL: l.url,
		Dest: l.dest, Ineligible: uiIneligible(l.path), Enum: l.enum, Doc: l.doc,
	}
}

// Leaves lists every setting whose path starts with prefix ("" for all), in schema order.
func Leaves(prefix string) []Leaf {
	var out []Leaf
	for _, l := range schema.leaves {
		if strings.HasPrefix(l.path, prefix) {
			out = append(out, l.info())
		}
	}
	return out
}

// LeafAt describes the setting at path; ok is false when path is not a leaf (a section, or
// nothing).
func LeafAt(path string) (Leaf, bool) {
	l := schema.byPath[path]
	if l == nil {
		return Leaf{}, false
	}
	return l.info(), true
}

// jsonType is the JSON Schema type of a leaf's values.
func jsonType(t reflect.Type) string {
	if t == secretType || t == durationType {
		return "string"
	}
	switch t.Kind() {
	case reflect.Pointer:
		return jsonType(t.Elem())
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	}
	return "object"
}

// Value is the effective value of the leaf at path as JSON would carry it (a Duration is its
// text, a list a []any), with the userinfo of every URL in it removed. A secret has none: ok is
// false for a secret and for a path that is not a leaf.
func (r *Resolved) Value(path string) (any, bool) {
	l := schema.byPath[path]
	if l == nil || l.secret {
		return nil, false
	}
	b, err := json.Marshal(l.field(&r.Tree).Interface())
	if err != nil {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return RedactURLs(v), true
}

// Secret is the effective value of the secret leaf at path; ok is false for any other path.
func (r *Resolved) Secret(path string) (secret.Value, bool) {
	l := schema.byPath[path]
	if l == nil || !l.secret {
		return secret.Value{}, false
	}
	return l.field(&r.Tree).Interface().(secret.Value), true
}

// RedactURLs returns v (JSON-shaped: maps, lists, strings) with the userinfo of every absolute
// URL string removed: credentials belong in the secret fields, and a view never shows them
// (design D16).
func RedactURLs(v any) any {
	switch v := v.(type) {
	case string:
		if !strings.Contains(v, "@") || !strings.Contains(v, "://") {
			return v
		}
		u, err := url.Parse(v)
		if err != nil {
			// Unparsable but shaped like scheme://user@host: drop everything up to the @.
			scheme, rest, _ := strings.Cut(v, "://")
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				return scheme + "://" + rest[at+1:]
			}
			return v
		}
		if u.User == nil {
			return v
		}
		u.User = nil
		return u.String()
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = RedactURLs(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = RedactURLs(e)
		}
		return out
	}
	return v
}

// StoredAt is the value the sparse, nested UI store map holds at the dotted path.
func StoredAt(stored map[string]any, path string) (any, bool) {
	var cur any = stored
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// SettingFields builds the settings view of every leaf for which keep is true, in schema order:
// the effective value from res with its provenance, what the store holds (stored, the UI layer),
// and whether the UI may write it. readonly is the store's state. A secret carries metadata
// only, never its value.
func SettingFields(res *Resolved, stored map[string]any, readonly bool, keep func(path string) bool) []model.SettingField {
	var out []model.SettingField
	for _, l := range schema.leaves {
		if keep(l.path) {
			out = append(out, settingField(res, stored, readonly, l))
		}
	}
	return out
}

// SettingField is the settings view of the leaf at path; ok is false when path is not a leaf.
func SettingField(res *Resolved, stored map[string]any, readonly bool, path string) (model.SettingField, bool) {
	l := schema.byPath[path]
	if l == nil {
		return model.SettingField{}, false
	}
	return settingField(res, stored, readonly, l), true
}

func settingField(res *Resolved, stored map[string]any, readonly bool, l *leaf) model.SettingField {
	p := res.Provenance[l.path]
	f := model.SettingField{
		Path: l.path, Type: jsonType(l.typ), Secret: l.secret, Source: model.SettingSource(p.Source),
		Restart: l.restart, Enum: l.enum, Doc: l.doc,
	}
	if f.Source == "" {
		f.Source = model.SettingSourceDefault
	}
	_, f.Stored = StoredAt(stored, l.path)
	switch p.Source {
	case SourceEnv, SourceFile:
		f.Ref, f.Locked, f.LockedBy = p.Ref, true, model.SettingLock(p.Source)
		f.Shadowed = f.Stored
	case SourceDefault, SourceUI:
		switch {
		case uiIneligible(l.path):
			f.Locked, f.LockedBy = true, model.SettingLockIneligible
		case readonly:
			f.Locked, f.LockedBy = true, model.SettingLockReadonly
		}
	}
	if l.secret {
		v := l.field(&res.Tree).Interface().(secret.Value)
		f.Set = v.Present()
		if p.Source == SourceUI && strings.HasPrefix(v.Ref(), "k8s:") {
			f.Ref = v.Ref() // a reference, never a value
		}
		return f
	}
	f.Set = !l.field(&res.Tree).IsZero()
	f.Value, _ = res.Value(l.path)
	return f
}
