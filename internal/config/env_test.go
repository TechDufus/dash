package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
)

// resolveEnv resolves a demo-mode environment (no public URL needed, any listen address) plus
// the given variables, with warnings captured.
func resolveEnv(t *testing.T, env ...string) (*Resolved, string, error) {
	t.Helper()
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: append([]string{"DASH_DEMO=true"}, env...), Log: log})
	return res, buf.String(), err
}

// value is the tree value at a schema path.
func value(t *testing.T, res *Resolved, path string) any {
	t.Helper()
	l := schema.byPath[path]
	if l == nil {
		t.Fatalf("no schema path %s", path)
	}
	v := l.field(&res.Tree).Interface()
	if s, ok := v.(secret.Value); ok {
		return s.Reveal()
	}
	return v
}

// warned lists the legacy variables a log reports as deprecated, one entry per warning.
func warned(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, `msg="deprecated environment variable"`) {
			continue
		}
		_, rest, _ := strings.Cut(line, " name=")
		name, _, _ := strings.Cut(rest, " ")
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// Every row of design B.5: the legacy variable sets the new path and warns exactly once.
func TestLegacyAliases(t *testing.T) {
	tests := []struct {
		env  []string
		want map[string]any // path -> value
		ref  string         // provenance ref of every path
	}{
		{[]string{"DASH_PUBLIC_URL=https://dash.example.com/"}, map[string]any{"server.publicURL": "https://dash.example.com"}, "DASH_PUBLIC_URL"},
		{[]string{"DASH_LISTEN=0.0.0.0:9000"}, map[string]any{"server.listen": "0.0.0.0:9000"}, "DASH_LISTEN"},
		{[]string{"DASH_HEALTH_LISTEN=127.0.0.1:9001"}, map[string]any{"server.healthListen": "127.0.0.1:9001"}, "DASH_HEALTH_LISTEN"},
		{[]string{"DASH_ALLOWED_HOSTS= Media.Example.com, ,192.0.2.10"}, map[string]any{"server.allowedHosts": []string{"media.example.com", "192.0.2.10"}}, "DASH_ALLOWED_HOSTS"},
		{[]string{"DASH_VM_URL=http://prom.example.com:9090"}, map[string]any{"plugins.prometheus.url": "http://prom.example.com:9090"}, "DASH_VM_URL"},
		{[]string{"DASH_ALERTMANAGER_URL=http://am.example.com:9093"}, map[string]any{"plugins.alertmanager.url": "http://am.example.com:9093"}, "DASH_ALERTMANAGER_URL"},
		{[]string{"DASH_VLOGS_URL=http://vl.example.com:9428"}, map[string]any{"plugins.victorialogs.url": "http://vl.example.com:9428"}, "DASH_VLOGS_URL"},
		{[]string{"DASH_GATUS_URL=http://gatus.example.com:8080"}, map[string]any{"plugins.gatus.url": "http://gatus.example.com:8080"}, "DASH_GATUS_URL"},
		{[]string{"DASH_PROXMOX_URL=https://pve.example.com:8006", "DASH_PROXMOX_TOKEN=u@pve!t=s3cret"}, map[string]any{"plugins.proxmox.url": "https://pve.example.com:8006"}, "DASH_PROXMOX_URL"},
		{[]string{"DASH_PROXMOX_NODE=pve"}, map[string]any{"plugins.proxmox.node": "pve"}, "DASH_PROXMOX_NODE"},
		{[]string{"DASH_HEADLAMP_URL=https://headlamp.example.com"}, map[string]any{"plugins.headlamp.url": "https://headlamp.example.com"}, "DASH_HEADLAMP_URL"},
		{[]string{"DASH_HEADLAMP_CLUSTER=prod"}, map[string]any{"plugins.headlamp.cluster": "prod"}, "DASH_HEADLAMP_CLUSTER"},
		{[]string{"DASH_GRAFANA_URL=https://grafana.example.com"}, map[string]any{"plugins.grafana.url": "https://grafana.example.com"}, "DASH_GRAFANA_URL"},
		{[]string{"DASH_LAUNCHER_FILE=/etc/dash/launcher.yaml"}, map[string]any{"plugins.launcher.file": "/etc/dash/launcher.yaml"}, "DASH_LAUNCHER_FILE"},
		{[]string{"DASH_LABEL_PROTECTED=example.com/protected"}, map[string]any{"plugins.kubernetes.conventions.protectedLabel": "example.com/protected"}, "DASH_LABEL_PROTECTED"},
		{[]string{"DASH_LABEL_TENANT=example.com/tenant"}, map[string]any{"plugins.kubernetes.conventions.tenantLabel": "example.com/tenant"}, "DASH_LABEL_TENANT"},
		{[]string{"DASH_APPS_FLUX_PREFIX=apps-"}, map[string]any{"plugins.kubernetes.conventions.appsFluxPrefix": "apps-"}, "DASH_APPS_FLUX_PREFIX"},
		{[]string{"DASH_ANNOTATION_SUSPEND_REASON=example.com/suspended"}, map[string]any{"plugins.flux.suspendReasonAnnotation": "example.com/suspended"}, "DASH_ANNOTATION_SUSPEND_REASON"},
		{[]string{"DASH_ZFS_SCRUB_METRIC_PREFIX=example_scrub"}, map[string]any{"plugins.hardware.zfsScrubMetricPrefix": "example_scrub"}, "DASH_ZFS_SCRUB_METRIC_PREFIX"},
		{[]string{"DASH_HOST_NODENAME=box"}, map[string]any{"plugins.hardware.hosts": []HardwareHost{{Nodename: "box"}}}, "DASH_HOST_NODENAME"},
		{[]string{"DASH_HOST_MODEL=Dell R730"}, map[string]any{"plugins.hardware.hosts": []HardwareHost{{Model: "Dell R730"}}}, "DASH_HOST_MODEL"},
		{[]string{"DASH_NAS_BACKUP_NAMESPACE=backup", "DASH_NAS_BACKUP_CRONJOB=nightly"},
			map[string]any{"plugins.kubernetes.backupJobs": []BackupJob{{Namespace: "backup", CronJob: "nightly"}}}, "DASH_NAS_BACKUP_NAMESPACE, DASH_NAS_BACKUP_CRONJOB"},
	}
	covered := map[string]bool{}
	for _, tc := range tests {
		t.Run(strings.Join(tc.env, " "), func(t *testing.T) {
			res, log, err := resolveEnv(t, tc.env...)
			if err != nil {
				t.Fatal(err)
			}
			for path, want := range tc.want {
				if got := value(t, res, path); !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %#v, want %#v", path, got, want)
				}
				if p := res.Provenance[path]; p.Source != SourceEnv || p.Ref != tc.ref {
					t.Errorf("provenance of %s = %+v, want env %s", path, p, tc.ref)
				}
			}
			var wantWarn []string
			for _, kv := range tc.env {
				k, _, _ := strings.Cut(kv, "=")
				wantWarn = append(wantWarn, k)
				covered[k] = true
			}
			slices.Sort(wantWarn)
			if got := warned(log); !slices.Equal(got, wantWarn) {
				t.Errorf("deprecation warnings = %v, want one per variable %v\n%s", got, wantWarn, log)
			}
		})
	}
	for _, a := range aliases {
		for _, n := range a.names {
			if !covered[n] && n != "DASH_PROXMOX_TOKEN" && n != "KUBECONFIG" {
				t.Errorf("legacy variable %s has no test row", n)
			}
		}
	}
}

