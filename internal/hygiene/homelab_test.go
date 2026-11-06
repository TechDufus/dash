package hygiene

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// j joins its parts. Every term, sample and allowlisted form in this file is split with it, so
// the file never contains a denied literal and the repo-wide scan does not trip on itself.
func j(parts ...string) string { return strings.Join(parts, "") }

// term is one denied lab identifier: a case-insensitive pattern, and an optional scope that
// limits it to some files (nil scans every file).
type term struct {
	name  string
	re    *regexp.Regexp
	scope func(rel string) bool
}

// re compiles a case-insensitive pattern; sub matches s literally, anywhere.
func re(pattern string) *regexp.Regexp { return regexp.MustCompile(`(?i)` + pattern) }
func sub(s string) *regexp.Regexp      { return re(regexp.QuoteMeta(s)) }

// sep is an optional separator between the parts of a name (a hyphen, an underscore, a space or
// nothing); sep1 is a required hyphen or underscore.
const (
	sep  = `[-_ ]?`
	sep1 = `[-_]`
)

// denylist names one lab: design G.2, the epic's P6.1/P6.2/P6.6 strings and a few low-sensitivity
// names seen in the history. Short terms use word boundaries so that, say, "unassigned" stays
// clean. Names allow the usual separator variants.
//
// The list holds only identifiers that are already public through the history scrub list or are
// generic enough to name: never Wi-Fi network names, the lab's other machine names or the owner's
// other project domains, since a public test would itself publish them.
var denylist = []term{
	{name: j("tech", "dufus"), re: sub(j("tech", "dufus"))},
	{name: j("de", "garmo"), re: sub(j("de", "garmo"))},
	{name: j("10.0", ".20."), re: re(`\b` + regexp.QuoteMeta(j("10.0", ".20.")))},
	{name: j("02:0a", ":00:14"), re: re(j("02", "[-:]0a", "[-:]00", "[-:]14"))},
	{name: j("pve", "-730"), re: re(j("pve", sep, "730"))},
	{name: j("talos", "-cp"), re: re(j("talos", sep, "cp"))},
	{name: j("talos", "-vip"), re: re(j("talos", sep, "vip"))},
	{name: j("yah", "wai"), re: sub(j("yah", "wai"))},
	{name: j("registry", ".home"), re: sub(j("registry", ".home"))},
	{name: j("omp", "-cloud"), re: re(j("omp", sep, "cloud"))},
	{name: j("mac", "-mini"), re: re(j("mac", sep, "mini"))},
	{name: j("rpi", "-control"), re: re(j("rpi", sep, "control"))},
	{name: j("e5", "-2683"), re: sub(j("e5", "-2683"))},
	{name: j("fast", "-zfs"), re: re(j("fast", sep1, "zfs"))},
	{name: j("csi", "@pve"), re: sub(j("csi", "@pve"))},
	{name: j("homelab", " guardrail"), re: sub(j("homelab", " guardrail"))},
	{name: j("homelab", ".io/"), re: sub(j("homelab", ".io/"))},
	{name: j("homelab", "_zfs_scrub"), re: sub(j("homelab", "_zfs_scrub"))},
	{name: j("homelab", "-cluster"), re: sub(j("homelab", "-cluster"))},
	{name: j("r7", "30"), re: re(`\b` + j("r7", "30"))}, // with a suffix such as xd too
	{name: j("power", "edge"), re: sub(j("power", "edge"))},
	{name: j("id", "rac-"), re: sub(j("id", "rac-"))},
	{name: j("udm", "-pro"), re: re(j("udm", sep, "pro"))},
	{name: j("tailscale", "-router"), re: re(j("tailscale", sep, "router"))},
	{name: j("nas", "-backup"), re: re(j("nas", sep, "backup"))},
	{name: j("un", "as"), re: re(`\b` + j("un", "as") + `(?:` + sep + `pro)?\b`)}, // the Pro model too
	{name: j("github-runner", "-1"), re: re(j("github", sep1, "runner", sep1, "1"))},
	{name: j("hl", "-op"), re: re(`\b` + j("hl", sep1, "op") + `\b`)},
	{name: j("../home", "lab"), re: sub(j("../home", "lab"))},
	{name: j("op", "://"), re: re(`\b` + regexp.QuoteMeta(j("op", "://")))},
	{name: j("2020 && ", "vmid"), re: sub(j("2020 && ", "vmid"))}, // the old "critical VM" rule
	{name: j("gateway", "/home"), re: sub(j("gateway", "/home"))},
	// The Flux namespace is a generic name that docs and tests legitimately use; it stays denied
	// where P3.11 removed it, in code that ships.
	{name: j("flux", "-system"), re: sub(j("flux", "-system")), scope: shipped},
}

