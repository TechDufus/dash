package hygiene

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// notes runs scripts/release/notes.sh, the script CI's github-release job publishes a release
// with, and returns its output without the final newline.
func notes(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command(filepath.Join(root, "scripts", "release", "notes.sh"), args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return strings.TrimSuffix(string(out), "\n"), err
}

// The chart's version has release notes: a version bump that forgets its CHANGELOG.md section
// fails here, in make check, and not after the tag has published the image.
func TestChartVersionHasReleaseNotes(t *testing.T) {
	for _, mode := range []string{"title", "body"} {
		if out, err := notes(t, mode); err != nil || out == "" {
			t.Errorf("notes.sh %s for the version of charts/dash/Chart.yaml: %q, %v; add its `## vX.Y.Z: headline` section to CHANGELOG.md", mode, out, err)
		}
	}
}

// The notes of a version are its own section and nothing else: not a version it is a prefix of,
// not the next section, and a `## ` line in a code block stays in them. A version that is
// missing, listed twice or empty is refused, so a release is never published without notes.
func TestReleaseNotesAreTheVersionsSection(t *testing.T) {
	file := filepath.Join(t.TempDir(), "CHANGELOG.md")
	changelog := strings.Join([]string{
		"# Changelog", "", "## not a version", "",
		"## Unreleased", "", "- not yet", "",
		"## v1.2.10: ten", "", "- the tenth patch", "",
		"## v1.2.1: one", "", "", "First line.", "", "```sh", "## not a heading", "```", "", "### Fixed", "", "- a fix", "", "",
		"## v1.2.0", "", "- no headline", "",
		"## v1.1.0: empty", "", "  ", "",
		"## v1.0.0: twice", "", "- a", "",
		"## v1.0.0: twice", "", "- b", "",
	}, "\n")
	if err := os.WriteFile(file, []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ mode, version, want string }{
		{"title", "1.2.1", "v1.2.1: one"},
		{"body", "1.2.1", "First line.\n\n```sh\n## not a heading\n```\n\n### Fixed\n\n- a fix"},
		{"title", "1.2.10", "v1.2.10: ten"},
		{"body", "1.2.10", "- the tenth patch"},
		{"title", "1.2.0", "v1.2.0"},
		{"body", "1.2.0", "- no headline"},
	} {
		if got, err := notes(t, c.mode, c.version, file); err != nil || got != c.want {
			t.Errorf("notes.sh %s %s = %q, %v; want %q", c.mode, c.version, got, err, c.want)
		}
	}

	for _, c := range []struct{ version, why string }{
		{"9.9.9", "no section"},
		{"1.2", "not X.Y.Z"},
		{"1.1.0", "a section without notes"},
		{"1.0.0", "two sections"},
	} {
		for _, mode := range []string{"title", "body"} {
			if got, err := notes(t, mode, c.version, file); err == nil {
				t.Errorf("notes.sh %s %s (%s) succeeded with %q; want a refusal", mode, c.version, c.why, got)
			}
		}
	}
}
