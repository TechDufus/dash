package launcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/launcher"
)

const topic model.Topic = "launcher.links"

var inline = []model.LauncherLink{
	{Name: "Proxmox", Href: "https://pve.example.com:8006", Icon: "proxmox", Description: "Hypervisor"},
	{Name: "Router", Href: "https://router.example.com", Icon: "/icons/router.png", Description: "Network"},
}

const fileYAML = `
- name: NAS
  href: https://nas.example.com
  icon: nas
  description: Backups
- name: Switch
  href: http://switch.example.com
`

func ic(c pluginconf.Launcher) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: launcher.ID, Config: &c}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "launcher.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type env struct {
	sup *plugin.Supervisor
	set *plugintest.Settings
	hub *plugintest.Hub
}

func run(t *testing.T, demo bool, ics ...plugin.InstanceConfig) *env {
	t.Helper()
	return runWith(t, demo, "", slog.New(slog.DiscardHandler), ics...)
}

// runWith is run with ui.iconBaseURL (Options.IconBase) and a logger.
func runWith(t *testing.T, demo bool, iconBase string, log *slog.Logger, ics ...plugin.InstanceConfig) *env {
	t.Helper()
	e := &env{set: &plugintest.Settings{}, hub: &plugintest.Hub{}}
	e.set.Set(ics...)
	e.sup = plugin.NewSupervisor(plugin.Options{
		Publisher:   e.hub,
		Settings:    e.set,
		StatusEvery: 10 * time.Millisecond,
		StopTimeout: time.Second,
		Log:         log,
		Demo:        demo,
		IconBase:    iconBase,
	})
	if err := e.sup.Add(launcher.New); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.sup.Run(ctx); err != nil {
		cancel()
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); e.sup.Shutdown(context.Background()) })
	return e
}

func (e *env) row(t *testing.T) model.PluginInfo {
	t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == string(launcher.ID) {
			return pi
		}
	}
	t.Fatal("launcher is not registered")
	return model.PluginInfo{}
}