func TestLegacyProxmoxTokenIsSplitAtTheFirstEquals(t *testing.T) {
	res, log, err := resolveEnv(t, "DASH_PROXMOX_URL=https://pve.example.com:8006", "DASH_PROXMOX_TOKEN=dash@pve!ci=abc=def")
	if err != nil {
		t.Fatal(err)
	}
	if id, secret := value(t, res, "plugins.proxmox.tokenID"), value(t, res, "plugins.proxmox.tokenSecret"); id != "dash@pve!ci" || secret != "abc=def" {
		t.Errorf("tokenID %v, tokenSecret %v", id, secret)
	}
	for _, p := range []string{"plugins.proxmox.tokenID", "plugins.proxmox.tokenSecret"} {
		if got := res.Provenance[p]; got != (Provenance{SourceEnv, "DASH_PROXMOX_TOKEN"}) {
			t.Errorf("provenance of %s = %+v", p, got)
		}
	}
	if res.Config.ProxmoxTokenID != "dash@pve!ci" || res.Config.ProxmoxTokenSecret.Reveal() != "abc=def" {
		t.Errorf("Config token = %q, %v", res.Config.ProxmoxTokenID, res.Config.ProxmoxTokenSecret)
	}
	if got := warned(log); !slices.Equal(got, []string{"DASH_PROXMOX_TOKEN", "DASH_PROXMOX_URL"}) {
		t.Errorf("warnings %v", got)
	}
	if !strings.Contains(log, "DASH_PLUGINS_PROXMOX_TOKEN_ID and DASH_PLUGINS_PROXMOX_TOKEN_SECRET") {
		t.Errorf("the warning must name the replacements:\n%s", log)
	}

	// A malformed token is refused without echoing it.
	_, _, err = resolveEnv(t, "DASH_PROXMOX_URL=https://pve.example.com:8006", "DASH_PROXMOX_TOKEN=canary-no-equals-sign")
	if err == nil || !strings.Contains(err.Error(), "DASH_PROXMOX_TOKEN") || strings.Contains(err.Error(), "canary") {
		t.Errorf("malformed token: %v", err)
	}
}

