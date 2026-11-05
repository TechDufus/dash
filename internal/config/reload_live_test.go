package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// liveRig is a reloader over a config file in a temp directory.
type liveRig struct {
	path string
	log  *bytes.Buffer
	rl   *Reloader
	got  []Change
}

func newLiveRig(t *testing.T, file string, live func(string) bool) *liveRig {
	t.Helper()
	g := &liveRig{path: filepath.Join(t.TempDir(), "config.yaml"), log: &bytes.Buffer{}}
	g.write(t, file)
	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + g.path}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(g.log, nil))
	res, err := Resolve(Sources{Environ: env, File: f, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	g.rl = NewReloader(res, nil, ReloadOptions{Log: log, Live: live})
	g.rl.Subscribe(func(c Change) { g.got = append(g.got, c) })
	return g
}

func (g *liveRig) write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(g.path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (g *liveRig) edit(t *testing.T, body string) {
	t.Helper()
	g.write(t, body)
	g.rl.poll()
}

func headlamp(url string) string { return "plugins:\n  headlamp:\n    url: " + url + "\n" }

func TestLiveSectionsAreNotRestartRequired(t *testing.T) {
	live := map[string]bool{"plugins.headlamp": true}
	g := newLiveRig(t, headlamp("https://hl-a.example.com")+"  grafana:\n    url: https://gf-a.example.com\n", func(s string) bool { return live[s] })
	g.rl.poll()

	g.edit(t, headlamp("https://hl-b.example.com")+"  grafana:\n    url: https://gf-b.example.com\n")
	// Both changes are announced, in section order.
	if len(g.got) != 2 || g.got[0].Section != "plugins.grafana" || g.got[1].Section != "plugins.headlamp" {
		t.Fatalf("changes = %+v", g.got)
	}
	// Only the section nobody applies in-process needs a restart.
	if got := g.rl.RestartRequired(); !slices.Equal(got, []string{"plugins.grafana.url"}) {
		t.Fatalf("restart required = %v, want only the section that is not live", got)
	}
	log := g.log.String()
	if !strings.Contains(log, "plugins.grafana.url") || strings.Contains(log, "plugins.headlamp.url") {
		t.Fatalf("the restart warning must name grafana only:\n%s", log)
	}

	// With no hook, nothing is live (the zero value of ReloadOptions).
	g = newLiveRig(t, headlamp("https://hl-a.example.com"), nil)
	g.edit(t, headlamp("https://hl-b.example.com"))
	if got := g.rl.RestartRequired(); !slices.Equal(got, []string{"plugins.headlamp.url"}) {
		t.Fatalf("without Live: restart required = %v", got)
	}
}

func TestRestartTaggedLeafAlwaysNeedsRestart(t *testing.T) {
	var asked []string
	g := newLiveRig(t, "server:\n  listen: 127.0.0.1:9001\n"+headlamp("https://hl-a.example.com"), func(s string) bool {
		asked = append(asked, s)
		return true // every section claims to be live
	})
	g.edit(t, "server:\n  listen: 127.0.0.1:9002\n"+headlamp("https://hl-b.example.com"))
	// The rig runs in demo mode, whose default public URL follows server.listen: it moves too.
	if got := g.rl.RestartRequired(); !slices.Equal(got, []string{"server.listen", "server.publicURL"}) {
		t.Fatalf("restart required = %v: a restart-tagged setting needs one, a live plugin section does not", got)
	}
	if slices.Contains(asked, "server") {
		t.Errorf("Live was asked about %v, but a restart-tagged leaf does not depend on it", asked)
	}
	if !slices.Contains(asked, "plugins.headlamp") {
		t.Errorf("Live was asked about %v, want plugins.headlamp", asked)
	}
	// A restart-tagged setting edited back to its startup value leaves the list.
	g.edit(t, "server:\n  listen: 127.0.0.1:9001\n"+headlamp("https://hl-b.example.com"))
	if got := g.rl.RestartRequired(); len(got) != 0 {
		t.Fatalf("restart required = %v after the edit was undone", got)
	}
}

func TestReloaderTreeTracksLastValidResolution(t *testing.T) {
	g := newLiveRig(t, headlamp("https://hl-a.example.com"), nil)
	if got := g.rl.Tree().Plugins.Headlamp.URL; got != "https://hl-a.example.com" {
		t.Fatalf("before any reload: %q", got)
	}
	g.edit(t, headlamp("https://hl-b.example.com"))
	if got := g.rl.Tree().Plugins.Headlamp.URL; got != "https://hl-b.example.com" {
		t.Fatalf("after a valid reload: %q", got)
	}
	// An invalid edit changes nothing: the running configuration stays in force.
	g.edit(t, "plugins:\n  headlamp:\n    urll: https://hl-c.example.com\n")
	if got := g.rl.Tree().Plugins.Headlamp.URL; got != "https://hl-b.example.com" {
		t.Fatalf("after an invalid reload: %q", got)
	}
	// The Change is out before a subscriber looks: Tree is already the new one.
	var seen string
	g.rl.Subscribe(func(Change) { seen = g.rl.Tree().Plugins.Headlamp.URL })
	g.edit(t, headlamp("https://hl-d.example.com"))
	if seen != "https://hl-d.example.com" {
		t.Fatalf("a subscriber saw %q", seen)
	}
	// Reading it does not block the reloader: it is a copy taken under the lock.
	tree := g.rl.Tree()
	tree.Plugins.Headlamp.URL = "changed"
	if g.rl.Tree().Plugins.Headlamp.URL != "https://hl-d.example.com" {
		t.Fatal("Tree handed out the reloader's own copy")
	}
}

// File timestamps tick coarsely, so an edit can keep the previous modification time; with the
// same size, only the content tells the change apart. This pins mtime and size and expects the
// reload anyway (the case a CI runner hit when a test wrote twice within one timestamp tick).
func TestSameSizeEditWithAnUnchangedModTimeIsReloaded(t *testing.T) {
	g := newLiveRig(t, headlamp("https://hl-a.example.com"), nil)
	g.rl.poll()
	fi, err := os.Stat(g.path)
	if err != nil {
		t.Fatal(err)
	}
	g.write(t, headlamp("https://hl-b.example.com"))
	if err := os.Chtimes(g.path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if after, err := os.Stat(g.path); err != nil || after.Size() != fi.Size() || !after.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("setup: stat must look unchanged (err %v)", err)
	}
	g.rl.poll()
	if got := g.rl.Tree().Plugins.Headlamp.URL; got != "https://hl-b.example.com" {
		t.Fatalf("an edit with the same size and modification time was missed: %q", got)
	}
}
