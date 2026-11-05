package config

import (
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// settings.secretNamespaces is a list of namespace names; a bad entry is named by its position,
// never its text. The UI layer cannot set it.
func TestSecretNamespacesAreValidated(t *testing.T) {
	env := func(v string) Sources {
		return Sources{Environ: []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_SETTINGS_SECRET_NAMESPACES=" + v}, Log: slog.New(slog.DiscardHandler)}
	}
	res, err := Resolve(env(`["monitoring","kube-system"]`))
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Tree.Settings.SecretNamespaces; !slices.Equal(got, []string{"monitoring", "kube-system"}) {
		t.Errorf("namespaces = %v", got)
	}
	const canary = "Bad_Namespace_Canary"
	_, err = Resolve(env(`["ok","` + canary + `"]`))
	want := []FieldError{{Path: "settings.secretNamespaces", Message: "entry 2 is not a Kubernetes namespace name"}}
	if got := FieldErrors(err); !reflect.DeepEqual(got, want) || strings.Contains(err.Error(), canary) {
		t.Errorf("err = %v, field errors %v", err, got)
	}

	src := env(`[]`)
	src.UI = map[string]any{"settings": map[string]any{"secretNamespaces": []any{"kube-system"}}}
	src.strictUI = map[string]any{}
	_, err = Resolve(src)
	if got := FieldErrors(err); len(got) != 1 || got[0].Path != "settings.secretNamespaces" || !uiIneligible("settings.secretNamespaces") {
		t.Errorf("a UI value: %v", err)
	}
}