// KUBECONFIG stays supported without a warning, and does not need to be set via a DASH_ name.
func TestKubeconfigIsHonouredQuietly(t *testing.T) {
	res, log, err := resolveEnv(t, "KUBECONFIG=/home/dev/.kube/config")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.Kubeconfig; got != "/home/dev/.kube/config" {
		t.Errorf("Kubeconfig = %q", got)
	}
	if strings.Contains(log, "WARN") {
		t.Errorf("unexpected warning:\n%s", log)
	}
	res, _, err = resolveEnv(t, "KUBECONFIG=/a", "DASH_PLUGINS_KUBERNETES_KUBECONFIG=/b")
	if err != nil || res.Config.Kubeconfig != "/b" {
		t.Errorf("generated name must win: %q, %v", res.Config.Kubeconfig, err)
	}
}

func TestGeneratedNames(t *testing.T) {
	for path, want := range map[string]string{
		"server.publicURL":                              "DASH_SERVER_PUBLIC_URL",
		"server.healthListen":                           "DASH_SERVER_HEALTH_LISTEN",
		"settings.ui":                                   "DASH_SETTINGS_UI",
		"dataDir":                                       "DASH_DATA_DIR",
		"plugins.proxmox.tokenID":                       "DASH_PLUGINS_PROXMOX_TOKEN_ID",
		"plugins.proxmox.tokenSecret":                   "DASH_PLUGINS_PROXMOX_TOKEN_SECRET",
		"plugins.victorialogs.url":                      "DASH_PLUGINS_VICTORIALOGS_URL",
		"plugins.hardware.zfsScrubMetricPrefix":         "DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX",
		"plugins.kubernetes.conventions.protectedLabel": "DASH_PLUGINS_KUBERNETES_CONVENTIONS_PROTECTED_LABEL",
		"plugins.kubernetes.backupJobs":                 "DASH_PLUGINS_KUBERNETES_BACKUP_JOBS",
	} {
		if got := envName(path); got != want {
			t.Errorf("envName(%s) = %s, want %s", path, got, want)
		}
		if l := schema.byPath[path]; l == nil || l.env != want {
			t.Errorf("schema leaf %s: %+v", path, l)
		}
	}
}

