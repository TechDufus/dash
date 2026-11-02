package config

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
	"github.com/TechDufus/dash/internal/upstream"
)

// reloadRig is a running configuration read from a config file and a secret file, with a
// Reloader on top whose log and change events are captured.
type reloadRig struct {
	dir, cfgPath, secretPath string
	res                      *Resolved
	rl                       *Reloader
	reg                      *secret.Registry
	log                      *bytes.Buffer
	changes                  []Change
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func headlampConfig(url string) string {
	return "plugins:\n  headlamp:\n    url: " + url + "\n"
}

// newReloadRig starts from cfg in $dir/config.yaml (DASH_CONFIG) and a Proxmox token secret
// read from $dir/token through DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE.
func newReloadRig(t *testing.T, cfg string, extraEnv ...string) *reloadRig {
	t.Helper()
	g := &reloadRig{dir: t.TempDir(), reg: secret.NewRegistry()}
	g.cfgPath, g.secretPath = filepath.Join(g.dir, "config.yaml"), filepath.Join(g.dir, "token")
	writeFile(t, g.cfgPath, cfg)
	writeFile(t, g.secretPath, "token-one-0123456789\n")
	env := append([]string{
		"DASH_DEMO=true", "DASH_CONFIG=" + g.cfgPath, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=" + g.secretPath,
		"DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t",
	}, extraEnv...)
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	g.log = buf
	if g.res, err = Resolve(Sources{Environ: env, File: f, Registry: g.reg, Log: log}); err != nil {
		t.Fatal(err)
	}
	g.rl = NewReloader(g.res, nil, ReloadOptions{Log: log})
	g.rl.Subscribe(func(c Change) { g.changes = append(g.changes, c) })
	return g
}

func (g *reloadRig) count(msg string) int { return strings.Count(g.log.String(), msg) }

// A ConfigMap update swaps the ..data symlink to a new directory. The file keeps its name, and
// here its size and modification time too; the reloader still sees it (P1.7 acceptance).
func TestReloadSeesAConfigMapStyleSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	version := func(name, url string) {
		d := filepath.Join(dir, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(d, "config.yaml"), headlampConfig(url))
		if err := os.Chtimes(filepath.Join(d, "config.yaml"), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	version("..v1", "https://hl-a.example.com")
	version("..v2", "https://hl-b.example.com") // same size, same time
	if err := os.Symlink("..v1", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(filepath.Join("..data", "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}

	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + cfg}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: env, File: f, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	rl := NewReloader(res, nil, ReloadOptions{Log: log})
	var got []Change
	rl.Subscribe(func(c Change) { got = append(got, c) })

	rl.poll()
	if len(got) != 0 {
		t.Fatalf("nothing changed yet: %+v", got)
	}

	next := filepath.Join(dir, "..data_tmp")
	if err := os.Symlink("..v2", next); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, filepath.Join(dir, "..data")); err != nil { // atomic, as kubelet does
		t.Fatal(err)
	}
	rl.poll()
	if len(got) != 1 || got[0].Section != "plugins.headlamp" || !slices.Equal(got[0].Paths, []string{"plugins.headlamp.url"}) {
		t.Fatalf("changes = %+v", got)
	}
	if !slices.Equal(rl.RestartRequired(), []string{"plugins.headlamp.url"}) {
		t.Errorf("restart required = %v", rl.RestartRequired())
	}
	if res.Tree.Plugins.Headlamp.URL != "https://hl-a.example.com" {
		t.Error("nothing but secrets applies live: the running config must not change")
	}
	rl.poll()
	if len(got) != 1 || strings.Count(buf.String(), "level=WARN") != 1 {
		t.Errorf("an unchanged file must not repeat the event or the warning:\n%s", buf)
	}
}

// An edit that does not validate changes nothing and is logged once, however often it is
// polled; a different error is a new message, and the next valid edit takes over (P1.7).
func TestReloadKeepsTheRunningConfigOnAnInvalidEditAndLogsOnce(t *testing.T) {
	g := newReloadRig(t, headlampConfig("https://hl-a.example.com"))
	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    urll: https://hl-b.example.com\n")
	for range 3 {
		g.rl.poll()
	}
	if n := g.count("level=ERROR"); n != 1 || !strings.Contains(g.log.String(), "plugins.headlamp.urll") {
		t.Fatalf("want one ERROR naming the field:\n%s", g.log)
	}
	if len(g.changes) != 0 || len(g.rl.RestartRequired()) != 0 || g.res.Tree.Plugins.Headlamp.URL != "https://hl-a.example.com" {
		t.Error("an invalid edit must change nothing")
	}

	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    url: [not, a, string]\n")
	g.rl.poll()
	g.rl.poll()
	if n := g.count("level=ERROR"); n != 2 {
		t.Errorf("a different error is one more message, got %d:\n%s", n, g.log)
	}

	writeFile(t, g.cfgPath, headlampConfig("https://hl-b.example.com"))
	g.rl.poll()
	if len(g.changes) != 1 || g.count("level=ERROR") != 2 {
		t.Errorf("the next valid edit takes over: changes %+v\n%s", g.changes, g.log)
	}
}

// An invalid state whose cause no watched file shows (a secret file that does not exist yet) is
// retried every tick, logged once, and recovers when the file appears.
func TestReloadRetriesAnInvalidStateWhoseCauseIsNotWatched(t *testing.T) {
	dir := t.TempDir()
	cfgPath, later := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "later")
	writeFile(t, cfgPath, headlampConfig("https://hl-a.example.com"))
	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + cfgPath}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: env, File: f, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	rl := NewReloader(res, nil, ReloadOptions{Log: log})
	var got []Change
	rl.Subscribe(func(c Change) { got = append(got, c) })

	writeFile(t, cfgPath, headlampConfig("https://hl-a.example.com")+"  proxmox:\n    url: https://pve.example.com:8006\n    tokenID: u@pve!t\n    tokenSecret: file:"+later+"\n")
	for range 3 {
		rl.poll()
	}
	if n := strings.Count(buf.String(), "level=ERROR"); n != 1 || !strings.Contains(buf.String(), later) {
		t.Fatalf("want one ERROR naming the missing file:\n%s", buf)
	}
	writeFile(t, later, "token-later-0123456789")
	rl.poll()
	if strings.Count(buf.String(), "level=ERROR") != 1 || rl.lastErr != "" || len(got) != 1 {
		t.Errorf("creating the file must recover: %+v\n%s", got, buf)
	}
}

// A value saved through the settings store after startup is re-resolved on the next poll
// (P1.6 "next reload"): it is flagged for a restart like any other change, a value the
// environment sets stays hidden behind it, and deleting the saved value takes it back.
func TestReloadReReadsTheSettingsStore(t *testing.T) {
	dir := t.TempDir()
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	env := []string{"DASH_DEMO=true", "DASH_DATA_DIR=" + dir, "DASH_PLUGINS_HEADLAMP_URL=https://hl-env.example.com"}
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: env, UI: st.Values(), UIRef: st.Path(), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	rl := NewReloader(res, st, ReloadOptions{Log: log})
	var got []Change
	rl.Subscribe(func(c Change) { got = append(got, c) })
	rl.poll()
	if len(got) != 0 {
		t.Fatalf("nothing changed yet: %+v", got)
	}

	if err := st.Set("plugins.grafana.url", "https://grafana.ui.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("plugins.headlamp.url", "https://hl-ui.example.com"); err != nil { // the environment wins
		t.Fatal(err)
	}
	rl.poll()
	if len(got) != 1 || got[0].Section != "plugins.grafana" || !slices.Equal(got[0].Paths, []string{"plugins.grafana.url"}) {
		t.Fatalf("changes = %+v\n%s", got, buf)
	}
	if !slices.Equal(rl.RestartRequired(), []string{"plugins.grafana.url"}) {
		t.Errorf("restart required = %v", rl.RestartRequired())
	}
	if res.Tree.Plugins.Grafana.URL != "" {
		t.Error("the running config must not change")
	}
	rl.poll()
	if len(got) != 1 || strings.Count(buf.String(), "level=WARN") != 1 {
		t.Errorf("an unchanged store must not repeat the event or the warning:\n%s", buf)
	}

	if err := st.Delete("plugins.grafana.url"); err != nil {
		t.Fatal(err)
	}
	rl.poll()
	if len(got) != 2 || len(rl.RestartRequired()) != 0 {
		t.Errorf("deleting the saved value is back to the startup value: %+v, restart %v", got, rl.RestartRequired())
	}
}

// A secret file that changes after the startup read and before the reloader exists is still
// picked up by its first poll: the baseline is the startup tree, not the file as found later.
func TestReloadFirstPollAppliesAChangeMadeBeforeTheReloaderExisted(t *testing.T) {
	g := newReloadRig(t, headlampConfig("https://hl-a.example.com"))
	held := g.res.Tree.Plugins.Proxmox.TokenSecret
	writeFile(t, g.secretPath, "token-two-9876543210\n")
	log, _ := logBuf()
	rl := NewReloader(g.res, nil, ReloadOptions{Log: log})
	var got []Change
	rl.Subscribe(func(c Change) { got = append(got, c) })

	rl.poll()
	if held.Reveal() != "token-two-9876543210" {
		t.Fatal("the first poll must apply the secret that changed before the reloader existed")
	}
	if len(got) != 1 || got[0].Section != "plugins.proxmox" {
		t.Errorf("changes = %+v", got)
	}
	rl.poll()
	if len(got) != 1 {
		t.Errorf("an unchanged file must not repeat the event: %+v", got)
	}
}

// A rotated secret file reaches the Value the running config already handed out, and a client
// built on it signs its next request with the new token; the registry scrubs both (P1.7).
func TestReloadRotatesSecretsInPlace(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		fmt.Fprint(w, `{"data":[{"node":"pve","status":"online"}]}`)
	}))
	defer pve.Close()

	g := newReloadRig(t, headlampConfig("https://hl-a.example.com"), "DASH_PLUGINS_PROXMOX_URL="+pve.URL)
	held := g.res.Tree.Plugins.Proxmox.TokenSecret
	p := g.res.Tree.Plugins.Proxmox
	client := upstream.NewProxmox(p.URL, "", p.TokenID, held, nil)
	if _, err := client.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}

	writeFile(t, g.secretPath, "token-two-9876543210\n") // different size and content
	g.rl.poll()
	if held.Reveal() != "token-two-9876543210" || g.res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != "token-two-9876543210" {
		t.Fatal("every holder of the Value must see the rotated secret")
	}
	if _, err := client.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(auth) != 2 || !strings.HasSuffix(auth[0], "=token-one-0123456789") || !strings.HasSuffix(auth[1], "=token-two-9876543210") {
		t.Errorf("Authorization headers = %q", auth)
	}
	mu.Unlock()

	if len(g.changes) != 1 || g.changes[0].Section != "plugins.proxmox" || !slices.Equal(g.changes[0].Paths, []string{"plugins.proxmox.tokenSecret"}) {
		t.Errorf("changes = %+v", g.changes)
	}
	if r := g.rl.RestartRequired(); len(r) != 0 {
		t.Errorf("a rotated secret applies live, got restart required %v", r)
	}
	for _, old := range []string{"token-one-0123456789", "token-two-9876543210"} {
		if got := g.reg.Scrub("said " + old); strings.Contains(got, old) {
			t.Errorf("%q must stay scrubbable: %q", old, got)
		}
		if strings.Contains(g.log.String(), old) {
			t.Errorf("the log shows a secret:\n%s", g.log)
		}
	}
	if g.count("secret reloaded") != 1 {
		t.Errorf("want one INFO for the rotation:\n%s", g.log)
	}

	// Rewriting the same content is no change.
	writeFile(t, g.secretPath, "token-two-9876543210\n")
	g.rl.poll()
	if len(g.changes) != 1 {
		t.Error("identical content is not a change")
	}
}

