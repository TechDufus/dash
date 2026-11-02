package hygiene

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// denylist is the epic's list of literals that name one lab and so must not be in code that ships.
// Every term scans everywhere: there is no allowlist and no switch, so a literal that a leaf
// removed cannot come back. (fluxcd.io is not on the list: the Flux API groups are public names.)
var denylist = []struct {
	term  string
	owner string // the leaf that removed it
}{
	{"mac-mini", "P3.7"},
	{"rpi-control", "P3.7"},
	{"2020 && vmid", "P3.7"}, // the old "critical VM" rule: vmid >= 2020 && vmid <= 2022
	{"flux-system", "P3.11"},
	{"gateway/home", "P3.9"},
}

// hit is one occurrence of a denied term.
type hit struct {
	file string // slash-separated, relative to the scanned root
	line int
	term string
}

func (h hit) String() string { return fmt.Sprintf("%s:%d: %q", h.file, h.line, h.term) }

// scanned says whether the file at rel (slash-separated, relative to the root) is code that ships:
// Go files other than tests, and the web app's sources other than its tests and its test helpers.
// Test code names whatever it needs to prove a neutral fixture neutral; testdata is a fixture
// and does not ship either.
func scanned(rel string) bool {
	parts := strings.Split(rel, "/")
	if slices.Contains(parts, "testdata") || slices.Contains(parts, "node_modules") {
		return false
	}
	switch ext := filepath.Ext(rel); {
	case ext == ".go":
		return !strings.HasSuffix(rel, "_test.go")
	case strings.HasPrefix(rel, "web/src/"):
		if strings.HasPrefix(rel, "web/src/test/") {
			return false
		}
		base := filepath.Base(rel)
		return !strings.HasSuffix(base, ".test.ts") && !strings.HasSuffix(base, ".test.tsx")
	}
	return false
}

// scan walks root and lists every active denied term in the files that ship, with no allowlist:
// a hit is renamed to something neutral, never excused.
func scan(root string) ([]hit, error) {
	var hits []hit
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "testdata" || d.Name() == "dist" && strings.HasPrefix(rel, "web/") {
				return fs.SkipDir
			}
			return nil
		}
		if !scanned(rel) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, d := range denylist {
				if strings.Contains(line, d.term) {
					hits = append(hits, hit{rel, i + 1, d.term})
				}
			}
		}
		return nil
	})
	return hits, err
}

// repoRoot is the repository's root, found from this file's own place in it.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repository root: %v", root, err)
	}
	return root
}

// No code that ships names one lab (the epic's hygiene rule): the demo fixtures are scanned too
// and must stay fake.
func TestNoHomelabLiterals(t *testing.T) {
	hits, err := scan(repoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("a lab's name in code that ships: %s", h)
	}
}

// The scan itself: it finds an active term wherever shipped code is and skips what is not shipped.
func TestHomelabScanCatchesALeakAndSkipsWhatDoesNotShip(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Shipped code that leaks.
	write("internal/x/x.go", "package x\n\nconst a = \"ok\"\nconst b = \"mac-mini\"\n")
	write("internal/demo/fixtures.go", "package demo\n\nvar m = \"rpi-control SSH\"\n")
	write("cmd/dash/z.go", "package main\n\nfunc f(vmid int) bool { return vmid >= 2020 && vmid <= 2022 }\n")
	write("web/src/pages/p.tsx", "const n = 'mac-mini';\n")
	write("web/src/api/a.ts", "export const x = 1;\n")
	// Code that does not ship.
	write("internal/x/x_test.go", "package x\n\nconst c = \"mac-mini\"\n")
	write("internal/x/testdata/data.go", "package data\n\nconst c = \"mac-mini\"\n")
	write("web/src/pages/p.test.tsx", "const n = 'mac-mini';\n")
	write("web/src/api/a.test.ts", "const n = 'rpi-control';\n")
	write("web/src/test/shims.ts", "const n = 'rpi-control';\n")
	write("web/node_modules/m/index.js", "mac-mini\n")
	write("docs/notes.md", "mac-mini and rpi-control\n")
	write("internal/flux/f.go", "package flux\n\nconst ns = \"flux-system\"\nconst gw = \"gateway/home\"\nconst group = \"kustomize.toolkit.fluxcd.io\"\n")

	hits, err := scan(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.String())
	}
	slices.Sort(got)
	want := []string{
		`cmd/dash/z.go:3: "2020 && vmid"`,
		`internal/demo/fixtures.go:3: "rpi-control"`,
		`internal/flux/f.go:3: "flux-system"`,
		`internal/flux/f.go:4: "gateway/home"`,
		`internal/x/x.go:4: "mac-mini"`,
		`web/src/pages/p.tsx:1: "mac-mini"`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("hits =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
