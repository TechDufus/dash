package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNoSiteDefaults(t *testing.T) {
	c := Load(env(nil))
	if c.PublicURL != "" || c.ProxmoxURL != "" || c.ProxmoxNode != "" || c.HeadlampURL != "" ||
		c.GrafanaURL != "" || c.HostModel != "" || c.HostNodename != "" {
		t.Errorf("site-specific default leaked: %+v", c)
	}
	if c.Conventions.ProtectedLabel != "homelab.io/protected" || c.Conventions.AppsFluxPrefix != "app-" {
		t.Errorf("conventions: %+v", c.Conventions)
	}
}

func TestHostNodenameFollowsProxmoxNode(t *testing.T) {
	if c := Load(env(map[string]string{"DASH_PROXMOX_NODE": "pve-1"})); c.HostNodename != "pve-1" {
		t.Errorf("HostNodename = %q", c.HostNodename)
	}
	c := Load(env(map[string]string{"DASH_PROXMOX_NODE": "pve-1", "DASH_HOST_NODENAME": "box"}))
	if c.HostNodename != "box" {
		t.Errorf("HostNodename = %q", c.HostNodename)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // substring of the error; "" means valid
	}{
		{"prod needs public url", nil, "DASH_PUBLIC_URL"},
		{"dev does not", map[string]string{"DASH_DEV": "true"}, ""},
		{"demo does not", map[string]string{"DASH_DEMO": "true"}, ""},
		{"minimal prod", map[string]string{"DASH_PUBLIC_URL": "https://dash.example.com"}, ""},
		{"token needs proxmox url and node",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_PROXMOX_TOKEN": "u!t=s"}, "DASH_PROXMOX_NODE"},
		{"token with proxmox",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_PROXMOX_TOKEN": "u!t=s",
				"DASH_PROXMOX_URL": "https://pve:8006", "DASH_PROXMOX_NODE": "pve"}, ""},
		{"bad metric prefix",
			map[string]string{"DASH_DEV": "true", "DASH_ZFS_SCRUB_METRIC_PREFIX": "a-b"}, "not a metric name"},
	}
	for _, tc := range tests {
		err := Load(env(tc.env)).Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want it to mention %q", tc.name, err, tc.want)
		}
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
	if _, err := LoadLauncher(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("missing file: want an error")
	}
}

func TestExampleLauncherParses(t *testing.T) {
	links, err := LoadLauncher("../../docs/launcher.example.yaml")
	if err != nil || len(links) == 0 {
		t.Fatalf("docs/launcher.example.yaml: %v (%d links)", err, len(links))
	}
}