// allow excuses one form of a term: in files matching path (a filepath.Match pattern on the
// slash-separated path; "*" alone means every file), every match of form is blanked from the line
// before the term is looked for. Anything else on the same line still hits.
type allow struct {
	path   string
	term   string
	form   *regexp.Regexp
	reason string
}

var owner = j("Tech", "Dufus")

var allowlist = []allow{
	// After the repository name: a path, a .git suffix, a closing quote, bracket or backtick, a
	// comma, an anchor, or a full stop that ends the sentence; never a longer name (dash-x,
	// dashboard) or another host (dash.example).
	{"*", j("tech", "dufus"), regexp.MustCompile(`(?i)github\.com/` + owner + "/dash(?:\\.git\\b|\\.(?:\\s|$)|[/\"'`),#]|\\s|$)"),
		"D4: the module path and the project's repository URLs"},
	{"*", j("tech", "dufus"), regexp.MustCompile(`(?i)ghcr\.io/` + owner + `/`),
		"D4: the published image and chart on GHCR"},
	{"*", j("tech", "dufus"), regexp.MustCompile(`(?i)--owner ` + owner + `\b`),
		"D4: the account that signs the published image's attestations"},
	{"*", j("tech", "dufus"), regexp.MustCompile(`(?i)io\.github\.` + owner + `\.dash\b`),
		"D4: the Android application ID"},
	{"LICENSE", j("tech", "dufus"), regexp.MustCompile(regexp.QuoteMeta(j("Matthew De", "Garmo (", owner, ")"))),
		"D4: the copyright holder (J.1)"},
	{"LICENSE", j("de", "garmo"), regexp.MustCompile(regexp.QuoteMeta(j("Matthew De", "Garmo (", owner, ")"))),
		"D4: the copyright holder (J.1)"},
	{".github/CODEOWNERS", j("tech", "dufus"), regexp.MustCompile(`@` + owner + `\b`),
		"D4: the code owner"},
	{"internal/secret/resolve.go", j("op", "://"), regexp.MustCompile(regexp.QuoteMeta(j(`strings.HasPrefix(raw, "op`, `://")`))),
		"dash refuses 1Password references by their scheme"},
	{"internal/secret/resolve.go", j("op", "://"), regexp.MustCompile(regexp.QuoteMeta(j("%s: op", ":// references are not built in"))),
		"the error that refusal returns"},
	{"internal/secret/secret_test.go", j("op", "://"), regexp.MustCompile(regexp.QuoteMeta(j(`raw: "op`, `://vault/item/field"`))),
		"the test of that refusal, with a neutral reference"},
	{"Makefile", j("hl", "-op"), regexp.MustCompile(`\b` + j("HL", "_OP") + `\b`),
		"P6.2: dev-api-op's broker variable, which the contract names and which has no default"},
	{"docs/development.md", j("hl", "-op"), regexp.MustCompile(`\b` + j("HL", "_OP") + `\b`),
		"P6.2: the development guide documents that variable"},
	{"dev/secrets.env.example", j("hl", "-op"), regexp.MustCompile(`\b` + j("HL", "_OP") + `\b`),
		"P6.2: the example names that variable"},
	{"*", j("nas", "-backup"), regexp.MustCompile(`\bDASH_` + j("NAS", "_BACKUP") + `_(?:NAMESPACE|CRONJOB)\b`),
		"removed variable names that dash still recognises and refuses with their replacement"},
	{"site/index.html", j("tech", "dufus"), regexp.MustCompile(`(?:github\.com/|© 2026 |Built by )` + owner + `["<]`),
		"the landing page's copyright line and its link to the owner's GitHub profile"},
	{"site/index.html", j("tech", "dufus"), regexp.MustCompile(`https://dash\.` + j("tech", "dufus") + `\.com/`),
		"the landing page's own public address, which link previews need as an absolute URL"},
}

