package settings

import (
	"bytes"
	"encoding/json"
	"fmt"

	"sigs.k8s.io/yaml"
)

// document is the on-disk shape of ui-settings.yaml. The sparse values live under their own key
// so envelope fields can never collide with a config section name.
type document struct {
	SchemaVersion int            `json:"schemaVersion"`
	Revision      uint64         `json:"revision"`
	Values        map[string]any `json:"values,omitempty"`
}

// migration upgrades a decoded document one schema version: migrations[i] turns version i+1
// into i+2. It receives the whole document (not just values) so a future version may change the
// envelope too. Decoding sets schemaVersion after each step.
type migration func(doc map[string]any) error

// migrations is the ordered list of upgrades to the current schema. Version 1 is the first
// release, so there is nothing to migrate yet; the first format change appends here and the
// current version follows from the length.
var migrations []migration

const header = "# Managed by dash. It holds only the values saved from the settings UI, never the effective\n" +
	"# configuration; operator settings belong in config.yaml or the environment.\n"

func currentVersion(ms []migration) int { return len(ms) + 1 }

// decode parses data, upgrading older schema versions with ms. Empty data is an empty store, so
// a freshly touched file is not an error.
func decode(data []byte, ms []migration) (document, error) {
	cur := currentVersion(ms)
	if len(bytes.TrimSpace(data)) == 0 {
		return document{SchemaVersion: cur, Values: map[string]any{}}, nil
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return document{}, fmt.Errorf("parse: %w", err)
	}
	v, ok := raw["schemaVersion"].(float64)
	if !ok || v != float64(int(v)) || v < 1 {
		return document{}, fmt.Errorf("schemaVersion is missing or not a positive integer")
	}
	ver := int(v)
	if ver > cur {
		return document{}, fmt.Errorf("schemaVersion %d is newer than this dash understands (%d); upgrade dash or restore a backup", ver, cur)
	}
	for ; ver < cur; ver++ {
		if err := ms[ver-1](raw); err != nil {
			return document{}, fmt.Errorf("migrate schemaVersion %d to %d: %w", ver, ver+1, err)
		}
		raw["schemaVersion"] = float64(ver + 1)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return document{}, err
	}
	var doc document
	if err := json.Unmarshal(b, &doc); err != nil {
		return document{}, fmt.Errorf("values: %w", err)
	}
	if doc.Values == nil {
		doc.Values = map[string]any{}
	}
	return doc, nil
}

func encode(doc document) ([]byte, error) {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), b...), nil
}
