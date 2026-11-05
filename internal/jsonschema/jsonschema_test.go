package jsonschema

import (
	"encoding/json"
	htmltemplate "html/template"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

type dur int64

func (dur) JSONSchema() Schema { return Schema{"type": "string", "format": "duration"} }

type inner struct {
	Name string `json:"name" doc:"the name"`
}

type tagged struct {
	Name     string          `json:"name" dash:"required" doc:"what it is called"`
	Token    secret.Value    `json:"token" dash:"secret" doc:"a credential"`
	Key      string          `json:"key" dash:"secret"`
	Listen   string          `json:"listen" dash:"restart"`
	Endpoint string          `json:"endpoint" dash:"url"`
	Mode     string          `json:"mode" dash:"enum=a|b|c"`
	Level    int             `json:"level" dash:"enum=1|2"`
	Contact  string          `json:"contact" dash:"format=email"`
	Every    dur             `json:"every"`
	At       time.Time       `json:"at"`
	Raw      json.RawMessage `json:"raw" tstype:"unknown"`
	Any      any             `json:"any"`
	Ptr      *inner          `json:"ptr"`
	List     []inner         `json:"list"`
	Labels   map[string]int  `json:"labels"`
	Skipped  string          `json:"-"`
	hidden   string
	Plain    bool
}

func TestReflectorTags(t *testing.T) {
	s, err := Document(tagged{}, WithRefPrefix("#/$defs/"))
	if err != nil {
		t.Fatal(err)
	}
	if s["$schema"] != Draft || s["type"] != "object" || s["additionalProperties"] != false {
		t.Errorf("root: %v", s)
	}
	if got := s["required"]; !reflect.DeepEqual(got, []string{"name"}) {
		t.Errorf("required = %v", got)
	}
	props := s["properties"].(Schema)
	for _, hidden := range []string{"Skipped", "hidden", "-"} {
		if _, ok := props[hidden]; ok {
			t.Errorf("%s must not be a property", hidden)
		}
	}
	want := map[string]Schema{
		"name":     {"type": "string", "description": "what it is called"},
		"token":    {"type": "string", "writeOnly": true, "x-dash-secret": true, "description": "a credential"},
		"key":      {"type": "string", "writeOnly": true, "x-dash-secret": true},
		"listen":   {"type": "string", "x-dash-restart": true},
		"endpoint": {"type": "string", "format": "uri"},
		"mode":     {"type": "string", "enum": []any{"a", "b", "c"}},
		"level":    {"type": "integer", "enum": []any{int64(1), int64(2)}},
		"contact":  {"type": "string", "format": "email"},
		"every":    {"type": "string", "format": "duration"},
		"at":       {"type": "string", "format": "date-time"},
		"raw":      {},
		"any":      {},
		"ptr":      {"$ref": "#/$defs/inner"},
		"list":     {"type": "array", "items": Schema{"$ref": "#/$defs/inner"}},
		"labels":   {"type": "object", "additionalProperties": Schema{"type": "integer"}},
		"Plain":    {"type": "boolean"},
	}
	for name, w := range want {
		if got := props[name]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %#v, want %#v", name, got, w)
		}
	}
	if len(props) != len(want) {
		t.Errorf("%d properties, want %d: %v", len(props), len(want), props)
	}
	defs := s["$defs"].(map[string]Schema)
	if got := defs["inner"]["properties"].(Schema)["name"]; !reflect.DeepEqual(got, Schema{"type": "string", "description": "the name"}) {
		t.Errorf("inner.name = %v", got)
	}
}

func TestReflectorInlinesWithoutAPrefix(t *testing.T) {
	s := New().Type(reflect.TypeFor[tagged]())
	ptr := s["properties"].(Schema)["ptr"].(Schema)
	if _, ref := ptr["$ref"]; ref || ptr["type"] != "object" {
		t.Errorf("ptr should be inlined: %v", ptr)
	}
}

func TestReflectorEmbeddedStructsAreFlattened(t *testing.T) {
	type base struct {
		Type    string `json:"type"`
		Enabled *bool  `json:"enabled"`
	}
	type section struct {
		base
		URL string `json:"url"`
	}
	props := New().Type(reflect.TypeFor[section]())["properties"].(Schema)
	for _, k := range []string{"type", "enabled", "url"} {
		if _, ok := props[k]; !ok {
			t.Errorf("missing %s in %v", k, props)
		}
	}
	if props["enabled"].(Schema)["type"] != "boolean" {
		t.Errorf("a pointer is its element type: %v", props["enabled"])
	}
}

