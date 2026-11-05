package config

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"

	"github.com/TechDufus/dash/internal/jsonschema"
)

// DefaultTree is the configuration with nothing set, outside dev and demo: the built-in layer
// that every other layer is applied to. The schema and the generated documentation take their
// defaults from it.
func DefaultTree() Tree { return defaults(false, false) }

// SchemaDocument is the JSON Schema 2020-12 of the configuration file (config.yaml, and the
// UI settings layer): every setting with its description, its default, whether it is a secret
// and whether a change needs a restart. The environment variables are the same paths in
// UPPER_SNAKE case (`dash config env`).
func SchemaDocument() (jsonschema.Schema, error) {
	d := DefaultTree()
	doc, err := jsonschema.Document(d, jsonschema.WithRefPrefix("#/$defs/"), jsonschema.WithDefaults(d))
	if err != nil {
		return nil, err
	}
	doc["title"] = "dash configuration"
	doc["description"] = "The dash config file. The environment overrides it, setting by setting: DASH_ plus the path in UPPER_SNAKE case."
	return doc, nil
}

// PluginSchemaDocument is the JSON Schema 2020-12 of the section plugins.<id> alone: the
// document the settings form renders (SettingsPluginView.Schema), with its nested types as $ref
// into its own $defs and the built-in values as defaults. It describes what the tree schema
// describes at plugins.<id>. ok is false for an id that has no section.
func PluginSchemaDocument(id string) (doc jsonschema.Schema, ok bool, err error) {
	pt := reflect.TypeFor[PluginsConfig]()
	for i := range pt.NumField() {
		if name, _, _ := strings.Cut(pt.Field(i).Tag.Get("json"), ","); name != id {
			continue
		}
		def := reflect.ValueOf(DefaultTree().Plugins).Field(i).Interface()
		doc, err := jsonschema.Document(def, jsonschema.WithRefPrefix("#/$defs/"), jsonschema.WithDefaults(def))
		return doc, true, err
	}
	return nil, false, nil
}

// WriteSchema writes SchemaDocument as indented JSON, the form `dash config schema` prints and
// testdata/config.schema.json holds.
func WriteSchema(w io.Writer) error {
	doc, err := SchemaDocument()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // descriptions say <prefix>
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
