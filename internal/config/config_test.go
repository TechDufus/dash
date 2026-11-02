package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
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
		SettingsUI: SettingsEditable, Limits: defaultLimits, ReadyTimeout: 60 * time.Second,
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

// The hardware host's nodename is the plugin's to resolve (hosts[0].nodename, else the Proxmox
// node), so the tree carries what was set and nothing derived.
func TestHostNodenameIsNotDerivedInTheTree(t *testing.T) {
	res, err := Resolve(Sources{Environ: environ(map[string]string{"DASH_DEV": "true", "DASH_PLUGINS_PROXMOX_NODE": "pve-1"})})
	if err != nil {
		t.Fatal(err)
	}
	if hw := res.Tree.Plugins.Hardware; len(hw.Hosts) != 0 || res.Tree.Plugins.Proxmox.Node != "pve-1" {
		t.Errorf("hosts = %+v, node = %q", hw.Hosts, res.Tree.Plugins.Proxmox.Node)
	}
	res, err = Resolve(Sources{Environ: environ(map[string]string{
		"DASH_DEV": "true", "DASH_PLUGINS_PROXMOX_NODE": "pve-1", "DASH_PLUGINS_HARDWARE_HOSTS": `[{"nodename":"box"}]`,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if hw := res.Tree.Plugins.Hardware; len(hw.Hosts) != 1 || hw.Hosts[0].Nodename != "box" || res.Tree.Plugins.Proxmox.Node != "pve-1" {
		t.Errorf("hosts = %+v, node = %q", hw.Hosts, res.Tree.Plugins.Proxmox.Node)
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
		{"dev on all interfaces", map[string]string{"DASH_DEV": "true", "DASH_SERVER_LISTEN": "0.0.0.0:8080"}, "DASH_SERVER_LISTEN"},
		{"dev with an empty host", map[string]string{"DASH_DEV": "true", "DASH_SERVER_LISTEN": ":8080"}, "DASH_SERVER_LISTEN"},
		{"dev on ipv6 loopback", map[string]string{"DASH_DEV": "true", "DASH_SERVER_LISTEN": "[::1]:8080"}, ""},
		{"dev on localhost", map[string]string{"DASH_DEV": "true", "DASH_SERVER_LISTEN": "localhost:8080"}, ""},
		{"demo may bind every interface", map[string]string{"DASH_DEMO": "true", "DASH_SERVER_LISTEN": "0.0.0.0:8080"}, ""},
		{"minimal prod", map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com"}, ""},
		{"token needs proxmox url",
			map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_PLUGINS_PROXMOX_TOKEN_ID": "u!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET": "s"}, "plugins.proxmox.url"},
		{"token with proxmox url, node left for discovery",
			map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_PLUGINS_PROXMOX_TOKEN_ID": "u!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET": "s",
				"DASH_PLUGINS_PROXMOX_URL": "https://pve:8006"}, ""},
		{"token with proxmox",
			map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_PLUGINS_PROXMOX_TOKEN_ID": "u!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET": "s",
				"DASH_PLUGINS_PROXMOX_URL": "https://pve:8006", "DASH_PLUGINS_PROXMOX_NODE": "pve"}, ""},
		{"bad metric prefix",
			map[string]string{"DASH_DEV": "true", "DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX": "a-b"}, "not a metric name"},
		{"allowed hosts: name, ip and wildcard",
			map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_SERVER_ALLOWED_HOSTS": `[" Dash.Example.com", "192.0.2.10 ", "", "*"]`}, ""},
		{"allowed hosts: scheme", map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_SERVER_ALLOWED_HOSTS": `["https://x"]`}, "DASH_SERVER_ALLOWED_HOSTS"},
		{"allowed hosts: port", map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_SERVER_ALLOWED_HOSTS": `["x:80"]`}, "DASH_SERVER_ALLOWED_HOSTS"},
		{"allowed hosts: path", map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_SERVER_ALLOWED_HOSTS": `["a/b"]`}, "DASH_SERVER_ALLOWED_HOSTS"},
		{"public url without a scheme", map[string]string{"DASH_SERVER_PUBLIC_URL": "dash.example.com"}, "DASH_SERVER_PUBLIC_URL"},
		{"public url with host and port but no scheme", map[string]string{"DASH_SERVER_PUBLIC_URL": "dash.example.com:8443"}, "DASH_SERVER_PUBLIC_URL"},
		{"public url with another scheme", map[string]string{"DASH_SERVER_PUBLIC_URL": "ftp://x"}, "DASH_SERVER_PUBLIC_URL"},
		{"public url without a host", map[string]string{"DASH_SERVER_PUBLIC_URL": "https://"}, "DASH_SERVER_PUBLIC_URL"},
		{"public url in dev", map[string]string{"DASH_DEV": "true", "DASH_SERVER_PUBLIC_URL": "http://localhost:5173"}, ""},
		{"public url with a bad scheme in demo", map[string]string{"DASH_DEMO": "true", "DASH_SERVER_PUBLIC_URL": "localhost:5173"}, "DASH_SERVER_PUBLIC_URL"},
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

	c := mustLoad(t, map[string]string{"DASH_DEV": "true", "DASH_SERVER_ALLOWED_HOSTS": `[" Media.Example.com", "", "192.0.2.10"]`})
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