// min and max bound an integer's value and a string's length; nullable admits null beside the
// field's own type.
func TestReflectorBoundsAndNullable(t *testing.T) {
	type bounded struct {
		Name string     `json:"name" dash:"min=1,max=64"`
		Days *int       `json:"days" dash:"min=1,max=365"`
		At   *time.Time `json:"at" dash:"nullable"`
	}
	props := New().Type(reflect.TypeFor[bounded]())["properties"].(Schema)
	for name, w := range map[string]Schema{
		"name": {"type": "string", "minLength": int64(1), "maxLength": int64(64)},
		"days": {"type": "integer", "minimum": int64(1), "maximum": int64(365)},
		"at":   {"type": []string{"string", "null"}, "format": "date-time"},
	} {
		if got := props[name]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %#v, want %#v", name, got, w)
		}
	}
	for name, v := range map[string]any{
		"min on a bool": struct {
			B bool `dash:"min=1"`
		}{},
		"max not a number": struct {
			N int `dash:"max=x"`
		}{},
	} {
		if _, err := Document(v); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

type withDefaults struct {
	Listen string       `json:"listen"`
	Count  int          `json:"count"`
	Off    int          `json:"off"`
	Hosts  []string     `json:"hosts"`
	Every  dur          `json:"every"`
	Token  secret.Value `json:"token" dash:"secret"`
	Limits struct {
		Max int `json:"max"`
	} `json:"limits"`
	Sub inner `json:"sub"`
}

func TestReflectorDefaults(t *testing.T) {
	v := withDefaults{Listen: "127.0.0.1:8080", Count: 3, Hosts: []string{"a"}, Every: 5, Token: secret.New("x", "")}
	v.Limits.Max = 9
	v.Sub.Name = "n"
	s, err := Document(v, WithRefPrefix("#/$defs/"), WithDefaults(v))
	if err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(Schema)
	for name, want := range map[string]any{"listen": "127.0.0.1:8080", "count": float64(3), "hosts": []any{"a"}, "every": float64(5)} {
		if got := props[name].(Schema)["default"]; !reflect.DeepEqual(got, want) {
			t.Errorf("default of %s = %#v, want %#v", name, got, want)
		}
	}
	if _, ok := props["off"].(Schema)["default"]; ok {
		t.Error("a zero field has no default")
	}
	if _, ok := props["token"].(Schema)["default"]; ok {
		t.Error("a secret has no default")
	}
	// Defaults of a section sit on its own fields, inside its definition or inline.
	max := props["limits"].(Schema)["properties"].(Schema)["max"].(Schema)
	if max["default"] != float64(9) {
		t.Errorf("limits.max = %v", max)
	}
	name := s["$defs"].(map[string]Schema)["inner"]["properties"].(Schema)["name"].(Schema)
	if name["default"] != "n" {
		t.Errorf("inner.name = %v", name)
	}
}

func TestReflectorRefusesOneTypeWithTwoDefaults(t *testing.T) {
	type pair struct {
		A inner `json:"a"`
		B inner `json:"b"`
	}
	v := pair{A: inner{Name: "x"}, B: inner{Name: "y"}}
	_, err := Document(v, WithRefPrefix("#/$defs/"), WithDefaults(v))
	if err == nil || !strings.Contains(err.Error(), "different defaults") {
		t.Errorf("error = %v", err)
	}
	same := pair{A: inner{Name: "x"}, B: inner{Name: "x"}}
	if _, err := Document(same, WithRefPrefix("#/$defs/"), WithDefaults(same)); err != nil {
		t.Errorf("the same defaults are one definition: %v", err)
	}
}

func TestReflectorIsDeterministic(t *testing.T) {
	v := withDefaults{Listen: "x", Count: 1}
	var first []byte
	for i := range 20 {
		s, err := Document(tagged{}, WithRefPrefix("#/$defs/"))
		if err != nil {
			t.Fatal(err)
		}
		d, err := Document(v, WithRefPrefix("#/$defs/"), WithDefaults(v))
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal([]Schema{s, d})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
		} else if string(b) != string(first) {
			t.Fatalf("run %d differs:\n%s\n%s", i, b, first)
		}
	}
}

// Two types called Template, from different packages, get package-qualified names; a unique
// name stays short.
func TestReflectorNamesCollidingTypesByPackage(t *testing.T) {
	type holder struct {
		Text *template.Template     `json:"text"`
		HTML *htmltemplate.Template `json:"html"`
		In   inner                  `json:"in"`
	}
	s, err := Document(holder{}, WithRefPrefix("#/$defs/"))
	if err != nil {
		t.Fatal(err)
	}
	props := s["properties"].(Schema)
	// Both packages end in "template", so the full path names them.
	if got := props["text"].(Schema)["$ref"]; got != "#/$defs/text.template.Template" {
		t.Errorf("text ref = %v", got)
	}
	if got := props["html"].(Schema)["$ref"]; got != "#/$defs/html.template.Template" {
		t.Errorf("html ref = %v", got)
	}
	textRef, htmlRef := props["text"].(Schema)["$ref"], props["html"].(Schema)["$ref"]
	if textRef == htmlRef {
		t.Fatalf("both refer to %v", textRef)
	}
	if props["in"].(Schema)["$ref"] != "#/$defs/inner" {
		t.Errorf("a unique name stays short: %v", props["in"])
	}
	defs := s["$defs"].(map[string]Schema)
	for _, ref := range []any{textRef, htmlRef} {
		if _, ok := defs[strings.TrimPrefix(ref.(string), "#/$defs/")]; !ok {
			t.Errorf("no definition for %v in %v", ref, defs)
		}
	}
}

func TestReflectorHandlesRecursiveTypes(t *testing.T) {
	type node struct {
		Name     string  `json:"name"`
		Children []*node `json:"children"`
	}
	s, err := Document(node{}, WithRefPrefix("#/$defs/"))
	if err != nil {
		t.Fatal(err)
	}
	items := s["properties"].(Schema)["children"].(Schema)["items"].(Schema)
	if items["$ref"] != "#/$defs/node" {
		t.Errorf("items = %v", items)
	}
	if _, ok := s["$defs"].(map[string]Schema)["node"]; !ok {
		t.Errorf("the root is defined for the reference: %v", s["$defs"])
	}
	if _, err := Document(node{}); err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Errorf("inlining a recursive type: %v", err)
	}
}

func TestReflectorRejectsWhatItDoesNotKnow(t *testing.T) {
	for name, v := range map[string]any{
		"chan":        struct{ C chan int }{},
		"func":        struct{ F func() }{},
		"int map key": struct{ M map[int]string }{},
		"bad option": struct {
			S string `dash:"nope"`
		}{},
		"enum on bool": struct {
			B bool `dash:"enum=a|b"`
		}{},
		"enum number": struct {
			N int `dash:"enum=x"`
		}{},
	} {
		if _, err := Document(v); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := Document(nil); err == nil {
		t.Error("nil: want an error")
	}
}