// shipped says whether the file at rel is code that ships: Go files other than tests, and the web
// app's sources other than its tests and test helpers; never testdata or node_modules.
func shipped(rel string) bool {
	parts := strings.Split(rel, "/")
	if slices.Contains(parts, "testdata") || slices.Contains(parts, "node_modules") {
		return false
	}
	switch {
	case filepath.Ext(rel) == ".go":
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

// hit is one occurrence of a denied term.
type hit struct {
	file string // slash-separated, relative to the scanned root
	line int
	term string
}

func (h hit) String() string { return fmt.Sprintf("%s:%d: %q", h.file, h.line, h.term) }

// scan reads each file of files under root and lists every denied term that no allowlist entry
// excuses. Files are read as bytes, so invalid UTF-8 is still scanned; only a file with a NUL byte
// counts as binary and is skipped. Directories (gitlinks) and files missing from the worktree are
// skipped too.
func scan(root string, files []string) ([]hit, error) {
	var hits []hit
	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		lines := strings.Split(string(data), "\n")
		for _, t := range denylist {
			if t.scope != nil && !t.scope(rel) {
				continue
			}
			var forms []*regexp.Regexp
			for _, a := range allowlist {
				if ok, _ := filepath.Match(a.path, rel); a.term == t.name && (a.path == "*" || ok) {
					forms = append(forms, a.form)
				}
			}
			for i, line := range lines {
				for _, f := range forms {
					line = f.ReplaceAllString(line, "")
				}
				if t.re.MatchString(line) {
					hits = append(hits, hit{rel, i + 1, t.name})
				}
			}
		}
	}
	return hits, nil
}

// walkFiles lists every file under root, slash-separated, for a tree that is not a git checkout
// (a source tarball). It skips .git, node_modules and the web build output.
func walkFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || rel == "web/dist" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	return files, err
}

// git runs git in root with the hook variables removed: a git hook exports GIT_DIR and friends,
// which would point -C at the hook's repository. It returns stdout, and on failure an error that
// carries git's stderr.
func git(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_DIR=") || strings.HasPrefix(kv, "GIT_WORK_TREE=") ||
			strings.HasPrefix(kv, "GIT_INDEX_FILE=")
	})
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitFiles lists the files git tracks in root, or says why it cannot: git fails (no git, no
// checkout), root is not the top of its checkout (a source tarball unpacked inside another
// repository), or git tracks nothing there.
func gitFiles(root string) ([]string, error) {
	top, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return nil, err
	}
	if filepath.Clean(got) != filepath.Clean(want) {
		return nil, fmt.Errorf("%s is inside the checkout %s, not its top", want, got)
	}
	out, err := git(root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) == 1 && files[0] == "" {
		return nil, errors.New("git ls-files listed nothing")
	}
	return files, nil
}

