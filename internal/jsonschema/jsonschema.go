// Package jsonschema reflects Go types into JSON Schema 2020-12. dash uses it for the
// configuration file schema (`dash config schema`), for the input schema of plugin actions and
// for the OpenAPI document, so one tag grammar describes all three.
//
// A field is read from its struct tags:
//
//	json:"name,omitempty"   the property name; "-" hides the field; an embedded struct without
//	                        a name is flattened, as encoding/json does
//	dash:"a,b=c"            comma-separated options:
//	                          required        the property is in the object's "required" list
//	                          secret          writeOnly: true and x-dash-secret: true
//	                          restart         x-dash-restart: true (a change needs a restart)
//	                          url             format: uri
//	                          format=<f>      format: <f>
//	                          enum=a|b|c      enum: [a, b, c] (numbers for integer fields)
//	doc:"text"              description
//
// tstype tags (read by tygo) are ignored. A pointer is its element type and, unlike required,
// says nothing about presence. A struct type is an object with additionalProperties: false.
// A type that has a JSONSchema method, secret.Value, time.Time and json.RawMessage describe
// themselves; anything the reflector does not know panics at generation, so that a test and
// not a request finds it.
package jsonschema

import (
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

// Schema is one JSON Schema object. json.Marshal sorts map keys, so the output is deterministic.
type Schema = map[string]any

// Draft is the dialect of every Document.
const Draft = "https://json-schema.org/draft/2020-12/schema"

// Option configures a Reflector.
type Option func(*Reflector)

// WithRefPrefix makes named struct types definitions that properties refer to, as
// {"$ref": prefix + name}. "" (the default) inlines every type. Use "#/$defs/" for a standalone
// document and "#/components/schemas/" for OpenAPI.
func WithRefPrefix(p string) Option { return func(r *Reflector) { r.prefix = p } }

// WithDefaults takes a value of the root type: each of its non-zero fields becomes the
// "default" of its property. A definition shared by two properties must get the same defaults
// at both places, or generation panics.
func WithDefaults(v any) Option { return func(r *Reflector) { r.defaults = reflect.ValueOf(v) } }

// hook is implemented by types that provide their own schema (config.Duration).
type hook interface{ JSONSchema() Schema }

var (
	hookType   = reflect.TypeFor[hook]()
	secretType = reflect.TypeFor[secret.Value]()
	timeType   = reflect.TypeFor[time.Time]()
	rawType    = reflect.TypeFor[json.RawMessage]()
)

// genError is what generation panics with; Document turns it into an error.
type genError string

func fail(format string, args ...any) { panic(genError(fmt.Sprintf(format, args...))) }

// Reflector turns types into schemas, collecting the named struct types it refers to.
type Reflector struct {
	prefix   string
	defaults reflect.Value

	defs     map[reflect.Type]Schema // finished definitions
	building map[reflect.Type]bool   // struct types being reflected, to end recursion
	pending  []pendingRef            // $ref values to fill in once every name is known
}

type pendingRef struct {
	m Schema
	t reflect.Type
}

// New returns a Reflector. Reuse one for several types to share their definitions.
func New(opts ...Option) *Reflector {
	r := &Reflector{defs: map[reflect.Type]Schema{}, building: map[reflect.Type]bool{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Type returns the schema of t: a $ref when t is a named struct and a prefix is set, an inline
// schema otherwise. Named structs are collected for Defs. When t is the type of the
// WithDefaults value, its defaults are applied.
func (r *Reflector) Type(t reflect.Type) Schema {
	s := r.schema(t, r.rootDefaults(t))
	r.finalize()
	return s
}

// Defs returns the collected named types by name: the Go name, or pkg.Name when two types
// share one. The map is a copy; its schemas are not.
func (r *Reflector) Defs() map[string]Schema {
	r.finalize()
	names := r.names()
	out := make(map[string]Schema, len(r.defs))
	for t, s := range r.defs {
		out[names[t]] = s
	}
	return out
}

// Document returns the JSON Schema 2020-12 document of v's type: the root schema inline, with
// $schema and, when a ref prefix is set, $defs. A generation problem is returned as an error.
func Document(v any, opts ...Option) (doc Schema, err error) {
	defer func() {
		if p := recover(); p != nil {
			g, ok := p.(genError)
			if !ok {
				panic(p)
			}
			doc, err = nil, fmt.Errorf("jsonschema: %s", string(g))
		}
	}()
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("jsonschema: no type to describe")
	}
	r := New(opts...)
	d := r.rootDefaults(t)
	t, d = deref(t, d)
	var root Schema
	if t.Kind() == reflect.Struct && !r.selfDescribing(t) {
		r.building[t] = true
		root = r.structBody(t, d)
	} else {
		root = r.schema(t, d)
	}
	if slices.ContainsFunc(r.pending, func(p pendingRef) bool { return p.t == t }) {
		r.defs[t] = maps.Clone(root) // the root refers to itself
	}
	r.finalize()
	root["$schema"] = Draft
	if defs := r.Defs(); len(defs) > 0 {
		root["$defs"] = defs
	}
	return root, nil
}

// rootDefaults is the WithDefaults value when t is its type.
func (r *Reflector) rootDefaults(t reflect.Type) reflect.Value {
	if !r.defaults.IsValid() {
		return reflect.Value{}
	}
	dt, dv := deref(r.defaults.Type(), r.defaults)
	if tt, _ := deref(t, reflect.Value{}); tt != dt {
		return reflect.Value{}
	}
	return dv
}

// deref strips pointers from t and, when d is valid, from d (a nil pointer ends the defaults).
func deref(t reflect.Type, d reflect.Value) (reflect.Type, reflect.Value) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
		if d.IsValid() {
			if d.IsNil() {
				d = reflect.Value{}
			} else {
				d = d.Elem()
			}
		}
	}
	if d.IsValid() && d.Kind() == reflect.Interface {
		d = reflect.Value{}
	}
	return t, d
}

// selfDescribing reports whether t has a schema of its own rather than one made from its fields.
func (r *Reflector) selfDescribing(t reflect.Type) bool {
	return t == secretType || t == timeType || t == rawType || hasHook(t)
}

func hasHook(t reflect.Type) bool {
	return t.Implements(hookType) || reflect.PointerTo(t).Implements(hookType)
}

// schema reflects t; d is the default value of the property being reflected, or invalid.
func (r *Reflector) schema(t reflect.Type, d reflect.Value) Schema {
	t, d = deref(t, d)
	switch {
	case t == secretType:
		return Schema{"type": "string", "writeOnly": true, "x-dash-secret": true}
	case t == timeType:
		return Schema{"type": "string", "format": "date-time"}
	case t == rawType:
		return Schema{}
	case hasHook(t):
		return maps.Clone(reflect.New(t).Interface().(hook).JSONSchema())
	}
	switch t.Kind() {
	case reflect.String:
		return Schema{"type": "string"}
	case reflect.Bool:
		return Schema{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Schema{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return Schema{"type": "number"}
	case reflect.Interface:
		return Schema{}
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return Schema{"type": "string", "contentEncoding": "base64"}
		}
		return Schema{"type": "array", "items": r.schema(t.Elem(), reflect.Value{})}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			fail("%s: map keys must be strings", t)
		}
		return Schema{"type": "object", "additionalProperties": r.schema(t.Elem(), reflect.Value{})}
	case reflect.Struct:
		return r.structSchema(t, d)
	}
	fail("unsupported type %s (%s)", t, t.Kind())
	return nil
}

// structSchema is the schema of a struct property: its body inline, or a reference to its
// definition.
func (r *Reflector) structSchema(t reflect.Type, d reflect.Value) Schema {
	if r.prefix == "" || t.Name() == "" {
		if r.building[t] {
			fail("%s is recursive: set a ref prefix", t)
		}
		r.building[t] = true
		defer delete(r.building, t)
		return r.structBody(t, d)
	}
	ref := Schema{"$ref": ""}
	r.pending = append(r.pending, pendingRef{ref, t})
	if r.building[t] {
		return ref
	}
	r.building[t] = true
	body := r.structBody(t, d)
	delete(r.building, t)
	if prev, ok := r.defs[t]; ok {
		// The same type at another place: its definition must come out the same. Both are
		// resolved first, so only the shapes are compared.
		r.finalize()
		if !reflect.DeepEqual(prev, body) {
			fail("%s has different defaults at two places; give each its own type", t)
		}
		return ref
	}
	r.defs[t] = body
	return ref
}

// structBody is the object schema of t's fields.
func (r *Reflector) structBody(t reflect.Type, d reflect.Value) Schema {
	props := Schema{}
	var required []string
	r.fields(t, d, props, &required)
	s := Schema{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		slices.Sort(required)
		s["required"] = required
	}
	return s
}

func (r *Reflector) fields(t reflect.Type, d reflect.Value, props Schema, required *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("json") == "-" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		var fd reflect.Value
		if d.IsValid() {
			fd = d.Field(i)
		}
		if f.Anonymous && name == "" {
			if et, ed := deref(f.Type, fd); et.Kind() == reflect.Struct && !r.selfDescribing(et) {
				r.fields(et, ed, props, required)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		prop := r.schema(f.Type, fd)
		if doc := f.Tag.Get("doc"); doc != "" {
			prop["description"] = doc
		}
		req := applyOptions(name, f, prop)
		if req {
			*required = append(*required, name)
		}
		if fd.IsValid() && !fd.IsZero() && !isObject(prop) {
			if _, secret := prop["x-dash-secret"]; !secret {
				prop["default"] = jsonValue(fd)
			}
		}
		props[name] = prop
	}
}

// isObject reports whether prop describes a struct (a reference or an inline object), whose
// defaults belong to its own fields.
func isObject(prop Schema) bool {
	_, ref := prop["$ref"]
	_, props := prop["properties"]
	return ref || props
}

// applyOptions applies the dash tag of f to prop and reports whether the field is required.
func applyOptions(name string, f reflect.StructField, prop Schema) (required bool) {
	tag := f.Tag.Get("dash")
	if tag == "" {
		return false
	}
	for opt := range strings.SplitSeq(tag, ",") {
		key, val, hasVal := strings.Cut(opt, "=")
		switch {
		case key == "required" && !hasVal:
			required = true
		case key == "secret" && !hasVal:
			prop["writeOnly"], prop["x-dash-secret"] = true, true
		case key == "restart" && !hasVal:
			prop["x-dash-restart"] = true
		case key == "url" && !hasVal:
			prop["format"] = "uri"
		case key == "format" && hasVal && val != "":
			prop["format"] = val
		case key == "enum" && hasVal && val != "":
			prop["enum"] = enumValues(name, f.Type, val)
		default:
			fail("%s: unknown dash tag option %q", name, opt)
		}
	}
	return required
}

func enumValues(name string, t reflect.Type, list string) []any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var out []any
	for v := range strings.SplitSeq(list, "|") {
		switch t.Kind() {
		case reflect.String:
			out = append(out, v)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				fail("%s: enum value %q is not an integer", name, v)
			}
			out = append(out, n)
		default:
			fail("%s: enum is only for strings and integers, not %s", name, t)
		}
	}
	return out
}

