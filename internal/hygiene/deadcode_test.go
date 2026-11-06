package hygiene

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The binary links neither text/template nor html/template. Their reflect.MethodByName call
// keeps every exported method of every type reachable, which turns off the linker's dead-code
// elimination: linking either one grew dash by about 27 MB (G7-R5). Render text with
// strings.Replacer or a hand-written writer instead.
func TestBinaryLinksNoTemplatePackage(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		goBin = "go"
	}
	cmd := exec.Command(goBin, "list", "-deps", "./cmd/dash")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps ./cmd/dash: %v", err)
	}
	deps := strings.Fields(string(out))
	if !slices.Contains(deps, "github.com/TechDufus/dash/internal/mcp") {
		t.Fatalf("the dependency list lacks internal/mcp; is this the dash module? %d packages", len(deps))
	}
	for _, pkg := range []string{"text/template", "html/template"} {
		if slices.Contains(deps, pkg) {
			t.Errorf("./cmd/dash links %s: its reflect.MethodByName turns off dead-code elimination (+27 MB binary); "+
				"use strings.Replacer instead (`go mod why %s` names the importer)", pkg, pkg)
		}
	}
}
