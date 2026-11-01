package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// logBuf captures what Resolve warns about.
func logBuf() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

const sampleFile = `
server:
  publicURL: https://dash.example.com/
  listen: 0.0.0.0:9090
  healthListen: 127.0.0.1:9091
  allowedHosts: [Media.Example.com, 192.0.2.10]
settings:
  ui: readonly
dataDir: /var/lib/dash
plugins:
  prometheus: {url: "http://prometheus.example.com:9090/"}
  alertmanager: {url: "http://alertmanager.example.com:9093"}
  victorialogs: {url: "http://vlogs.example.com:9428"}
  gatus: {url: "http://gatus.example.com:8080"}
  proxmox:
    url: https://pve.example.com:8006
    node: pve
    tokenID: dash@pve!dash
    tokenSecret: s3cret
  hardware:
    hosts: [{nodename: box, model: Dell R730}]
    zfsScrubMetricPrefix: example_zfs_scrub
  headlamp: {url: "https://headlamp.example.com", cluster: prod}
  grafana: {url: "https://grafana.example.com"}
  launcher:
    links:
      - {name: Proxmox, href: "https://pve.example.com:8006", icon: proxmox}
  kubernetes:
    kubeconfig: /etc/dash/kubeconfig
    conventions: {protectedLabel: example.com/protected, tenantLabel: example.com/tenant, appsFluxPrefix: apps-}
    backupJobs: [{namespace: backup, cronjob: nightly}]
  flux: {suspendReasonAnnotation: example.com/suspended}
`

func TestFileLoadsIntoConfig(t *testing.T) {
	res, err := Resolve(Sources{File: &File{Path: "/etc/dash/config.yaml", Data: []byte(sampleFile)}})
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Listen: "0.0.0.0:9090", HealthListen: "127.0.0.1:9091", PublicURL: "https://dash.example.com",
		AllowedHosts: []string{"media.example.com", "192.0.2.10"},
		DataDir:      "/var/lib/dash", SettingsUI: SettingsReadOnly,
		VMURL: "http://prometheus.example.com:9090", AlertmanagerURL: "http://alertmanager.example.com:9093",
		VLogsURL: "http://vlogs.example.com:9428", GatusURL: "http://gatus.example.com:8080",
		ProxmoxURL: "https://pve.example.com:8006", ProxmoxNode: "pve", ProxmoxTokenID: "dash@pve!dash", ProxmoxTokenSecret: secret.New("s3cret", ""),
		HostNodename: "box", HostModel: "Dell R730",
		HeadlampURL: "https://headlamp.example.com", HeadlampCluster: "prod", GrafanaURL: "https://grafana.example.com",
		Launcher:   []model.LauncherLink{{Name: "Proxmox", Href: "https://pve.example.com:8006", Icon: "proxmox"}},
		Kubeconfig: "/etc/dash/kubeconfig",
		Conventions: Conventions{
			ProtectedLabel: "example.com/protected", TenantLabel: "example.com/tenant",
			SuspendReasonAnnotation: "example.com/suspended", AppsFluxPrefix: "apps-",
			ZFSScrubMetricPrefix: "example_zfs_scrub", NASBackupNamespace: "backup", NASBackupCronJob: "nightly",
		},
	}
	if !reflect.DeepEqual(res.Config, want) {
		t.Errorf("Config =\n%+v\nwant\n%+v", res.Config, want)
	}
}

func TestFileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name, file string
		want       []string // every one must appear in the error
	}{
		{"unknown key names its full path", "plugins:\n  proxmox:\n    urll: x\n", []string{"plugins.proxmox.urll: unknown field", "/etc/dash/config.yaml"}},
		{"unknown section", "plugin: {}\n", []string{"plugin: unknown field"}},
		{"unknown key in a list entry", "plugins:\n  hardware:\n    hosts: [{nodenam: x}]\n", []string{"plugins.hardware.hosts", `unknown field "nodenam"`}},
		{"every problem is reported", "server: {lisen: a}\nplugins: {proxmoxx: {}}\n", []string{"server.lisen: unknown field", "plugins.proxmoxx: unknown field"}},
		{"wrong scalar type", "server:\n  listen: 8080\n", []string{"server.listen: expected a string, got number"}},
		{"section is not a map", "server: nope\n", []string{"server: expected a section"}},
		{"list is not a list", "server:\n  allowedHosts: x\n", []string{"server.allowedHosts: expected a list of strings, got string"}},
		{"duplicate keys", "server: {listen: a}\nserver: {listen: b}\n", []string{"config file"}},
		{"not yaml", "server: [\n", []string{"config file"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Sources{
				Environ: []string{"DASH_DEV=true"},
				File:    &File{Path: "/etc/dash/config.yaml", Data: []byte(tc.file)},
			})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestEmptyFileIsValid(t *testing.T) {
	for _, data := range []string{"", "# only a comment\n", "{}\n", "server:\nplugins:\n"} {
		if _, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, File: &File{Path: "c.yaml", Data: []byte(data)}}); err != nil {
			t.Errorf("%q: %v", data, err)
		}
	}
}

// No file and no environment beyond the required public URL: no layer sets anything else, and
// every leaf reports where it came from.
func TestNoFileAndNoEnvLeaveEveryLeafAtItsDefault(t *testing.T) {
	res, err := Resolve(Sources{Environ: []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range schema.leaves {
		want := Provenance{Source: SourceDefault}
		if l.path == "server.publicURL" {
			want = Provenance{SourceEnv, "DASH_SERVER_PUBLIC_URL"}
		}
		if got := res.Provenance[l.path]; got != want {
			t.Errorf("Provenance[%s] = %+v, want %+v", l.path, got, want)
		}
	}
}

func TestLayerPrecedenceAndProvenance(t *testing.T) {
	file := &File{Path: "/etc/dash/config.yaml", Data: []byte(`
plugins:
  grafana: {url: "https://grafana.file.example.com"}
  headlamp: {url: "https://headlamp.file.example.com"}
`)}
	ui := map[string]any{
		"plugins": map[string]any{
			"grafana":  map[string]any{"url": "https://grafana.ui.example.com"},
			"headlamp": map[string]any{"url": "https://headlamp.ui.example.com", "cluster": "ui-cluster"},
			"gatus":    map[string]any{"url": "http://gatus.ui.example.com"},
		},
	}
	log, _ := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEV=true", "DASH_HEADLAMP_URL=https://headlamp.env.example.com"},
		File:    file, UI: ui, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.GrafanaURL != "https://grafana.file.example.com" || c.HeadlampURL != "https://headlamp.env.example.com" ||
		c.HeadlampCluster != "ui-cluster" || c.GatusURL != "http://gatus.ui.example.com" {
		t.Errorf("effective values: grafana %q headlamp %q cluster %q gatus %q", c.GrafanaURL, c.HeadlampURL, c.HeadlampCluster, c.GatusURL)
	}
	for path, want := range map[string]Provenance{
		"plugins.grafana.url":      {SourceFile, "/etc/dash/config.yaml"},
		"plugins.headlamp.url":     {SourceEnv, "DASH_HEADLAMP_URL"},
		"plugins.headlamp.cluster": {SourceUI, ""},
		"plugins.gatus.url":        {SourceUI, ""},
		"server.listen":            {SourceDefault, ""},
	} {
		if got := res.Provenance[path]; got != want {
			t.Errorf("Provenance[%s] = %+v, want %+v", path, got, want)
		}
	}
	for _, l := range schema.leaves {
		if res.Provenance[l.path].Source == "" {
			t.Errorf("no provenance for %s", l.path)
		}
	}
}

func TestListsAreOneProvenanceEntry(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte("plugins:\n  hardware:\n    hosts: [{nodename: a, model: m}]\n")}
	res, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, File: file})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Provenance["plugins.hardware.hosts"]; got != (Provenance{SourceFile, "c.yaml"}) {
		t.Errorf("hosts provenance = %+v", got)
	}
	for path := range res.Provenance {
		if strings.Contains(path, "[") || strings.HasPrefix(path, "plugins.hardware.hosts.") {
			t.Errorf("list element has its own provenance: %s", path)
		}
	}
}

