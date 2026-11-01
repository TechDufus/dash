package pluginconf

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

func TestLauncherValidate(t *testing.T) {
	link := model.LauncherLink{Name: "Router", Href: "http://192.0.2.1"}
	for name, tc := range map[string]struct {
		l    Launcher
		want []string
		msg  string
	}{
		"empty":           {Launcher{}, nil, ""},
		"file":            {Launcher{File: "/etc/dash/launcher.yaml"}, nil, ""},
		"links":           {Launcher{Links: []model.LauncherLink{link, {Name: "x", Href: "https://x.example.com"}}}, nil, ""},
		"file and links":  {Launcher{File: "/f", Links: []model.LauncherLink{link}}, []string{"links"}, "plugins.launcher.file"},
		"no name":         {Launcher{Links: []model.LauncherLink{link, {Href: "https://x"}}}, []string{"links"}, "entry 2 needs a name and an http(s) href"},
		"javascript href": {Launcher{Links: []model.LauncherLink{{Name: "x", Href: "javascript:alert(1)"}}}, []string{"links"}, "entry 1"},
		"relative href":   {Launcher{Links: []model.LauncherLink{{Name: "x", Href: "/local"}}}, []string{"links"}, "entry 1"},
		"both problems":   {Launcher{File: "/f", Links: []model.LauncherLink{{Href: "https://x"}}}, []string{"links", "links"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.l.Validate()
			if got := paths(err); !slices.Equal(got, tc.want) {
				t.Fatalf("problems at %v, want %v (%v)", got, tc.want, err)
			}
			if tc.msg != "" && !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %q lacks %q", err, tc.msg)
			}
		})
	}
}

func TestLoadLauncher(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	links, err := LoadLauncher(write("ok.yaml", `
- name: Proxmox
  href: https://pve.example.com:8006
  icon: proxmox
  description: Hypervisor
- name: Router
  href: http://192.0.2.1
  icon: https://example.com/router.png
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].Icon != "proxmox" || links[1].Href != "http://192.0.2.1" || links[0].Description != "Hypervisor" {
		t.Errorf("links = %+v", links)
	}
	for name, body := range map[string]string{
		"unknown field": "- name: x\n  href: https://x\n  url: y\n",
		"no name":       "- href: https://x\n",
		"bad scheme":    "- name: x\n  href: javascript:alert(1)\n",
		"not a list":    "name: x\n",
	} {
		if _, err := LoadLauncher(write(strings.ReplaceAll(name, " ", "-")+".yaml", body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	_, err = LoadLauncher(filepath.Join(dir, "missing.yaml"))
	if err == nil || !strings.HasPrefix(err.Error(), "launcher file: ") {
		t.Errorf("missing file: %v, want a launcher file error", err)
	}
}

func TestExampleLauncherParses(t *testing.T) {
	links, err := LoadLauncher("../../docs/launcher.example.yaml")
	if err != nil || len(links) == 0 {
		t.Fatalf("docs/launcher.example.yaml: %v (%d links)", err, len(links))
	}
}
