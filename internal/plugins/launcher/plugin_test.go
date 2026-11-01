package launcher_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
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
	e := &env{set: &plugintest.Settings{}, hub: &plugintest.Hub{}}
	e.set.Set(ics...)
	e.sup = plugin.NewSupervisor(plugin.Options{
		Publisher:   e.hub,
		Settings:    e.set,
		StatusEvery: 10 * time.Millisecond,
		StopTimeout: time.Second,
		Log:         slog.New(slog.DiscardHandler),
		Demo:        demo,
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
