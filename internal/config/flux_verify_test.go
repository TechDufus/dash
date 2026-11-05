package config

import (
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// plugins.flux.verify.sources is read strictly like every list of objects: an entry with an
// unknown field is a load error that names the path, from the file and from the environment.
func TestFluxVerifySourcesAreDecodedStrictly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		environ []string
		file    string
	}{
		{"file", nil, "plugins:\n  flux:\n    verify:\n      sources: [{kind: GitRepository, namespace: a, nme: b}]\n"},
		{"environment", []string{`DASH_PLUGINS_FLUX_VERIFY_SOURCES=[{"kind":"GitRepository","namespace":"a","nme":"b"}]`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := Sources{Environ: append([]string{"DASH_DEV=true"}, tc.environ...)}
			if tc.file != "" {
				src.File = &File{Path: "/etc/dash/config.yaml", Data: []byte(tc.file)}
			}
			_, err := Resolve(src)
			if err == nil {
				t.Fatal("want a load error for the unknown field")
			}
			for _, w := range []string{"plugins.flux.verify.sources", "has a field this setting does not know"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

// The pins reach the tree, and what Validate rejects stops the load with the setting's path.
func TestFluxVerifySourcesValidation(t *testing.T) {
	const good = `{"kind":"GitRepository","namespace":"gitops","name":"cluster"}`
	res, err := Resolve(Sources{Environ: []string{"DASH_DEV=true", "DASH_PLUGINS_FLUX_VERIFY_SOURCES=[" + good + `,{"kind":"OCIRepository","namespace":"gitops","name":"charts"}]`}})
	if err != nil {
		t.Fatal(err)
	}
	want := []pluginconf.VerifySource{
		{Kind: "GitRepository", Namespace: "gitops", Name: "cluster"},
		{Kind: "OCIRepository", Namespace: "gitops", Name: "charts"},
	}
	if got := res.Tree.Plugins.Flux.Verify.Sources; !slices.Equal(got, want) {
		t.Errorf("sources = %+v, want %+v", got, want)
	}
	if !res.Tree.Plugins.Flux.Verify.RequireWhenSpecVerify {
		t.Error("requireWhenSpecVerify is not true by default: defaults() lacks DefaultFlux()")
	}

	for _, tc := range []struct{ name, list, want string }{
		{"a kind that cannot be verified", `[{"kind":"HelmRepository","namespace":"a","name":"b"}]`, "entry 1: kind must be one of: GitRepository, OCIRepository"},
		{"an empty kind", `[{"namespace":"a","name":"b"}]`, "entry 1: kind must be one of: GitRepository, OCIRepository"},
		{"an empty namespace", `[{"kind":"GitRepository","name":"b"}]`, "needs both namespace and name"},
		{"an empty name", `[{"kind":"GitRepository","namespace":"a"}]`, "needs both namespace and name"},
		{"a duplicate", "[" + good + "," + good + "]", "entry 2 repeats entry 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Sources{Environ: []string{"DASH_DEV=true", "DASH_PLUGINS_FLUX_VERIFY_SOURCES=" + tc.list}})
			if err == nil {
				t.Fatal("want a validation error")
			}
			for _, w := range []string{"plugins.flux.verify.sources", tc.want} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}
