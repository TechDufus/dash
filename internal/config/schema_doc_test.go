package config

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/jsonschema"
)

var update = flag.Bool("update", false, "rewrite testdata/config.schema.json")

// The schema is a published interface (editors, `dash config schema`): a change to it must be
// deliberate. Regenerate with `go test ./internal/config -run TestConfigSchemaGolden -update`.
func TestConfigSchemaGolden(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSchema(&buf); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "config.schema.json")
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("the schema changed; review the diff and run the test with -update\n--- got\n%s", buf.String())
	}
}

// docOf is the doc tag of the field at the dotted path.
func docOf(t *testing.T, l *leaf) string {
	t.Helper()
	return reflect.TypeFor[Tree]().FieldByIndex(l.index).Tag.Get("doc")
}

// Every setting is described: the schema, docs/configuration.md and `dash config env` read it.
func TestEveryLeafHasDoc(t *testing.T) {
	for _, l := range schema.leaves {
		if doc := docOf(t, l); strings.TrimSpace(doc) == "" {
			t.Errorf("%s has no doc tag", l.path)
		} else if !strings.HasSuffix(doc, ".") {
			t.Errorf("%s: doc %q should be a sentence ending in a full stop", l.path, doc)
		}
	}
	for path, doc := range sectionDocs() {
		if strings.TrimSpace(doc) == "" {
			t.Errorf("section %s has no doc tag", path)
		}
	}
}

// at follows a dotted path through a schema document, resolving $ref.
func at(t *testing.T, doc jsonschema.Schema, path string) jsonschema.Schema {
	t.Helper()
	defs, _ := doc["$defs"].(map[string]jsonschema.Schema)
	resolve := func(s jsonschema.Schema) jsonschema.Schema {
		if ref, ok := s["$ref"].(string); ok {
			return defs[strings.TrimPrefix(ref, "#/$defs/")]
		}
		return s
	}
	cur := doc
	for seg := range strings.SplitSeq(path, ".") {
		props, _ := resolve(cur)["properties"].(jsonschema.Schema)
		next, ok := props[seg].(jsonschema.Schema)
		if !ok {
			t.Fatalf("schema has no %s (stopped at %s)", path, seg)
		}
		cur = next
	}
	return cur
}

// The schema describes the tree the resolver reads: every leaf is there, with the flags of its
// dash tag and its default.
func TestSchemaMatchesTheTreeLeaves(t *testing.T) {
	doc, err := SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	if doc["additionalProperties"] != false {
		t.Error("an unknown top-level key must be an error, as it is for the loader")
	}
	defaults := DefaultTree()
	for _, l := range schema.leaves {
		s := at(t, doc, l.path)
		if _, ok := s["x-dash-secret"]; ok != l.secret {
			t.Errorf("%s: x-dash-secret = %v, leaf secret = %v", l.path, ok, l.secret)
		}
		if w, _ := s["writeOnly"].(bool); w != l.secret {
			t.Errorf("%s: writeOnly = %v, leaf secret = %v", l.path, w, l.secret)
		}
		if _, ok := s["x-dash-restart"]; ok != l.restart {
			t.Errorf("%s: x-dash-restart = %v, leaf restart = %v", l.path, ok, l.restart)
		}
		if f, _ := s["format"].(string); (f == "uri") != l.url {
			t.Errorf("%s: format = %q, leaf url = %v", l.path, f, l.url)
		}
		if s["description"] != docOf(t, l) {
			t.Errorf("%s: description = %v", l.path, s["description"])
		}
		if def := l.field(&defaults); !def.IsZero() && !l.secret {
			if _, ok := s["default"]; !ok {
				t.Errorf("%s has the default %v, the schema none", l.path, def)
			}
		}
	}
	if got := at(t, doc, "server.limits.tailMaxDuration"); got["type"] != "string" || got["format"] != "duration" || got["default"] != "30m" {
		t.Errorf("tailMaxDuration = %v", got)
	}
	if got := at(t, doc, "server.listen"); got["default"] != "127.0.0.1:8080" {
		t.Errorf("listen = %v", got)
	}
	if got := at(t, doc, "plugins.proxmox.tokenSecret"); got["type"] != "string" {
		t.Errorf("tokenSecret = %v", got)
	}
}