// Two schema paths must never generate one name, and no name may be another's _FILE form or a
// legacy variable: each variable has exactly one meaning.
func TestEnvNamesDoNotCollide(t *testing.T) {
	if len(schema.byEnv) != len(schema.leaves) {
		t.Fatalf("%d leaves generate %d distinct names", len(schema.leaves), len(schema.byEnv))
	}
	owner := map[string]string{} // variable -> what it means
	claim := func(name, meaning string) {
		t.Helper()
		if prev, ok := owner[name]; ok {
			t.Errorf("%s means both %q and %q", name, prev, meaning)
		}
		owner[name] = meaning
	}
	for _, l := range schema.leaves {
		claim(l.env, l.path)
		claim(l.env+"_FILE", l.path+" (file)")
	}
	for _, a := range aliases {
		for _, n := range a.names {
			claim(n, "legacy "+n)
			claim(n+"_FILE", "legacy "+n+" (file)")
		}
	}
}

func TestEnvSetsFieldsWithJSONForLists(t *testing.T) {
	res, log, err := resolveEnv(t,
		"DASH_SERVER_ALLOWED_HOSTS=[\"Media.Example.com\",\"192.0.2.10\"]",
		`DASH_PLUGINS_HARDWARE_HOSTS=[{"nodename":"box","model":"Dell R730"}]`,
		"DASH_PLUGINS_PROXMOX_NODE=pve",
		"DASH_PLUGINS_KUBERNETES_BACKUP_JOBS=[{\"namespace\":\"backup\",\"cronjob\":\"nightly\"}]",
	)
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if !slices.Equal(c.AllowedHosts, []string{"media.example.com", "192.0.2.10"}) || c.HostNodename != "box" || c.HostModel != "Dell R730" ||
		c.ProxmoxNode != "pve" || c.Conventions.NASBackupNamespace != "backup" || c.Conventions.NASBackupCronJob != "nightly" {
		t.Errorf("config = %+v", c)
	}
	if strings.Contains(log, "WARN") {
		t.Errorf("generated names must not warn:\n%s", log)
	}
	if got := res.Provenance["plugins.hardware.hosts"]; got != (Provenance{SourceEnv, "DASH_PLUGINS_HARDWARE_HOSTS"}) {
		t.Errorf("list provenance = %+v", got)
	}
}

func TestEnvBadValuesNameTheVariable(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{`DASH_PLUGINS_HARDWARE_HOSTS=[{"nodename":`, "DASH_PLUGINS_HARDWARE_HOSTS: plugins.hardware.hosts"},
		{`DASH_PLUGINS_HARDWARE_HOSTS={"nodename":"x"}`, "expected a list of objects"},
		{`DASH_PLUGINS_HARDWARE_HOSTS=[{"nodenam":"x"}]`, `unknown field "nodenam"`},
		{`DASH_SERVER_ALLOWED_HOSTS=a.example.com`, "DASH_SERVER_ALLOWED_HOSTS"},
		{`DASH_PLUGINS_HARDWARE_HOSTS=[] []`, "unexpected data"},
	} {
		_, _, err := resolveEnv(t, tc.env)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %q", tc.env, err, tc.want)
		}
	}
}

func TestGeneratedNameBeatsLegacyAndFileLayer(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte("plugins: {grafana: {url: 'https://file.example.com'}}\n")}
	log, _ := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true", "DASH_GRAFANA_URL=https://legacy.example.com", "DASH_PLUGINS_GRAFANA_URL=https://new.example.com"},
		File:    file, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.GrafanaURL != "https://new.example.com" || res.Provenance["plugins.grafana.url"].Ref != "DASH_PLUGINS_GRAFANA_URL" {
		t.Errorf("grafana %q, %+v", res.Config.GrafanaURL, res.Provenance["plugins.grafana.url"])
	}
	res, _ = Resolve(Sources{Environ: []string{"DASH_DEMO=true"}, File: file, Log: log})
	if res.Config.GrafanaURL != "https://file.example.com" {
		t.Errorf("without env the file value applies: %q", res.Config.GrafanaURL)
	}
}

