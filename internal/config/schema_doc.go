package config

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/jsonschema"
)

// DefaultTree is the configuration with nothing set, outside dev and demo: the built-in layer
// that every other layer is applied to. The schema and the generated documentation take their
// defaults from it.
func DefaultTree() Tree { return defaults(false, false) }

// JSONSchema makes a Duration a string with format "duration" in the generated schema.
func (Duration) JSONSchema() jsonschema.Schema {
	return jsonschema.Schema{"type": "string", "format": "duration", "examples": []string{"30s", "5m", "1h30m"}}
}

// compact writes d as an operator would: 30m, not 30m0s.
func (d Duration) compact() string {
	s := time.Duration(d).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

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