// links is the payload of the last launcher.links publish.
func (e *env) links(t *testing.T) []model.LauncherLink {
	t.Helper()
	ev, ok := e.hub.Last(topic)
	if !ok {
		t.Fatal("nothing was published on launcher.links")
	}
	d, ok := ev.Data.(model.LauncherData)
	if !ok {
		t.Fatalf("payload is %T, want model.LauncherData", ev.Data)
	}
	return d.Links
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestManifestDeclaresTheTopic(t *testing.T) {
	m := launcher.New().Manifest()
	if m.ID != "launcher" || len(m.Topics) != 1 || m.Topics[0].Name != "links" {
		t.Fatalf("manifest = %+v", m)
	}
	if _, ok := m.Topics[0].Type.(model.LauncherData); !ok {
		t.Errorf("topic type is %T, want model.LauncherData", m.Topics[0].Type)
	}
	if len(m.Requires)+len(m.Uses) != 0 || len(m.Actions) != 0 {
		t.Errorf("the launcher depends on nothing and has no actions: %+v", m)
	}
}

// An inline list is published as launcher.links, and the row says which topic it owns.
func TestInlineLinksArePublished(t *testing.T) {
	e := run(t, false, ic(pluginconf.Launcher{Links: inline}))
	if got := e.links(t); !reflect.DeepEqual(got, inline) {
		t.Errorf("links = %+v, want %+v", got, inline)
	}
	row := e.row(t)
	if row.State != model.PluginHealthy || !reflect.DeepEqual(row.Topics, []string{"launcher.links"}) {
		t.Errorf("row = %+v, want healthy with topic launcher.links", row)
	}
}

// A file list is read at Start and published.
func TestFileLinksArePublished(t *testing.T) {
	e := run(t, false, ic(pluginconf.Launcher{File: writeFile(t, fileYAML)}))
	want := []model.LauncherLink{
		{Name: "NAS", Href: "https://nas.example.com", Icon: "nas", Description: "Backups"},
		{Name: "Switch", Href: "http://switch.example.com"},
	}
	if got := e.links(t); !reflect.DeepEqual(got, want) {
		t.Errorf("links = %+v, want %+v", got, want)
	}
}

// A file that lists nothing publishes an empty list ("links": []), not null: the SPA tells "no
// links" from "not here yet" by the envelope, and reads links.length.
func TestAnEmptyFilePublishesAnEmptyList(t *testing.T) {
	e := run(t, false, ic(pluginconf.Launcher{File: writeFile(t, "[]\n")}))
	got := e.links(t)
	if got == nil || len(got) != 0 {
		t.Fatalf("links = %#v, want an empty non-nil slice", got)
	}
	ev, _ := e.hub.Last(topic)
	raw, err := json.Marshal(ev.Data)
	if err != nil || string(raw) != `{"links":[]}` {
		t.Errorf("payload encodes as %s (%v), want {\"links\":[]}", raw, err)
	}
}

// An unreadable or malformed file makes the row error; it does not abort anything else, and
// nothing is published.
func TestUnreadableFileIsAnErrorRow(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("- name: x\n  href: ftp://nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	notYAML := filepath.Join(dir, "garbage.yaml")
	if err := os.WriteFile(notYAML, []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing":   filepath.Join(dir, "absent.yaml"),
		"bad href":  bad,
		"not yaml":  notYAML,
		"directory": dir,
	} {
		t.Run(name, func(t *testing.T) {
			e := run(t, false, ic(pluginconf.Launcher{File: path}))
			if row := e.row(t); row.State != model.PluginError {
				t.Errorf("state = %s, want error", row.State)
			}
			if _, ok := e.hub.Last(topic); ok {
				t.Error("a failed start must not publish")
			}
		})
	}
}

// Static rules are checked again by the plugin: a file and links together, or a link without a
// name or an http(s) href, leave the row unconfigured and publish nothing.
func TestInvalidSectionIsUnconfigured(t *testing.T) {
	yes := true
	for name, c := range map[string]pluginconf.Launcher{
		"file and links": {File: "/x.yaml", Links: inline},
		"no name":        {Links: []model.LauncherLink{{Href: "https://a.example.com"}}},
		"ftp href":       {Links: []model.LauncherLink{{Name: "a", Href: "ftp://a.example.com"}}},
	} {
		t.Run(name, func(t *testing.T) {
			e := run(t, false, plugin.InstanceConfig{Name: launcher.ID, Enabled: &yes, Config: &c})
			if row := e.row(t); row.State != model.PluginUnconfigured {
				t.Errorf("state = %s, want unconfigured", row.State)
			}
			if _, ok := e.hub.Last(topic); ok {
				t.Error("nothing may be published")
			}
		})
	}
}

// Nothing set: disabled (auto-enable) and, with enabled: true, unconfigured. Both are hidden.
func TestNothingSetIsHidden(t *testing.T) {
	yes := true
	if row := run(t, false, ic(pluginconf.Launcher{})).row(t); row.State != model.PluginDisabled {
		t.Errorf("enabled unset: state %s, want disabled", row.State)
	}
	e := run(t, false, plugin.InstanceConfig{Name: launcher.ID, Enabled: &yes, Config: &pluginconf.Launcher{}})
	if row := e.row(t); row.State != model.PluginUnconfigured {
		t.Errorf("enabled true: state %s, want unconfigured", row.State)
	}
}

// A reconfigure restarts the instance, which publishes the new list.
func TestReconfigurePublishesTheNewLinks(t *testing.T) {
	e := run(t, false, ic(pluginconf.Launcher{Links: inline}))
	next := []model.LauncherLink{{Name: "Grafana", Href: "https://grafana.example.com"}}
	e.set.Set(ic(pluginconf.Launcher{Links: next}))
	e.sup.Reload()
	eventually(t, "the new list", func() bool {
		ev, ok := e.hub.Last(topic)
		return ok && reflect.DeepEqual(ev.Data.(model.LauncherData).Links, next)
	})
	// Removing the links turns the plugin off and takes the topic with it.
	e.set.Set(ic(pluginconf.Launcher{}))
	e.sup.Reload()
	eventually(t, "the topic to be removed", func() bool {
		for _, rm := range e.hub.Removed() {
			if rm == topic {
				return true
			}
		}
		return false
	})
}

// Visibility rule (a): nothing set leaves the row unconfigured; setting links through a reconfigure
// shows it, and launcher.links reaches a subscriber that was already open.
func TestTopicsReachOpenStream(t *testing.T) {
	yes := true
	plugintest.AssertTopicsReachOpenStream(t, launcher.New,
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Launcher{}},
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Launcher{Links: inline}})
	// The same through a file.
	plugintest.AssertTopicsReachOpenStream(t, launcher.New,
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Launcher{}},
		plugin.InstanceConfig{Enabled: &yes, Config: &pluginconf.Launcher{File: writeFile(t, fileYAML)}})
}

