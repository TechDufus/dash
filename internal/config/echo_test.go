package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/TechDufus/dash/internal/secret"
)

// The canaries: a text no rule accepts as a name, a header, a regular expression, a URL, a
// duration or an enum value, and a number outside every range. A message that repeats what it
// rejects carries one of the markers.
const (
	echoText   = "Zq ( canary-ECHO"
	echoMarker = "canary-ECHO"
	echoNumber = 987654321
	echoDigits = "987654321"
)

// echoValues are the values a leaf of type typ gets: each is wrong for most leaves of the type
// and carries a canary. A secret leaf gets a literal and each reference form.
func echoValues(typ reflect.Type, isSecret bool) []any {
	if isSecret {
		return []any{echoText, "k8s:" + echoText, "env:" + echoText, "file:/" + echoText}
	}
	if typ == durationType {
		return []any{echoText, echoDigits, echoNumber}
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return echoValues(typ.Elem(), false)
	case reflect.String, reflect.Bool:
		return []any{echoText}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Float32, reflect.Float64:
		return []any{echoNumber, -echoNumber, echoText}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return []any{echoNumber, echoText}
	case reflect.Slice:
		var out []any
		for _, e := range echoValues(typ.Elem(), false) {
			out = append(out, []any{e, e}) // twice: a rule against repeats sees one too
		}
		return out
	case reflect.Map:
		var out []any
		for _, e := range echoValues(typ.Elem(), false) {
			out = append(out, map[string]any{echoText: e})
		}
		return out
	case reflect.Struct:
		// One object per variant: every field takes its i-th value (or its last).
		fields := map[string][]any{}
		n := 0
		for f := range typ.Fields() {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" || name == "" {
				continue
			}
			fields[name] = echoValues(f.Type, false)
			n = max(n, len(fields[name]))
		}
		out := make([]any, n)
		for i := range n {
			obj := map[string]any{}
			for name, vs := range fields {
				obj[name] = vs[min(i, len(vs)-1)]
			}
			out[i] = obj
		}
		// And one whose key is the canary: an unknown field must not be named either.
		return append(out, map[string]any{echoText: "x"})
	}
	panic(fmt.Sprintf("echo canary: no values for %s", typ))
}

// nested puts v at the dotted path of a sparse tree.
func nested(path string, v any) map[string]any {
	keys := strings.Split(path, ".")
	m := map[string]any{keys[len(keys)-1]: v}
	for i := len(keys) - 2; i >= 0; i-- {
		m = map[string]any{keys[i]: m}
	}
	return m
}

// echoEnvValue is how an operator writes v in the environment: text as it is, the rest as JSON.
func echoEnvValue(t *testing.T, v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// D16, ruling 3: a validation error never repeats the value it rejects. Every leaf of the schema
// gets a canary of its type from the file, the environment and the UI layer (a candidate's, so a
// UI problem is an error with its path), in dev and demo mode; the canary must appear in no error
// text and no FieldErrors entry. A new leaf is covered without touching this test.
//
// One exception: an env: or file: reference of a secret from the file or the environment names
// its target (the variable, the file) in the error text when it cannot be read (internal/secret
// Resolve): the target is where the secret lives, not the secret. Such an error is never a field
// error (ruling 4), which the test still checks.
func TestValidationErrorsNeverEchoValues(t *testing.T) {
	quiet := slog.New(slog.DiscardHandler)
	failures := 0
	for _, l := range schema.leaves {
		for vi, v := range echoValues(l.typ, l.secret) {
			s, _ := v.(string)
			refTarget := l.secret && (strings.HasPrefix(s, "env:") || strings.HasPrefix(s, "file:"))
			for _, mode := range []string{"DASH_DEV=true", "DASH_DEMO=true"} {
				file, err := yaml.Marshal(nested(l.path, v))
				if err != nil {
					t.Fatal(err)
				}
				for layer, src := range map[string]Sources{
					"file": {Environ: []string{mode}, File: &File{Path: "c.yaml", Data: file}},
					"env":  {Environ: []string{mode, l.env + "=" + echoEnvValue(t, v)}},
					"ui":   {Environ: []string{mode}, UI: nested(l.path, v), UIRef: "ui-settings.yaml", strictUI: map[string]any{}},
				} {
					src.Log, src.K8s = quiet, secret.NewK8sRefs(nil)
					_, err := Resolve(src)
					if err == nil {
						continue
					}
					failures++
					var texts []string
					if !refTarget || layer == "ui" {
						texts = append(texts, err.Error())
					}
					for _, fe := range FieldErrors(err) {
						texts = append(texts, fe.Path+": "+fe.Message)
					}
					for _, text := range texts {
						if strings.Contains(text, echoMarker) || strings.Contains(text, echoDigits) {
							t.Errorf("%s, value %d from the %s layer (%s) echoed:\n%s", l.path, vi, layer, mode, text)
						}
					}
				}
			}
		}
	}
	// The canaries must reach the validators, or the test proves nothing.
	if failures == 0 {
		t.Fatal("no canary was rejected")
	}
}
