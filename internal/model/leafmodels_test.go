package model

import (
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// nonStdImports lists "<file>: <import>" for every import of the Go files that is not the
// standard library (whose first path element has no dot).
func nonStdImports(t *testing.T, files []string) []string {
	t.Helper()
	var out []string
	for _, f := range files {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, imp := range af.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
				out = append(out, fmt.Sprintf("%s: %s", f, path))
			}
		}
	}
	return out
}

// A plugin's model package (internal/plugins/<id>/model) holds payload types that only that
// plugin publishes. TopicTypes, in this package, names the payload type of every topic, so it
// imports each of them: a plugin model package that imported internal/model (or anything else
// of dash) would be an import cycle, and one that imported a third-party package would drag
// that into the OpenAPI generator. So they may import the standard library only.
func TestPluginModelPackagesAreImportLeaves(t *testing.T) {
	files, err := filepath.Glob("../plugins/*/model/*.go")
	if err != nil {
		t.Fatal(err)
	}
	pkgs := map[string]bool{}
	for _, f := range files {
		pkgs[filepath.Base(filepath.Dir(filepath.Dir(f)))] = true
	}
	for _, bad := range nonStdImports(t, files) {
		t.Errorf("a plugin model package may import the standard library only: %s", bad)
	}
	// The packages this rule exists for are there, so an empty glob cannot pass it.
	for _, want := range []string{"proxmox", "hardware"} {
		if !pkgs[want] {
			t.Errorf("no model package found for %s (found %v)", want, slices.Sorted(maps.Keys(pkgs)))
		}
	}
}

// The check itself rejects what it is for: another package of dash and a third-party package,
// while the standard library passes.
func TestLeafCheckRejectsImports(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ok := write("ok.go", "package m\nimport (\n\t\"encoding/json\"\n\t\"time\"\n)\nvar _ = json.Marshal\nvar _ time.Time\n")
	core := write("core.go", "package m\nimport \"github.com/TechDufus/dash/internal/model\"\nvar _ model.Health\n")
	third := write("third.go", "package m\nimport _ \"k8s.io/api/core/v1\"\n")
	if got := nonStdImports(t, []string{ok}); len(got) != 0 {
		t.Errorf("the standard library was rejected: %v", got)
	}
	got := nonStdImports(t, []string{core, third})
	if len(got) != 2 || !strings.HasSuffix(got[0], "github.com/TechDufus/dash/internal/model") || !strings.HasSuffix(got[1], "k8s.io/api/core/v1") {
		t.Errorf("violations = %v", got)
	}
}