func TestUIIneligiblePathsAreIgnoredWithAWarning(t *testing.T) {
	ui := map[string]any{
		"server":   map[string]any{"listen": "0.0.0.0:1", "publicURL": "https://evil.example.com"},
		"settings": map[string]any{"ui": "readonly"},
		"dataDir":  "/tmp/x",
		"auth":     map[string]any{"mode": "none"},
		"plugins":  map[string]any{"grafana": map[string]any{"url": "https://grafana.example.com"}, "nope": map[string]any{}},
	}
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, UI: ui, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if c.Listen != "127.0.0.1:8080" || c.PublicURL != "http://localhost:5173" || c.SettingsUI != SettingsEditable || c.DataDir != "/data" {
		t.Errorf("UI changed an ineligible field: %+v", c)
	}
	if c.GrafanaURL != "https://grafana.example.com" {
		t.Errorf("eligible UI value lost: %q", c.GrafanaURL)
	}
	out := buf.String()
	for _, p := range []string{"server.listen", "server.publicURL", "settings.ui", "dataDir", "auth.mode", "plugins.nope"} {
		if !strings.Contains(out, "path="+p+" ") {
			t.Errorf("no warning for %s in:\n%s", p, out)
		}
	}
	if n := strings.Count(out, "level=WARN"); n != 6 {
		t.Errorf("%d warnings, want 6:\n%s", n, out)
	}
}

func TestUIWrongTypeIsIgnoredNotFatal(t *testing.T) {
	log, buf := logBuf()
	ui := map[string]any{"plugins": map[string]any{"grafana": map[string]any{"url": 7.0}}}
	res, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, UI: ui, Log: log})
	if err != nil {
		t.Fatalf("a stored UI value must not stop dash starting: %v", err)
	}
	if res.Config.GrafanaURL != "" || !strings.Contains(buf.String(), "plugins.grafana.url") {
		t.Errorf("grafana %q, log %q", res.Config.GrafanaURL, buf)
	}
}

func TestValidationNamesPathAndSource(t *testing.T) {
	file := &File{Path: "/etc/dash/config.yaml", Data: []byte("plugins:\n  grafana: {url: \"ftp://grafana\"}\n")}
	_, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, File: file})
	if err == nil || !strings.Contains(err.Error(), "plugins.grafana.url (file /etc/dash/config.yaml)") {
		t.Errorf("file: %v", err)
	}
	_, err = Resolve(Sources{Environ: []string{"DASH_DEV=true", "DASH_GRAFANA_URL=ftp://grafana"}})
	if err == nil || !strings.Contains(err.Error(), "plugins.grafana.url (env DASH_GRAFANA_URL)") {
		t.Errorf("env: %v", err)
	}
}

