package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// environ turns a map into os.Environ form, sorted so a test's input is the same on every run.
func environ(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func load(m map[string]string) (Config, error) { return Load(environ(m)) }

func mustLoad(t *testing.T, m map[string]string) Config {
	t.Helper()
	c, err := load(m)
	if err != nil {
		t.Fatalf("Load(%v): %v", m, err)
	}
	return c
}

// Nothing site-specific has a default: every upstream and convention is off until configured.
func TestNoSiteDefaults(t *testing.T) {
	want := Config{
		Listen: "127.0.0.1:8080", HealthListen: "0.0.0.0:8081", DataDir: "/data",
		SettingsUI: SettingsEditable, HeadlampCluster: "main",
	}
	if got := Defaults(); !reflect.DeepEqual(got, want) {
		t.Errorf("Defaults() =\n%+v\nwant\n%+v", got, want)
	}
}

// The empty environment of a production start (only the required public URL) switches nothing on.
func TestPublicURLAloneConfiguresNoUpstream(t *testing.T) {
	c := mustLoad(t, map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com"})
	want := Defaults()
	want.PublicURL = "https://dash.example.com"
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Config =\n%+v\nwant\n%+v", c, want)
	}
	for _, u := range []string{c.VMURL, c.AlertmanagerURL, c.VLogsURL, c.GatusURL} {
		if u != "" {
			t.Errorf("upstream URL %q set without configuration", u)
		}
	}
}

func TestZFSScrubPrefixIsValidatedOnlyWhenSet(t *testing.T) {
	base := map[string]string{"DASH_DEV": "true"}
	if _, err := load(base); err != nil {
		t.Errorf("unset prefix: %v", err)
	}
	base["DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX"] = "example_zfs_scrub"
	if _, err := load(base); err != nil {
		t.Errorf("valid prefix: %v", err)
	}
	base["DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX"] = "a-b"
	if _, err := load(base); err == nil || !strings.Contains(err.Error(), "plugins.hardware.zfsScrubMetricPrefix") {
		t.Errorf("bad prefix: %v", err)
	}
}

func TestHostNodenameFollowsProxmoxNode(t *testing.T) {
	base := map[string]string{"DASH_DEV": "true", "DASH_PROXMOX_NODE": "pve-1"}
	if c := mustLoad(t, base); c.HostNodename != "pve-1" {
		t.Errorf("HostNodename = %q", c.HostNodename)
	}
	base["DASH_HOST_NODENAME"] = "box"
	if c := mustLoad(t, base); c.HostNodename != "box" {
		t.Errorf("HostNodename = %q", c.HostNodename)
	}
}

// A demo server may bind every interface; it never signs requests in as "dev".
func TestDemoDoesNotImplyDev(t *testing.T) {
	c := mustLoad(t, map[string]string{"DASH_DEMO": "true"})
	if !c.Demo || c.Dev {
		t.Errorf("Demo = %v, Dev = %v; want demo without dev", c.Demo, c.Dev)
	}
	if c.PublicURL != "http://localhost:5173" || c.HealthListen != "127.0.0.1:8081" {
		t.Errorf("demo defaults: PublicURL = %q, HealthListen = %q", c.PublicURL, c.HealthListen)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // substring of the error; "" means valid
	}{
		{"prod needs public url", nil, "DASH_SERVER_PUBLIC_URL"},
		{"dev does not", map[string]string{"DASH_DEV": "true"}, ""},
		{"demo does not", map[string]string{"DASH_DEMO": "true"}, ""},
		{"dev on all interfaces", map[string]string{"DASH_DEV": "true", "DASH_LISTEN": "0.0.0.0:8080"}, "DASH_LISTEN"},
		{"dev with an empty host", map[string]string{"DASH_DEV": "true", "DASH_LISTEN": ":8080"}, "DASH_LISTEN"},
		{"dev on ipv6 loopback", map[string]string{"DASH_DEV": "true", "DASH_LISTEN": "[::1]:8080"}, ""},
		{"dev on localhost", map[string]string{"DASH_DEV": "true", "DASH_LISTEN": "localhost:8080"}, ""},
		{"demo may bind every interface", map[string]string{"DASH_DEMO": "true", "DASH_LISTEN": "0.0.0.0:8080"}, ""},
		{"minimal prod", map[string]string{"DASH_PUBLIC_URL": "https://dash.example.com"}, ""},
		{"token needs proxmox url",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_PROXMOX_TOKEN": "u!t=s"}, "plugins.proxmox.url"},
		{"token with proxmox url, node left for discovery",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_PROXMOX_TOKEN": "u!t=s",
				"DASH_PROXMOX_URL": "https://pve:8006"}, ""},
		{"token with proxmox",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_PROXMOX_TOKEN": "u!t=s",
				"DASH_PROXMOX_URL": "https://pve:8006", "DASH_PROXMOX_NODE": "pve"}, ""},
		{"bad metric prefix",
			map[string]string{"DASH_DEV": "true", "DASH_ZFS_SCRUB_METRIC_PREFIX": "a-b"}, "not a metric name"},
		{"allowed hosts: name, ip and wildcard",
			map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_ALLOWED_HOSTS": " Dash.Example.com, 192.0.2.10 ,,*"}, ""},
		{"allowed hosts: scheme", map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_ALLOWED_HOSTS": "https://x"}, "DASH_ALLOWED_HOSTS"},
		{"allowed hosts: port", map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_ALLOWED_HOSTS": "x:80"}, "DASH_ALLOWED_HOSTS"},
		{"allowed hosts: path", map[string]string{"DASH_PUBLIC_URL": "https://d", "DASH_ALLOWED_HOSTS": "a/b"}, "DASH_ALLOWED_HOSTS"},
		{"public url without a scheme", map[string]string{"DASH_PUBLIC_URL": "dash.example.com"}, "DASH_PUBLIC_URL"},
		{"public url with host and port but no scheme", map[string]string{"DASH_PUBLIC_URL": "dash.example.com:8443"}, "DASH_PUBLIC_URL"},
		{"public url with another scheme", map[string]string{"DASH_PUBLIC_URL": "ftp://x"}, "DASH_PUBLIC_URL"},
		{"public url without a host", map[string]string{"DASH_PUBLIC_URL": "https://"}, "DASH_PUBLIC_URL"},
		{"public url in dev", map[string]string{"DASH_DEV": "true", "DASH_PUBLIC_URL": "http://localhost:5173"}, ""},
		{"public url with a bad scheme in demo", map[string]string{"DASH_DEMO": "true", "DASH_PUBLIC_URL": "localhost:5173"}, "DASH_PUBLIC_URL"},
	}
	for _, tc := range tests {
		_, err := load(tc.env)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	prod := Config{PublicURL: "https://dash.example.com:8443"}
	dev := Config{PublicURL: "http://localhost:5173", Dev: true}
	demo := Config{Demo: true}
	tests := []struct {
		name string
		c    Config
		host string
		want bool
	}{
		{"public host", prod, "dash.example.com", true},
		{"public host with port and case", prod, "DASH.example.com:443", true},
		{"public host with a trailing dot", prod, "dash.example.com.", true},
		{"other host", prod, "evil.example", false},
		{"loopback ip in prod", prod, "127.0.0.1:8080", false},
		{"localhost in prod", prod, "localhost", false},
		{"empty host", prod, "", false},
		{"dev localhost with port", dev, "localhost:5173", true},
		{"dev ipv6 loopback", dev, "[::1]:8080", true},
		{"dev other host", dev, "evil.example", false},
		{"demo loopback", demo, "127.0.0.1:8080", true},
		{"listed ip", Config{PublicURL: prod.PublicURL, AllowedHosts: []string{"192.0.2.10"}}, "192.0.2.10:8080", true},
		{"unlisted ip", Config{PublicURL: prod.PublicURL, AllowedHosts: []string{"192.0.2.10"}}, "192.0.2.11:8080", false},
		{"wildcard", Config{PublicURL: prod.PublicURL, AllowedHosts: []string{"*"}}, "anything.example", true},
		{"public host with a trailing dot, bare host", Config{PublicURL: "https://dash.example.com."}, "dash.example.com", true},
		{"public host with a trailing dot, dotted host", Config{PublicURL: "https://dash.example.com."}, "dash.example.com.", true},
		{"public host in capitals", Config{PublicURL: "https://DASH.Example.com"}, "dash.example.com", true},
		{"public url without a scheme matches nothing", Config{PublicURL: "dash.example.com"}, "dash.example.com", false},
	}
	for _, tc := range tests {
		if got := tc.c.HostAllowed(tc.host); got != tc.want {
			t.Errorf("%s: HostAllowed(%q) = %v, want %v", tc.name, tc.host, got, tc.want)
		}
	}

	c := mustLoad(t, map[string]string{"DASH_DEV": "true", "DASH_ALLOWED_HOSTS": " Media.Example.com, ,192.0.2.10"})
	if got := strings.Join(c.AllowedHosts, "|"); got != "media.example.com|192.0.2.10" {
		t.Errorf("AllowedHosts = %q", got)
	}
}

func TestPublicOrigin(t *testing.T) {
	tests := []struct{ url, want string }{
		{"https://dash.example.com", "https://dash.example.com"},
		{"https://DASH.Example.com/", "https://dash.example.com"},
		{"HTTPS://Dash.Example.com:8443/some/path", "https://dash.example.com:8443"},
		{"http://localhost:5173", "http://localhost:5173"},
		{"dash.example.com", "dash.example.com"}, // not a URL: returned as is (Validate rejects it)
	}
	for _, tc := range tests {
		if got := (Config{PublicURL: tc.url}).PublicOrigin(); got != tc.want {
			t.Errorf("PublicOrigin(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestDeploymentEnvCompat loads exactly the env names the homelab Kubernetes deployment sets
// (with placeholder values). DASH_PROXMOX_NODE, DASH_HOST_NODENAME, DASH_HOST_MODEL and
// DASH_LAUNCHER_FILE are deliberately absent: the node is left for discovery at startup.
func TestDeploymentEnvCompat(t *testing.T) {
	e := map[string]string{
		"DASH_PUBLIC_URL":       "https://dash.example.com",
		"DASH_VM_URL":           "http://vm.example.com:8428",
		"DASH_ALERTMANAGER_URL": "http://alertmanager.example.com:9093",
		"DASH_VLOGS_URL":        "http://vlogs.example.com:9428",
		"DASH_GATUS_URL":        "http://gatus.example.com:8080",
		"DASH_PROXMOX_URL":      "https://pve.example.com:8006",
		"DASH_HEADLAMP_URL":     "https://headlamp.example.com",
		"DASH_GRAFANA_URL":      "https://grafana.example.com",
		"DASH_PROXMOX_TOKEN":    "dash@pve!ci=00000000-0000-0000-0000-000000000000",
	}
	c := mustLoad(t, e)
	fields := map[string]string{
		"DASH_PUBLIC_URL":       c.PublicURL,
		"DASH_VM_URL":           c.VMURL,
		"DASH_ALERTMANAGER_URL": c.AlertmanagerURL,
		"DASH_VLOGS_URL":        c.VLogsURL,
		"DASH_GATUS_URL":        c.GatusURL,
		"DASH_PROXMOX_URL":      c.ProxmoxURL,
		"DASH_HEADLAMP_URL":     c.HeadlampURL,
		"DASH_GRAFANA_URL":      c.GrafanaURL,
		"DASH_PROXMOX_TOKEN":    c.ProxmoxToken,
	}
	for name, want := range e {
		if got := fields[name]; got != want {
			t.Errorf("%s: field = %q, want %q (env name not consumed?)", name, got, want)
		}
	}
	if len(fields) != len(e) {
		t.Errorf("table covers %d env names, env sets %d", len(fields), len(e))
	}
	if c.ProxmoxNode != "" || c.HostNodename != "" {
		t.Errorf("node = %q, nodename = %q, want both empty before discovery", c.ProxmoxNode, c.HostNodename)
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