// jsonValue is v as encoding/json writes it.
func jsonValue(v reflect.Value) any {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		fail("default %v: %v", v.Type(), err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		fail("default %v: %v", v.Type(), err)
	}
	return out
}

// finalize writes the $ref of every reference now that the names are known. Two types with one
// Go name are named by package, so a name can change when another type is added; the schemas
// already returned are updated in place.
func (r *Reflector) finalize() {
	names := r.names()
	for _, p := range r.pending {
		p.m["$ref"] = r.prefix + names[p.t]
	}
}

// names assigns each definition its name: the Go name when unique, else pkg.Name, else the
// package path with dots.
func (r *Reflector) names() map[reflect.Type]string {
	byName := map[string][]reflect.Type{}
	for t := range r.defs {
		byName[t.Name()] = append(byName[t.Name()], t)
	}
	out := make(map[reflect.Type]string, len(r.defs))
	for name, ts := range byName {
		if len(ts) == 1 {
			out[ts[0]] = sanitize(name)
			continue
		}
		byPkg := map[string]int{}
		for _, t := range ts {
			byPkg[path.Base(t.PkgPath())+"."+name]++
		}
		for _, t := range ts {
			q := path.Base(t.PkgPath()) + "." + name
			if byPkg[q] > 1 {
				q = strings.ReplaceAll(t.PkgPath(), "/", ".") + "." + name
			}
			out[t] = sanitize(q)
		}
	}
	return out
}

// sanitize keeps a definition name usable in a JSON pointer and a URI fragment.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}
