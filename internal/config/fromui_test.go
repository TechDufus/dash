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