func TestDemoTwinPublishesTheDemoLinks(t *testing.T) {
	// The configuration is empty: the twin runs anyway and reads no file.
	e := run(t, true, ic(pluginconf.Launcher{}))
	if row := e.row(t); !plugin.Visible(row.State) {
		t.Fatalf("the demo twin is %s, want visible", row.State)
	}
	if got, want := e.links(t), world.Launcher().Links; !reflect.DeepEqual(got, want) {
		t.Errorf("demo links = %+v, want %+v", got, want)
	}
	if want, got := launcher.New().Manifest(), launcher.New().(plugin.Demo).Demo().Manifest(); !reflect.DeepEqual(want.Topics, got.Topics) || want.ID != got.ID {
		t.Errorf("the twin's manifest %+v differs from the plugin's %+v", got, want)
	}
}

// logBuf is a concurrency-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func textLog(out *logBuf) *slog.Logger {
	return slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

const iconBase = "https://cdn.example.com/png"

// warnFor is the WARN lines of the log that name the launcher icon refusal.
func warnFor(out *logBuf) []string {
	var lines []string
	for _, l := range strings.Split(out.String(), "\n") {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "launcher icon refused") {
			lines = append(lines, l)
		}
	}
	return lines
}

// An inline icon that is a foreign URL, a javascript: or a data: URL makes the row error with a
// WARN naming the entry, and nothing is published. It is not a configuration error: the section
// still validates, and the other plugins are untouched.
func TestLauncherIconOutsideBaseMakesTheRowError(t *testing.T) {
	for name, icon := range map[string]string{
		"other origin":     "https://evil.example.com/png/x.png",
		"outside path":     "https://cdn.example.com/other/x.png",
		"path prefix":      "https://cdn.example.com/pngx/x.png",
		"protocol-rel":     "//evil.example.com/x.png",
		"javascript":       "javascript:alert(1)",
		"data":             "data:image/svg+xml,<svg/>",
		"credentials":      "https://user:secret@cdn.example.com/png/x.png",
		"with a query":     "https://evil.example.com/x.png?token=secret",
		"different port":   "https://cdn.example.com:8443/png/x.png",
		"different scheme": "http://cdn.example.com/png/x.png",
	} {
		t.Run(name, func(t *testing.T) {
			c := pluginconf.Launcher{Links: []model.LauncherLink{
				{Name: "ok", Href: "https://ok.example.com", Icon: "proxmox"},
				{Name: "bad", Href: "https://bad.example.com", Icon: icon},
			}}
			if err := c.Validate(); err != nil {
				t.Fatalf("the section must still validate (not a load error): %v", err)
			}
			var out logBuf
			e := runWith(t, false, iconBase, textLog(&out), ic(c))
			if row := e.row(t); row.State != model.PluginError {
				t.Errorf("state = %s, want error", row.State)
			}
			if _, ok := e.hub.Last(topic); ok {
				t.Error("a refused icon must not publish anything")
			}
			warns := warnFor(&out)
			if len(warns) != 1 || !strings.Contains(warns[0], "entry 2: icon ") {
				t.Fatalf("WARN lines = %q, want one naming entry 2", warns)
			}
			if strings.Contains(out.String(), "secret") {
				t.Errorf("the log repeats a credential or query: %s", out.String())
			}
		})
	}
}