func TestEmptyVariablesAreUnset(t *testing.T) {
	res, log, err := resolveEnv(t, "DASH_PLUGINS_GRAFANA_URL=", "DASH_VM_URL=  ", "DASH_PLUGINS_GRAFANA_URL_FILE=")
	if err != nil {
		t.Fatal(err)
	}
	if res.Provenance["plugins.grafana.url"].Source != SourceDefault || strings.Contains(log, "WARN") {
		t.Errorf("empty variables must not count: %+v\n%s", res.Provenance["plugins.grafana.url"], log)
	}
}

func TestFileSuffixVariables(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const canary = "canary-secret-value-123"

	t.Run("generated name", func(t *testing.T) {
		p := write("secret", canary+"\n")
		res, log, err := resolveEnv(t, "DASH_PLUGINS_PROXMOX_URL=https://pve.example.com", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE="+p)
		if err != nil {
			t.Fatal(err)
		}
		if got := value(t, res, "plugins.proxmox.tokenSecret"); got != canary {
			t.Errorf("tokenSecret = %q, want the file's trimmed content", got)
		}
		if got := res.Provenance["plugins.proxmox.tokenSecret"]; got != (Provenance{SourceEnv, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE"}) {
			t.Errorf("provenance = %+v", got)
		}
		if strings.Contains(log, canary) {
			t.Errorf("secret in log:\n%s", log)
		}
	})
	t.Run("list from a file", func(t *testing.T) {
		p := write("hosts", `[{"nodename":"box"}]`)
		res, _, err := resolveEnv(t, "DASH_PLUGINS_HARDWARE_HOSTS_FILE="+p)
		if err != nil || res.Config.HostNodename != "box" {
			t.Fatalf("nodename %q, err %v", res.Config.HostNodename, err)
		}
	})
	t.Run("legacy name", func(t *testing.T) {
		p := write("token", "u@pve!t="+canary+"\n")
		res, log, err := resolveEnv(t, "DASH_PROXMOX_URL=https://pve.example.com", "DASH_PROXMOX_TOKEN_FILE="+p)
		if err != nil {
			t.Fatal(err)
		}
		if res.Config.ProxmoxTokenID != "u@pve!t" || res.Config.ProxmoxTokenSecret.Reveal() != canary {
			t.Errorf("ProxmoxToken not read from the file")
		}
		if !strings.Contains(log, "name=DASH_PROXMOX_TOKEN_FILE") || strings.Contains(log, canary) {
			t.Errorf("the legacy _FILE form warns by name and never logs the value:\n%s", log)
		}
		if got := warned(log); !slices.Equal(got, []string{"DASH_PROXMOX_TOKEN_FILE", "DASH_PROXMOX_URL"}) {
			t.Errorf("warnings = %v", got)
		}
	})
	t.Run("both set is an error naming both", func(t *testing.T) {
		p := write("v", "https://file.example.com")
		for _, pair := range [][2]string{
			{"DASH_PLUGINS_GRAFANA_URL", "DASH_PLUGINS_GRAFANA_URL_FILE"},
			{"DASH_GRAFANA_URL", "DASH_GRAFANA_URL_FILE"},
		} {
			_, _, err := resolveEnv(t, pair[0]+"=https://env.example.com", pair[1]+"="+p)
			if err == nil || !strings.Contains(err.Error(), pair[0]) || !strings.Contains(err.Error(), pair[1]) {
				t.Errorf("%v: error %v must name both variables", pair, err)
			}
		}
	})
	t.Run("unreadable file", func(t *testing.T) {
		missing := filepath.Join(dir, "missing")
		_, _, err := resolveEnv(t, "DASH_PLUGINS_GRAFANA_URL_FILE="+missing)
		if err == nil || !strings.Contains(err.Error(), "DASH_PLUGINS_GRAFANA_URL_FILE") || !strings.Contains(err.Error(), missing) {
			t.Errorf("error %v must name the variable and the path", err)
		}
	})
	t.Run("a name that merely ends in _FILE is not a file form", func(t *testing.T) {
		res, _, err := resolveEnv(t, "DASH_PLUGINS_LAUNCHER_FILE=/etc/dash/launcher.yaml")
		if err != nil || res.Config.LauncherFile != "/etc/dash/launcher.yaml" {
			t.Errorf("launcher file %q, err %v", res.Config.LauncherFile, err)
		}
	})
}

func TestUnknownVariables(t *testing.T) {
	t.Run("unknown plugin variable fails startup", func(t *testing.T) {
		_, _, err := resolveEnv(t, "DASH_PLUGINS_PROXMOX_URLL=https://pve.example.com")
		if err == nil || !strings.Contains(err.Error(), "DASH_PLUGINS_PROXMOX_URLL") {
			t.Errorf("error = %v", err)
		}
		_, _, err = resolveEnv(t, "DASH_PLUGINS_NOPE_FILE=/x")
		if err == nil || !strings.Contains(err.Error(), "DASH_PLUGINS_NOPE_FILE") {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("other unknown variables warn once each", func(t *testing.T) {
		_, log, err := resolveEnv(t, "DASH_LISTN=127.0.0.1:1", "DASH_FOO_FILE=/x")
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(log, "ignoring unknown environment variable"); n != 2 ||
			!strings.Contains(log, "name=DASH_LISTN") || !strings.Contains(log, "name=DASH_FOO_FILE") {
			t.Errorf("want one warning per unknown variable:\n%s", log)
		}
	})
	t.Run("process and service-link variables never warn", func(t *testing.T) {
		_, log, err := resolveEnv(t,
			"DASH_DEV=false", "DASH_CONFIG=", "DASH_API=http://127.0.0.1:8080",
			"DASH_SECRET_KEY=x", "DASH_SECRET_KEY_FILE=/x", "DASH_SECRET_KEY_PREVIOUS=y",
			"DASH_SERVICE_HOST=10.96.0.1", "DASH_SERVICE_PORT=8080", "DASH_SERVICE_PORT_HTTP=8080",
			"DASH_PORT=tcp://10.96.0.1:8080", "DASH_PORT_8080_TCP=tcp://10.96.0.1:8080",
			"DASH_PORT_8080_TCP_PROTO=tcp", "DASH_PORT_8080_TCP_PORT=8080", "DASH_PORT_8080_TCP_ADDR=10.96.0.1",
			"DASH_PORT_4180_TCP_ADDR=10.96.0.2", "DASH_PORT_53_UDP_ADDR=10.96.0.3",
			"OTHER_APP_SETTING=1",
		)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(log, "WARN") {
			t.Errorf("unexpected warnings:\n%s", log)
		}
	})
}

func TestEnvTable(t *testing.T) {
	vars := EnvVars()
	if len(vars) != len(schema.leaves) {
		t.Fatalf("%d rows for %d leaves", len(vars), len(schema.leaves))
	}
	byPath := map[string]EnvVar{}
	for _, v := range vars {
		byPath[v.Path] = v
	}
	if v := byPath["plugins.proxmox.tokenSecret"]; v.Name != "DASH_PLUGINS_PROXMOX_TOKEN_SECRET" || !v.Secret || !slices.Equal(v.Legacy, []string{"DASH_PROXMOX_TOKEN"}) {
		t.Errorf("tokenSecret row: %+v", v)
	}
	if v := byPath["plugins.hardware.hosts"]; v.Type != "list of objects (JSON)" || !slices.Equal(v.Legacy, []string{"DASH_HOST_NODENAME", "DASH_HOST_MODEL"}) {
		t.Errorf("hosts row: %+v", v)
	}
	if v := byPath["server.listen"]; !v.Restart || v.Secret || v.Type != "string" {
		t.Errorf("listen row: %+v", v)
	}
	if v := byPath["plugins.kubernetes.kubeconfig"]; len(v.Legacy) != 0 {
		t.Errorf("KUBECONFIG is not deprecated, so it is not listed as legacy: %+v", v)
	}

	var buf bytes.Buffer
	if err := WriteEnvTable(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"PATH", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "DASH_DATA_DIR", "DASH_PROXMOX_TOKEN", "_FILE"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	for _, l := range schema.leaves {
		if !strings.Contains(out, l.env+" ") {
			t.Errorf("table lacks %s", l.env)
		}
	}
}
