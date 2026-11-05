package api

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
)

// exportSettings answers GET /api/v1/settings/export: what the UI store holds, as a config.yaml
// snippet an operator can move into GitOps. A secret stored encrypted is never in it, not even
// as ciphertext: a comment names its path instead. A k8s: reference is no secret and is kept.
// Every value is written as JSON, which YAML reads as it is.
func (s *Server) exportSettings(w http.ResponseWriter, r *http.Request) {
	b, ok := s.settingsBackend(w)
	if !ok {
		return
	}
	// The revision and the values are read under one lock: the revision names these values.
	var (
		rev    uint64
		stored map[string]any
	)
	_ = b.WithLock(func() error {
		rev, stored = b.Revision(), b.Stored()
		return nil
	})
	var out bytes.Buffer
	out.WriteString("# dash settings saved from the UI, revision " + strconv.FormatUint(rev, 10) + ".\n")
	out.WriteString("# Merge them into config.yaml or the environment to manage them outside dash.\n")
	// A URL's userinfo is never exported, as no view shows it (one stored before the UI refused
	// it, or written to the store by hand).
	writeYAMLMap(&out, config.RedactURLs(stored).(map[string]any), "", 0)
	h := w.Header()
	h.Set("Content-Type", "text/yaml; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="dash-settings.yaml"`)
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}

// writeYAMLMap writes m as block YAML at depth, its keys sorted; prefix is the dotted path of m.
func writeYAMLMap(out *bytes.Buffer, m map[string]any, prefix string, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, k := range slices.Sorted(maps.Keys(m)) {
		path := prefix + k
		key, _ := json.Marshal(k) // a quoted key reads as the same string in YAML
		switch v := m[k].(type) {
		case map[string]any:
			out.WriteString(indent + string(key) + ":\n")
			writeYAMLMap(out, v, path+".", depth+1)
		case string:
			if secret.IsEncrypted(v) {
				out.WriteString(indent + "# " + path + ": secret stored in dash; supply env:NAME or file:/path here\n")
				continue
			}
			writeYAMLLeaf(out, indent, key, v)
		default:
			writeYAMLLeaf(out, indent, key, v)
		}
	}
}

func writeYAMLLeaf(out *bytes.Buffer, indent string, key []byte, v any) {
	val, err := json.Marshal(v)
	if err != nil {
		return
	}
	out.WriteString(indent + string(key) + ": " + string(val) + "\n")
}