func TestValidateTree(t *testing.T) {
	tests := []struct{ name, file, want string }{
		{"settings.ui values", "settings: {ui: sometimes}\n", "settings.ui"},
		{"token halves go together", "plugins: {proxmox: {url: 'https://pve', tokenID: a!b}}\n", "tokenID and tokenSecret"},
		{"token needs a url", "plugins: {proxmox: {tokenID: a!b, tokenSecret: c}}\n", "plugins.proxmox.url"},
		{"one hardware host", "plugins: {hardware: {hosts: [{nodename: a}, {nodename: b}]}}\n", "plugins.hardware.hosts"},
		{"backup job is complete", "plugins: {kubernetes: {backupJobs: [{namespace: a}]}}\n", "needs both namespace and cronjob"},
		{"launcher file xor links", "plugins: {launcher: {file: /x, links: [{name: a, href: 'https://a'}]}}\n", "plugins.launcher.file"},
		{"launcher link href", "plugins: {launcher: {links: [{name: a, href: 'javascript:alert(1)'}]}}\n", "entry 1 needs a name and an http(s) href"},
		{"proxmox url scheme", "plugins: {proxmox: {url: 'pve.example.com:8006'}}\n", "plugins.proxmox.url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Sources{Environ: []string{"DASH_DEV=true"}, File: &File{Path: "c.yaml", Data: []byte(tc.file)}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSchemaMetadata(t *testing.T) {
	check := func(path string, secret, restart, url bool) {
		t.Helper()
		l := schema.byPath[path]
		if l == nil {
			t.Fatalf("no leaf %s", path)
		}
		if l.secret != secret || l.restart != restart || l.url != url {
			t.Errorf("%s: secret=%v restart=%v url=%v, want %v %v %v", path, l.secret, l.restart, l.url, secret, restart, url)
		}
	}
	check("plugins.proxmox.tokenSecret", true, false, false)
	check("plugins.proxmox.tokenID", false, false, false)
	check("server.listen", false, true, false)
	check("server.publicURL", false, true, true)
	check("plugins.prometheus.url", false, false, true)
	if n := len(schema.leaves); n != len(schema.byPath) {
		t.Errorf("%d leaves, %d paths: duplicate paths", n, len(schema.byPath))
	}
}

func TestConfigFileLocation(t *testing.T) {
	dir := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("data dir default", func(t *testing.T) {
		write(filepath.Join(dir, "config.yaml"), "server: {listen: 127.0.0.1:1}\n")
		f, err := readConfigFile([]string{"DASH_DATA_DIR=" + dir})
		if err != nil || f == nil || f.Path != filepath.Join(dir, "config.yaml") {
			t.Fatalf("file %+v, err %v", f, err)
		}
	})
	t.Run("absent default file is no file", func(t *testing.T) {
		f, err := readConfigFile([]string{"DASH_DATA_DIR=" + t.TempDir()})
		if f != nil || err != nil {
			t.Fatalf("file %+v, err %v", f, err)
		}
	})
	t.Run("DASH_CONFIG wins over the data dir", func(t *testing.T) {
		p := filepath.Join(dir, "other.yaml")
		write(p, "server: {listen: 127.0.0.1:2}\n")
		f, err := readConfigFile([]string{"DASH_DATA_DIR=" + dir, "DASH_CONFIG=" + p})
		if err != nil || f == nil || f.Path != p {
			t.Fatalf("file %+v, err %v", f, err)
		}
	})
	t.Run("missing DASH_CONFIG is an error", func(t *testing.T) {
		_, err := readConfigFile([]string{"DASH_CONFIG=" + filepath.Join(dir, "missing.yaml")})
		if err == nil || !strings.Contains(err.Error(), "DASH_CONFIG") {
			t.Fatalf("err = %v", err)
		}
	})
}

// ProcessSources reads the file the process environment points at, with env on top.
func TestProcessSourcesLayerFileAndEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	launcher := filepath.Join(dir, "launcher.yaml")
	if err := os.WriteFile(launcher, []byte("- {name: Router, href: 'http://192.0.2.1'}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := "server: {publicURL: 'https://dash.example.com', listen: '127.0.0.1:9000'}\nplugins: {launcher: {file: " + launcher + "}}\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DASH_CONFIG", p)
	t.Setenv("DASH_LISTEN", "127.0.0.1:9001")
	src, err := ProcessSources(nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Resolve(src)
	if err == nil {
		err = res.LoadLauncherFile()
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.PublicURL != "https://dash.example.com" || res.Config.Listen != "127.0.0.1:9001" {
		t.Errorf("publicURL %q listen %q", res.Config.PublicURL, res.Config.Listen)
	}
	if len(res.Config.Launcher) != 1 || res.Config.Launcher[0].Name != "Router" {
		t.Errorf("launcher = %+v", res.Config.Launcher)
	}
}