// listFiles is what the scan covers: git's tracked files, or, when git cannot list them, every
// file of the tree. note says which, with git's reason.
func listFiles(root string) (files []string, note string, err error) {
	files, gitErr := gitFiles(root)
	if gitErr == nil {
		return files, "git ls-files", nil
	}
	files, err = walkFiles(root)
	note = fmt.Sprintf("git cannot list the files (%v); scanned every file of the tree instead", gitErr)
	return files, note, err
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

// No tracked text file names the lab (P6.1), outside the allowlist.
func TestNoLabIdentifiers(t *testing.T) {
	root := repoRoot(t)
	files, note, err := listFiles(root)
	if err != nil {
		t.Fatalf("%s: %v", note, err)
	}
	t.Log(note)
	if len(files) < 100 {
		t.Fatalf("%s listed %d files; the scan would prove nothing", note, len(files))
	}
	hits, err := scan(root, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("lab identifier: %s", h)
	}
}

// The allowlist only holds entries that excuse something: each has a reason and a known term.
func TestAllowlistEntriesAreExplained(t *testing.T) {
	for _, a := range allowlist {
		if a.reason == "" || !slices.ContainsFunc(denylist, func(d term) bool { return d.name == a.term }) {
			t.Errorf("allowlist entry %s/%q: no reason or unknown term", a.path, a.term)
		}
	}
}

// tree is a temp directory the self-tests write files into.
type tree struct {
	t     *testing.T
	root  string
	files []string
}

func newTree(t *testing.T) *tree { return &tree{t: t, root: t.TempDir()} }

func (tr *tree) write(rel, content string) {
	tr.t.Helper()
	p := filepath.Join(tr.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		tr.t.Fatal(err)
	}
	tr.files = append(tr.files, rel)
}

func (tr *tree) hits(files []string) []string {
	tr.t.Helper()
	hits, err := scan(tr.root, files)
	if err != nil {
		tr.t.Fatal(err)
	}
	var got []string
	for _, h := range hits {
		got = append(got, h.String())
	}
	slices.Sort(got)
	return got
}

func sameHits(t *testing.T, got, want []string) {
	t.Helper()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("hits:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The scan itself, on a temp tree: every term hits, in each spelling it covers, and the
// allowlisted forms and longer words do not; binary files are skipped, and the design docs are
// scanned like the rest.
func TestDenylistScanCatchesEveryTermAndHonoursTheAllowlist(t *testing.T) {
	tr := newTree(t)

	// Samples per term, in a file that ships; mixed case proves case does not matter, and the
	// separator variants are the spellings a rename would most likely leave behind.
	samples := map[string][]string{
		j("tech", "dufus"):         {j("user ", "Tech", "Dufus")},
		j("de", "garmo"):           {j("Matthew De", "Garmo")},
		j("10.0", ".20."):          {j("host 10.0", ".20.20:6443")},
		j("02:0a", ":00:14"):       {j("mac 02:0A", ":00:14:01:02"), j("mac 02-0a", "-00-14-01-02"), j("02:0a", "-00:14")},
		j("pve", "-730"):           {j("node PVE", "-730"), j("pve", "_730"), j("pve", " 730"), j("pve", "730")},
		j("talos", "-cp"):          {j("vm talos", "-cp-1"), j("talos", "_cp"), j("talos", "cp1")},
		j("talos", "-vip"):         {j("talos", "-vip.lan"), j("talos", "_vip"), j("talos", " VIP"), j("talos", "vip")},
		j("yah", "wai"):            {j("tenant yah", "wai")},
		j("registry", ".home"):     {j("image registry", ".home.example")},
		j("omp", "-cloud"):         {j("vm omp", "-cloud"), j("omp", "_cloud"), j("omp", "cloud")},
		j("mac", "-mini"):          {j("machine mac", "-mini"), j("Mac", " Mini"), j("mac", "_mini"), j("mac", "mini")},
		j("rpi", "-control"):       {j("machine rpi", "-control SSH"), j("rpi", "_control"), j("rpi", "control")},
		j("e5", "-2683"):           {j("cpu E5", "-2683 v4")},
		j("fast", "-zfs"):          {j("pool fast", "-zfs"), j("fast", "_zfs")},
		j("csi", "@pve"):           {j("token csi", "@pve!x")},
		j("homelab", " guardrail"): {j("homelab", " guardrail: denied")},
		j("homelab", ".io/"):       {j("label homelab", ".io/protected")},
		j("homelab", "_zfs_scrub"): {j("metric homelab", "_zfs_scrub_last")},
		j("homelab", "-cluster"):   {j("vault Home", "Lab-Cluster")},
		j("r7", "30"):              {j("Dell R7", "30"), j("a R7", "30xd")},
		j("power", "edge"):         {j("Dell Power", "Edge")},
		j("id", "rac-"):            {j("host id", "rac-01")},
		j("udm", "-pro"):           {j("gateway UDM", "-Pro"), j("udm", "_pro"), j("UDM", " Pro"), j("udm", "pro")},
		j("tailscale", "-router"):  {j("pod tailscale", "-router-0"), j("tailscale", "_router"), j("Tailscale", " router"), j("tailscale", "router")},
		j("nas", "-backup"):        {j("cronjob nas", "-backup"), j("nas", "_backup"), j("NAS", " backup"), j("nas", "backup")},
		j("un", "as"):              {j("the UN", "AS box"), j("un", "as-pro"), j("UN", "AS_Pro"), j("UN", "AS Pro"), j("UN", "ASPro")},
		j("github-runner", "-1"):   {j("runner github-runner", "-1"), j("github", "_runner", "_1")},
		j("hl", "-op"):             {j("run hl", "-op"), j("hl", "_op run")},
		j("../home", "lab"):        {j("cd ../home", "lab")},
		j("op", "://"):             {j("ref op", "://Vault/item")},
		j("2020 && ", "vmid"):      {j("vmid >= 2020 && ", "vmid <= 2022")},
		j("gateway", "/home"):      {j("route gateway", "/home")},
		j("flux", "-system"):       {j("ns flux", "-system")},
	}
	var want []string
	for i, d := range denylist {
		ss, ok := samples[d.name]
		if !ok {
			t.Fatalf("no sample for term %q", d.name)
		}
		for k, s := range ss {
			rel := fmt.Sprintf("internal/t%02d/t%d.go", i, k)
			tr.write(rel, "package t\n\n// "+s+"\n")
			want = append(want, hit{rel, 3, d.name}.String())
		}
	}

	// Allowlisted forms, each where its entry applies.
	tr.write("go.mod", j("module github.com/", owner, "/dash\n"))
	tr.write("Dockerfile", j(`LABEL src="https://github.com/`, owner, `/dash" image=ghcr.io/`, owner, "/dash\n"))
	tr.write("docs/links.md", j("clone https://github.com/", owner, "/dash.git or see https://github.com/", owner, "/dash/issues\n"))
	tr.write("docs/more-links.md", j(
		"[the repository](https://github.com/", owner, "/dash), `github.com/", owner, "/dash`,\n",
		"'https://github.com/", owner, "/dash', https://github.com/", owner, "/dash#readme and\n",
		"https://github.com/", owner, "/dash. Also at the end: https://github.com/", owner, "/dash.\n"))
	tr.write("app.txt", j("io.github.", owner, ".dash\n"))
	tr.write("LICENSE", j("Copyright (c) 2026 Matthew De", "Garmo (", owner, ")\n"))
	tr.write(".github/CODEOWNERS", j("* @", owner, "\n"))
	tr.write("docs/verify.md", j("gh attestation verify oci://ghcr.io/", owner, "/dash:1.0.0 --owner ", owner, "\n"))
	// resolve.go: the two pinned strings, then another reference on line 5, which still hits.
	tr.write("internal/secret/resolve.go", j("package secret\n\nfunc f(raw string) bool { return strings.HasPrefix(raw, \"op", "://\") }\n",
		"var e = fmt.Errorf(\"%s: op", ":// references are not built in\", p)\n",
		"const ref = \"op", "://Lab/token\"\n"))
	want = append(want, hit{"internal/secret/resolve.go", 5, j("op", "://")}.String())
	tr.write("internal/secret/secret_test.go", j("package secret\n\nvar c = x{raw: \"op", "://vault/item/field\"}\n"))
	// The broker variable is allowed in upper case only; the tool's own name still hits.
	tr.write("Makefile", j("dev-api-op:\n\t\"$(", "HL", "_OP)\" run -- make dev-api\n# not hl", "-op\n"))
	want = append(want, hit{"Makefile", 3, j("hl", "-op")}.String())
	tr.write("internal/config/removed.go", j("package config\n\nvar old = []string{\"DASH_", "NAS", "_BACKUP_CRONJOB\"}\n"))
	// Word boundaries: longer words that contain a short term are clean.
	tr.write("words.txt", j("unassigned unasked br", "730 shl", "-ops\n"))
	// A flux namespace outside shipped code is fine.
	tr.write("internal/x/x_test.go", j("package x\n\nconst ns = \"flux", "-system\"\n"))
	// Binaries (a NUL byte) are skipped; the design docs and ADRs are scanned like any other file.
	tr.write("docs/design/d.md", j("mac", "-mini\n"))
	want = append(want, hit{"docs/design/d.md", 1, j("mac", "-mini")}.String())
	tr.write("docs/adr/0001.md", j("Deciders: ", owner, "\n"))
	want = append(want, hit{"docs/adr/0001.md", 1, j("tech", "dufus")}.String())
	tr.write("img.png", j("\x00\x01mac", "-mini"))

	// Invalid UTF-8 is not binary: the term in it still hits.
	tr.write("latin1.txt", j("caf\xe9 mac", "-mini\n"))
	want = append(want, hit{"latin1.txt", 1, j("mac", "-mini")}.String())

	// An allowlisted form excuses only itself: the user name next to it still hits, a lookalike
	// repository does not match the form, a form allowlisted for one file does not apply to
	// another, and the pinned op strings excuse no other reference in their own file.
	tr.write("mixed.md", j("see https://github.com/", owner, "/dash, ask ", owner, "\n"))
	want = append(want, hit{"mixed.md", 1, j("tech", "dufus")}.String())
	tr.write("fork.md", j("https://github.com/", owner, "/dash-fork\n"))
	want = append(want, hit{"fork.md", 1, j("tech", "dufus")}.String())
	for i, s := range []string{"/dash-x", "/dashboard", "/dash.evil", "/dash.github.io", "/dash_x"} {
		rel := fmt.Sprintf("lookalike%d.md", i)
		tr.write(rel, j("https://github.com/", owner, s, "\n"))
		want = append(want, hit{rel, 1, j("tech", "dufus")}.String())
	}
	tr.write("README.md", j("* @", owner, " by Matthew De", "Garmo\n"))
	want = append(want, hit{"README.md", 1, j("tech", "dufus")}.String(), hit{"README.md", 1, j("de", "garmo")}.String())
	tr.write("other.go", j("package o\n\nconst p = \"op", "://\"\n"))
	want = append(want, hit{"other.go", 3, j("op", "://")}.String())
	tr.write("internal/secret/more_test.go", j("package secret\n\nvar c = x{raw: \"op", "://vault/item/field\"}\n"))
	want = append(want, hit{"internal/secret/more_test.go", 3, j("op", "://")}.String())
	tr.write("internal/secret/creds.go", j("package secret\n\nconst ref = \"op", "://Lab/token\"\n"))
	want = append(want, hit{"internal/secret/creds.go", 3, j("op", "://")}.String())

	// A tracked file deleted in the worktree, and a gitlink (a directory), are skipped.
	files := append(slices.Clone(tr.files), "gone.txt", "internal")

	sameHits(t, tr.hits(files), want)
}

// When git cannot list the files the scan walks the tree instead, skipping .git, node_modules and
// the web build output, and says why with git's reason: outside a git checkout (a source
// tarball), inside a checkout that is not the tree's own (a tarball unpacked in another
// repository), or in a checkout that tracks nothing. In its own checkout it lists what git tracks.
func TestDenylistScanWalksWhenGitCannotList(t *testing.T) {
	fill := func(tr *tree, dir string) {
		tr.write(dir+"cmd/a.go", j("package a\n\n// mac", "-mini\n"))
		tr.write(dir+"web/node_modules/m/index.js", j("mac", "-mini\n"))
		tr.write(dir+"node_modules/m/index.js", j("mac", "-mini\n"))
		tr.write(dir+"web/dist/assets/a.js", j("mac", "-mini\n"))
	}
	mustGit := func(t *testing.T, root string, args ...string) {
		t.Helper()
		if _, err := git(root, args...); err != nil {
			t.Fatal(err)
		}
	}
	walked := func(t *testing.T, root, reason string) {
		t.Helper()
		files, note, err := listFiles(root)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(note, "every file of the tree") || !strings.Contains(note, reason) {
			t.Errorf("note = %q, want the walk because %q", note, reason)
		}
		slices.Sort(files)
		if !slices.Equal(files, []string{"cmd/a.go"}) {
			t.Errorf("walked files = %v, want only cmd/a.go", files)
		}
		hits, err := scan(root, files)
		if err != nil || len(hits) != 1 || hits[0].String() != (hit{"cmd/a.go", 3, j("mac", "-mini")}).String() {
			t.Errorf("hits = %v, %v; want cmd/a.go:3", hits, err)
		}
	}

	t.Run("no checkout", func(t *testing.T) { // git's reason varies when TMPDIR sits in a checkout
		tr := newTree(t)
		fill(tr, "")
		tr.write(".git/config", j("mac", "-mini\n")) // not a repository, and never scanned
		walked(t, tr.root, "")
	})
	t.Run("inside another checkout", func(t *testing.T) {
		tr := newTree(t)
		mustGit(t, tr.root, "init", "-q")
		fill(tr, "src/")
		walked(t, filepath.Join(tr.root, "src"), "not its top")
	})
	t.Run("nothing tracked", func(t *testing.T) {
		tr := newTree(t)
		mustGit(t, tr.root, "init", "-q")
		fill(tr, "")
		walked(t, tr.root, "listed nothing")
	})
	t.Run("own checkout", func(t *testing.T) {
		tr := newTree(t)
		mustGit(t, tr.root, "init", "-q")
		fill(tr, "")
		tr.write("untracked.txt", j("mac", "-mini\n"))
		mustGit(t, tr.root, "add", "cmd/a.go")
		files, note, err := listFiles(tr.root)
		if err != nil || note != "git ls-files" || !slices.Equal(files, []string{"cmd/a.go"}) {
			t.Errorf("listFiles = %v, %q, %v; want git's one tracked file", files, note, err)
		}
	})
}