// Everything else a valid edit changes is flagged, once, and clears when edited back.
func TestReloadFlagsChangedSettingsAsRestartRequired(t *testing.T) {
	g := newReloadRig(t, headlampConfig("https://hl-a.example.com"))
	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    url: https://hl-b.example.com\n  grafana:\n    url: https://grafana.example.com\nserver:\n  listen: 127.0.0.1:9090\n")
	g.rl.poll()
	want := []string{"plugins.grafana.url", "plugins.headlamp.url", "server.listen"}
	if got := g.rl.RestartRequired(); !slices.Equal(got, want) {
		t.Fatalf("restart required = %v, want %v", got, want)
	}
	var sections []string
	for _, c := range g.changes {
		sections = append(sections, c.Section)
	}
	if !slices.Equal(sections, []string{"plugins.grafana", "plugins.headlamp", "server"}) {
		t.Errorf("one event per affected section, got %v", sections)
	}
	if g.count("settings apply after a restart") != 1 || !strings.Contains(g.log.String(), "plugins.grafana.url") {
		t.Errorf("want one WARN listing the settings:\n%s", g.log)
	}

	// A further edit warns only about what is new.
	writeFile(t, g.cfgPath, "plugins:\n  headlamp:\n    url: https://hl-b.example.com\n  grafana:\n    url: https://grafana.example.com\nserver:\n  listen: 127.0.0.1:9090\n  allowedHosts: [extra.example.com]\n")
	g.rl.poll()
	if g.count("settings apply after a restart") != 2 || strings.Count(g.log.String(), "server.listen") != 1 {
		t.Errorf("the second WARN names only the new setting:\n%s", g.log)
	}

	writeFile(t, g.cfgPath, headlampConfig("https://hl-a.example.com"))
	g.rl.poll()
	if r := g.rl.RestartRequired(); len(r) != 0 {
		t.Errorf("editing back to the startup values clears the list, got %v", r)
	}
}