// A plugin's own schema (Manifest.Config, reflected alone) is the subschema of the tree schema,
// so what the plugin documents is what the file accepts.
func TestPluginConfigSchemaMatchesTreeSubschema(t *testing.T) {
	tree, err := SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	pt := reflect.TypeFor[PluginsConfig]()
	for i := range pt.NumField() {
		f := pt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		t.Run(name, func(t *testing.T) {
			own, err := jsonschema.Document(reflect.New(f.Type).Interface(), jsonschema.WithRefPrefix("#/$defs/"))
			if err != nil {
				t.Fatal(err)
			}
			sub := resolved(tree, at(t, tree, "plugins."+name))
			if got, want := stripDefaults(resolved(own, own)), stripDefaults(sub); !reflect.DeepEqual(got, want) {
				g, _ := json.MarshalIndent(got, "", " ")
				w, _ := json.MarshalIndent(want, "", " ")
				t.Errorf("the plugin's schema differs from plugins.%s:\n%s\nvs\n%s", name, g, w)
			}
		})
	}
}

// resolved is s with its $ref and every nested $ref replaced by the definition in doc, so two
// documents with different $defs compare by meaning. $schema and $defs are dropped.
func resolved(doc, s jsonschema.Schema) jsonschema.Schema {
	defs, _ := doc["$defs"].(map[string]jsonschema.Schema)
	var walk func(v any) any
	walk = func(v any) any {
		switch v := v.(type) {
		case jsonschema.Schema:
			if ref, ok := v["$ref"].(string); ok {
				return walk(defs[strings.TrimPrefix(ref, "#/$defs/")])
			}
			out := jsonschema.Schema{}
			for k, val := range v {
				if k == "$schema" || k == "$defs" {
					continue
				}
				out[k] = walk(val)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i := range v {
				out[i] = walk(v[i])
			}
			return out
		}
		return v
	}
	return walk(s).(jsonschema.Schema)
}

// stripDefaults removes "default" keys: the tree schema carries the built-in values, the plugin
// type alone has none.
func stripDefaults(v any) any {
	switch v := v.(type) {
	case jsonschema.Schema:
		out := jsonschema.Schema{}
		for k, val := range v {
			if k != "default" {
				out[k] = stripDefaults(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = stripDefaults(v[i])
		}
		return out
	}
	return v
}

func TestDurationIsCompactInTheEnvironmentAndTheFile(t *testing.T) {
	for in, want := range map[time.Duration]string{
		30 * time.Minute:              "30m",
		10 * time.Second:              "10s",
		time.Hour:                     "1h",
		90 * time.Minute:              "1h30m",
		1500 * time.Millisecond:       "1.5s",
		time.Hour + 5*time.Second:     "1h0m5s",
		0:                             "0s",
		2*time.Minute + 3*time.Second: "2m3s",
	} {
		if got := Duration(in).compact(); got != want {
			t.Errorf("compact(%v) = %q, want %q", in, got, want)
		}
		b, err := Duration(in).MarshalJSON()
		if err != nil || string(b) != `"`+want+`"` {
			t.Errorf("MarshalJSON(%v) = %s, %v", in, b, err)
		}
		var back Duration
		if err := back.UnmarshalJSON(b); err != nil || back != Duration(in) {
			t.Errorf("round trip of %v: %v, %v", in, back, err)
		}
	}
}

// docs/configuration.md is generated: this fails when a setting, its doc or its default changed
// without `make generate`.
func TestConfigurationDocIsCurrent(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteEnvMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	have, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), have) {
		t.Error("docs/configuration.md is out of date; run `make generate`")
	}
	out := buf.String()
	if !strings.HasPrefix(out, "<!-- Generated by `dash config env --markdown`; do not edit.") {
		t.Errorf("the page must say it is generated:\n%.200s", out)
	}
	for _, v := range EnvVars() {
		if !strings.Contains(out, "`"+v.Name+"`") || !strings.Contains(out, v.Description) {
			t.Errorf("the page lacks %s or its description", v.Name)
		}
	}
	if strings.Contains(out, "|  |") {
		t.Error("an empty table cell")
	}
}

// A section with both fields and sub-sections (plugins.kubernetes) is one heading, with all its
// settings under it.
func TestConfigurationDocHasOneHeadingPerSection(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteEnvMarkdown(&buf); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var current string
	section := map[string]string{} // setting path -> the heading it sits under
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			if seen[h] {
				t.Errorf("heading %q appears twice", h)
			}
			seen[h], current = true, h
		} else if strings.HasPrefix(line, "| `") && current != "" {
			path, _, _ := strings.Cut(strings.TrimPrefix(line, "| `"), "`")
			section[path] = current
		}
	}
	for _, v := range EnvVars() {
		want := "`" + v.Path[:max(strings.LastIndex(v.Path, "."), 0)] + "`"
		if want == "``" {
			want = "Top level"
		}
		if section[v.Path] != want {
			t.Errorf("%s is under %q, want %q", v.Path, section[v.Path], want)
		}
	}
}
