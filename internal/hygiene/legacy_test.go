package hygiene

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The legacy topic Manager is gone and stays gone: internal/topics does not exist, and the
// overview publisher that replaced it knows no plugin, so it composes only what the framework's
// contributors hand it (design D15).
func TestNoLegacyPackages(t *testing.T) {
	root := repoRoot(t)

	if _, err := os.Stat(filepath.Join(root, "internal", "topics")); !os.IsNotExist(err) {
		t.Errorf("internal/topics exists (stat err = %v): the legacy Manager was removed in P3.13", err)
	}

	dir := filepath.Join(root, "internal", "overview")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("internal/overview: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			// Test files may stand up real plugins to drive the publisher; the code that ships may not.
			if strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			if strings.Contains(path, "/internal/plugins/") || strings.HasSuffix(path, "/internal/plugins") {
				t.Errorf("internal/overview/%s imports %s: the publisher composes contributors, it names no plugin", e.Name(), path)
			}
		}
	}
	if checked == 0 {
		t.Error("internal/overview has no Go files")
	}
}