// A secret that appears after startup is a restart, not a live change: the plugin that reads it
// is off until then. It is registered for scrubbing all the same.
func TestReloadFlagsASecretThatAppearsAfterStartup(t *testing.T) {
	dir := t.TempDir()
	cfgPath, tokenPath := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "token")
	writeFile(t, cfgPath, headlampConfig("https://hl-a.example.com"))
	writeFile(t, tokenPath, "late-secret-0123456789\n")
	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + cfgPath}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	reg := secret.NewRegistry()
	res, err := Resolve(Sources{Environ: env, File: f, Registry: reg, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	rl := NewReloader(res, nil, ReloadOptions{Log: log})
	var got []Change
	rl.Subscribe(func(c Change) { got = append(got, c) })

	writeFile(t, cfgPath, headlampConfig("https://hl-a.example.com")+"  proxmox:\n    url: https://pve.example.com:8006\n    tokenID: u@pve!t\n    tokenSecret: file:"+tokenPath+"\n")
	rl.poll()
	if r := rl.RestartRequired(); !slices.Contains(r, "plugins.proxmox.tokenSecret") {
		t.Errorf("restart required = %v", r)
	}
	if res.Tree.Plugins.Proxmox.TokenSecret.IsSet() {
		t.Error("the running config must not gain a secret")
	}
	if len(got) != 1 || got[0].Section != "plugins.proxmox" {
		t.Errorf("changes = %+v", got)
	}
	if s := reg.Scrub("x late-secret-0123456789"); strings.Contains(s, "late-secret") || strings.Contains(buf.String(), "late-secret") {
		t.Errorf("the new secret must be scrubbed and never logged: %q\n%s", s, buf)
	}

	// The new file is watched from now on: rotating it is seen (and still needs the restart).
	writeFile(t, tokenPath, "later-secret-0123456789\n")
	rl.poll()
	if len(got) != 2 {
		t.Errorf("a file referenced only after startup must be watched: %+v", got)
	}
}

// Run polls on its interval and stops with its context.
func TestReloaderRunPollsUntilCancelled(t *testing.T) {
	g := newReloadRig(t, headlampConfig("https://hl-a.example.com"))
	g.rl.interval = time.Millisecond
	events := make(chan Change, 4)
	g.rl.Subscribe(func(c Change) { events <- c })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.rl.Run(ctx); close(done) }()

	writeFile(t, g.cfgPath, headlampConfig("https://hl-b.example.com"))
	select {
	case c := <-events:
		if c.Section != "plugins.headlamp" {
			t.Errorf("change = %+v", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no change event")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run must return when its context is done")
	}
}

func TestSectionOf(t *testing.T) {
	for path, want := range map[string]string{
		"plugins.proxmox.tokenSecret":                   "plugins.proxmox",
		"plugins.kubernetes.conventions.protectedLabel": "plugins.kubernetes",
		"server.limits.streams":                         "server",
		"dataDir":                                       "dataDir",
		"settings.ui":                                   "settings",
	} {
		if got := sectionOf(path); got != want {
			t.Errorf("sectionOf(%q) = %q, want %q", path, got, want)
		}
	}
}