// The same rule holds for the links of a file.
func TestLauncherIconInFileMakesTheRowError(t *testing.T) {
	path := writeFile(t, "- name: NAS\n  href: https://nas.example.com\n  icon: nas\n"+
		"- name: Evil\n  href: https://evil.example.com\n  icon: https://evil.example.com/x.png?k=secret\n")
	var out logBuf
	e := runWith(t, false, iconBase, textLog(&out), ic(pluginconf.Launcher{File: path}))
	if row := e.row(t); row.State != model.PluginError {
		t.Errorf("state = %s, want error", row.State)
	}
	if _, ok := e.hub.Last(topic); ok {
		t.Error("a refused icon must not publish anything")
	}
	if warns := warnFor(&out); len(warns) != 1 || !strings.Contains(warns[0], "entry 2: icon ") {
		t.Fatalf("WARN lines = %q, want one naming entry 2", warns)
	}
	if strings.Contains(out.String(), "secret") {
		t.Errorf("the log repeats a query: %s", out.String())
	}

	// A file with a valid icon under the base is published.
	ok := writeFile(t, "- name: NAS\n  href: https://nas.example.com\n  icon: https://cdn.example.com/png/nas.png\n")
	e = runWith(t, false, iconBase, slog.New(slog.DiscardHandler), ic(pluginconf.Launcher{File: ok}))
	if row := e.row(t); row.State != model.PluginHealthy {
		t.Errorf("state = %s, want healthy", row.State)
	}
	if got := e.links(t); len(got) != 1 || got[0].Icon != "https://cdn.example.com/png/nas.png" {
		t.Errorf("links = %+v", got)
	}
}

// Without a base no absolute icon URL is valid; names and same-origin paths are.
func TestLauncherIconURLNeedsABase(t *testing.T) {
	c := pluginconf.Launcher{Links: []model.LauncherLink{{Name: "x", Href: "https://x.example.com", Icon: "https://cdn.example.com/png/x.png"}}}
	if row := run(t, false, ic(c)).row(t); row.State != model.PluginError {
		t.Errorf("state = %s, want error", row.State)
	}
}

// Host.IconBaseURL is what Start checks against: the same links start under one base and are
// refused under another, through a host whose base is set by the test (and Start repeats the
// check on every start, as a reload does).
func TestHostIconBaseURLReachesTheLauncher(t *testing.T) {
	links := []model.LauncherLink{{Name: "x", Href: "https://x.example.com", Icon: "https://cdn.example.com/png/x.png"}}
	cfg := &pluginconf.Launcher{Links: links}
	start := func(base string) (*plugintest.Hub, string, error) {
		hub := &plugintest.Hub{}
		var out logBuf
		h := loggedHost{plugin.Host(plugintest.AllRunningWithIconBase(hub, base).HostFor(launcher.ID)), textLog(&out)}
		err := launcher.New().Start(context.Background(), h, cfg)
		return hub, out.String(), err
	}
	for _, base := range []string{"https://cdn.example.com/png", "https://cdn.example.com/png/", "https://cdn.example.com"} {
		hub, _, err := start(base)
		if err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
		if _, ok := hub.Last(topic); !ok {
			t.Errorf("base %q: nothing was published", base)
		}
	}
	for _, base := range []string{"", "https://other.example.com/png", "https://cdn.example.com/other", "http://cdn.example.com/png"} {
		hub, log, err := start(base)
		if err == nil || !strings.HasPrefix(err.Error(), "entry 1: icon ") {
			t.Errorf("base %q: error = %v, want entry 1: icon …", base, err)
		}
		if _, ok := hub.Last(topic); ok {
			t.Errorf("base %q: a refused icon published", base)
		}
		if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "entry 1: icon ") {
			t.Errorf("base %q: log %q lacks the WARN naming the entry", base, log)
		}
	}

	// A reload starts the plugin again with the same base: the check repeats, and a list that
	// became valid publishes.
	hub := &plugintest.Hub{}
	p := launcher.New()
	h := plugintest.AllRunningWithIconBase(hub, iconBase).HostFor(launcher.ID)
	if err := p.Start(context.Background(), h, cfg); err != nil {
		t.Fatal(err)
	}
	bad := &pluginconf.Launcher{Links: []model.LauncherLink{{Name: "x", Href: "https://x.example.com", Icon: "https://evil.example.com/x.png"}}}
	n := len(hub.Published())
	if err := p.Start(context.Background(), h, bad); err == nil {
		t.Error("the restart with a foreign icon must fail")
	}
	if got := len(hub.Published()); got != n {
		t.Errorf("the refused restart published %d more events", got-n)
	}
	if err := p.Start(context.Background(), h, cfg); err != nil {
		t.Errorf("the restart with a valid icon: %v", err)
	}
}

