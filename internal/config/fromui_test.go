package config

import "testing"

// A section is FromUI only when one of its url leaves takes its effective value from the UI
// layer: a UI URL under a file or env value, or a UI value of another leaf, is not.
func TestFromUIFollowsTheProvenanceOfURLLeaves(t *testing.T) {
	file := &File{Path: "/etc/dash/config.yaml", Data: []byte(`
plugins:
  grafana: {url: "https://grafana.file.example.com"}
`)}
	ui := map[string]any{
		"plugins": map[string]any{
			"grafana":  map[string]any{"url": "https://grafana.ui.example.com"},
			"headlamp": map[string]any{"url": "https://headlamp.ui.example.com", "cluster": "ui-cluster"},
			"gatus":    map[string]any{"url": "http://gatus.ui.example.com"},
			"proxmox":  map[string]any{"node": "pve1"},
		},
	}
	log, _ := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEV=true", "DASH_PLUGINS_HEADLAMP_URL=https://headlamp.env.example.com"},
		File:    file, UI: ui, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	for section, want := range map[string]bool{
		"plugins.gatus":      true,  // the URL is the UI's
		"plugins.grafana":    false, // the file's URL wins over the UI's
		"plugins.headlamp":   false, // the env's URL wins; cluster from the UI is not a URL
		"plugins.proxmox":    false, // only node from the UI
		"plugins.prometheus": false, // defaults
		"plugins.gat":        false, // a prefix of a section is not the section
	} {
		if got := res.FromUI(section); got != want {
			t.Errorf("FromUI(%s) = %v, want %v", section, got, want)
		}
	}
}

// plugins.ssh.hosts is a list of addresses, not a URL: tagged dial, it decides FromUI like a url
// leaf does. The list has one provenance, so the UI's list makes the section FromUI, and the
// file's or the environment's list does not, whatever else the UI sets.
func TestFromUIFollowsTheProvenanceOfTheSSHHosts(t *testing.T) {
	if l := schema.byPath["plugins.ssh.hosts"]; l == nil || !l.dial || l.url {
		t.Fatalf("plugins.ssh.hosts leaf = %+v, want dial and not url", l)
	}
	uiHosts := []any{map[string]any{"name": "lo", "address": "127.0.0.1:2222", "user": "alice"}}
	for _, tc := range []struct {
		name string
		src  Sources
		want bool
	}{
		{"from the UI", Sources{UI: map[string]any{"plugins": map[string]any{"ssh": map[string]any{"hosts": uiHosts}}}}, true},
		{"from the file, interval from the UI", Sources{
			File: &File{Path: "/etc/dash/config.yaml", Data: []byte("plugins:\n  ssh:\n    hosts: [{address: '127.0.0.1:2222', user: alice}]\n")},
			UI:   map[string]any{"plugins": map[string]any{"ssh": map[string]any{"hosts": uiHosts, "intervalSeconds": 10.0}}},
		}, false},
		{"from the environment", Sources{
			Environ: []string{`DASH_PLUGINS_SSH_HOSTS=[{"address":"127.0.0.1:2222","user":"alice"}]`},
			UI:      map[string]any{"plugins": map[string]any{"ssh": map[string]any{"hosts": uiHosts}}},
		}, false},
	} {
		log, _ := logBuf()
		tc.src.Environ = append(tc.src.Environ, "DASH_DEV=true")
		tc.src.Log = log
		res, err := Resolve(tc.src)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := res.FromUI("plugins.ssh"); got != tc.want {
			t.Errorf("%s: FromUI = %v, want %v", tc.name, got, tc.want)
		}
		if h := res.Tree.Plugins.SSH.Hosts; len(h) != 1 || h[0].Address != "127.0.0.1:2222" {
			t.Errorf("%s: hosts = %+v", tc.name, h)
		}
	}
}

// A host list that keeps its value but moves between the config file and the UI store changes
// the section, as a URL does: the supervisor restarts the plugin with the new FromUI.
func TestReloadAnnouncesSSHHostsThatMoveIntoOrOutOfTheUI(t *testing.T) {
	const path = "plugins.ssh.hosts"
	withFile := headlampConfig("https://hl-a.example.com") + "  ssh:\n    hosts: [{address: '127.0.0.1:2222', user: alice}]\n"
	g := newStoreRig(t, withFile, map[string]any{path: []any{map[string]any{"address": "127.0.0.1:2222", "user": "alice"}}})
	if g.rl.Resolved().FromUI("plugins.ssh") {
		t.Fatal("at startup the file's list wins")
	}
	for _, step := range []struct {
		name   string
		cfg    string
		fromUI bool
	}{
		{"removed from the file", headlampConfig("https://hl-a.example.com"), true},
		{"back in the file", withFile, false},
	} {
		g.changes = nil
		writeFile(t, g.cfgPath, step.cfg)
		if err := g.rl.Reload(); err != nil {
			t.Fatal(err)
		}
		if got := g.rl.Resolved().FromUI("plugins.ssh"); got != step.fromUI {
			t.Fatalf("%s: FromUI = %v", step.name, got)
		}
		if len(g.changes) != 1 || g.changes[0].Section != "plugins.ssh" || len(g.changes[0].Paths) != 1 || g.changes[0].Paths[0] != path {
			t.Errorf("%s: changes %+v, want plugins.ssh [%s]", step.name, g.changes, path)
		}
	}
}