// loggedHost replaces a Host's logger.
type loggedHost struct {
	plugin.Host
	log *slog.Logger
}

func (h loggedHost) Logger() *slog.Logger { return h.log }

// docs/launcher.example.yaml plus the icon names the production file uses all start healthy under
// a base, and their names are valid without one.
func TestHomelabLauncherFileIsValid(t *testing.T) {
	example, err := os.ReadFile("../../../docs/launcher.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	production := "\n"
	for _, name := range []string{"proxmox", "idrac", "unifi", "unifi-drive", "github"} {
		production += "- name: prod-" + name + "\n  href: https://" + name + ".example.com\n  icon: " + name + "\n"
	}
	path := writeFile(t, string(example)+production)
	links, err := pluginconf.LoadLauncher(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) < 9 {
		t.Fatalf("loaded %d links, want the example's and the five production ones", len(links))
	}
	for _, base := range []string{"", "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png"} {
		if err := pluginconf.ValidateIcons(links, base); err != nil {
			t.Errorf("base %q: %v", base, err)
		}
		e := runWith(t, false, base, slog.New(slog.DiscardHandler), ic(pluginconf.Launcher{File: path}))
		if row := e.row(t); row.State != model.PluginHealthy {
			t.Errorf("base %q: state = %s, want healthy", base, row.State)
		}
		if got := e.links(t); len(got) != len(links) {
			t.Errorf("base %q: published %d links, want %d", base, len(got), len(links))
		}
	}
}

// A javascript:, data: or other non-http(s) href is refused by the launcher, inline or in a file:
// the row does not publish it.
func TestLauncherRejectsNonHTTPHref(t *testing.T) {
	for _, href := range []string{"javascript:alert(1)", "data:text/html,<script>1</script>", "vbscript:x", "ftp://a.example.com", "/relative", "//evil.example.com"} {
		t.Run("inline "+href, func(t *testing.T) {
			yes := true
			c := pluginconf.Launcher{Links: []model.LauncherLink{{Name: "x", Href: href}}}
			e := run(t, false, plugin.InstanceConfig{Name: launcher.ID, Enabled: &yes, Config: &c})
			if row := e.row(t); row.State != model.PluginUnconfigured {
				t.Errorf("state = %s, want unconfigured", row.State)
			}
			if _, ok := e.hub.Last(topic); ok {
				t.Error("nothing may be published")
			}
		})
		t.Run("file "+href, func(t *testing.T) {
			e := run(t, false, ic(pluginconf.Launcher{File: writeFile(t, "- name: x\n  href: '"+href+"'\n")}))
			if row := e.row(t); row.State != model.PluginError {
				t.Errorf("state = %s, want error", row.State)
			}
			if _, ok := e.hub.Last(topic); ok {
				t.Error("nothing may be published")
			}
		})
	}
}
