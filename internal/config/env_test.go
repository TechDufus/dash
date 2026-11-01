package config

import (
	"bytes"
	"os"
	"path/filepath"
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

// removedNames is the 24 variables dash 0.4.0 accepted as deprecated aliases, with the generated
// variable(s) that replace each, written out by hand so the tests do not derive their
// expectation from the table they check.
var removedNames = map[string][]string{
	"DASH_PUBLIC_URL":                []string{"DASH_SERVER_PUBLIC_URL"},
	"DASH_LISTEN":                    {"DASH_SERVER_LISTEN"},
	"DASH_HEALTH_LISTEN":             {"DASH_SERVER_HEALTH_LISTEN"},
	"DASH_ALLOWED_HOSTS":             {"DASH_SERVER_ALLOWED_HOSTS"},
	"DASH_VM_URL":                    {"DASH_PLUGINS_PROMETHEUS_URL"},
	"DASH_ALERTMANAGER_URL":          {"DASH_PLUGINS_ALERTMANAGER_URL"},
	"DASH_VLOGS_URL":                 {"DASH_PLUGINS_VICTORIALOGS_URL"},
	"DASH_GATUS_URL":                 {"DASH_PLUGINS_GATUS_URL"},
	"DASH_PROXMOX_URL":               {"DASH_PLUGINS_PROXMOX_URL"},
	"DASH_PROXMOX_NODE":              {"DASH_PLUGINS_PROXMOX_NODE"},
	"DASH_PROXMOX_TOKEN":             {"DASH_PLUGINS_PROXMOX_TOKEN_ID", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET"},
	"DASH_HOST_NODENAME":             {"DASH_PLUGINS_HARDWARE_HOSTS"},
	"DASH_HOST_MODEL":                {"DASH_PLUGINS_HARDWARE_HOSTS"},
	"DASH_HEADLAMP_URL":              {"DASH_PLUGINS_HEADLAMP_URL"},
	"DASH_HEADLAMP_CLUSTER":          {"DASH_PLUGINS_HEADLAMP_CLUSTER"},
	"DASH_GRAFANA_URL":               {"DASH_PLUGINS_GRAFANA_URL"},
	"DASH_LAUNCHER_FILE":             {"DASH_PLUGINS_LAUNCHER_FILE"},
	"DASH_LABEL_PROTECTED":           {"DASH_PLUGINS_KUBERNETES_CONVENTIONS_PROTECTED_LABEL"},
	"DASH_LABEL_TENANT":              {"DASH_PLUGINS_KUBERNETES_CONVENTIONS_TENANT_LABEL"},
	"DASH_APPS_FLUX_PREFIX":          {"DASH_PLUGINS_KUBERNETES_CONVENTIONS_APPS_FLUX_PREFIX"},
	"DASH_ANNOTATION_SUSPEND_REASON": {"DASH_PLUGINS_FLUX_SUSPEND_REASON_ANNOTATION"},
	"DASH_ZFS_SCRUB_METRIC_PREFIX":   {"DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX"},
	"DASH_NAS_BACKUP_NAMESPACE":      {"DASH_PLUGINS_KUBERNETES_BACKUP_JOBS"},
	"DASH_NAS_BACKUP_CRONJOB":        {"DASH_PLUGINS_KUBERNETES_BACKUP_JOBS"},
}

func TestRemovedTableCoversEveryOldAlias(t *testing.T) {
	if len(removedNames) != 24 {
		t.Fatalf("the frozen list has %d names, want 24", len(removedNames))
	}
	got := map[string]string{}
	for _, r := range removedEnv {
		if _, dup := got[r.name]; dup {
			t.Errorf("%s is listed twice", r.name)
		}
		got[r.name] = r.use
	}
	for name, uses := range removedNames {
		use, ok := got[name]
		if !ok {
			t.Errorf("%s is not in removedEnv", name)
			continue
		}
		for _, u := range uses {
			if !strings.Contains(use, u) {
				t.Errorf("%s: replacement %q does not name %s", name, use, u)
			}
		}
	}
	for name := range got {
		if _, ok := removedNames[name]; !ok {
			t.Errorf("removedEnv lists %s, which was never an alias", name)
		}
	}
}

// Every variable a replacement text names is a real generated name, so the advice cannot rot.
func TestRemovedReplacementsAreRealVariables(t *testing.T) {
	for _, r := range removedEnv {
		names := envNameRE.FindAllString(r.use, -1)
		if len(names) == 0 {
			t.Errorf("%s: no replacement named in %q", r.name, r.use)
		}
		for _, n := range names {
			if schema.byEnv[n] == nil {
				t.Errorf("%s: %s is not a generated variable", r.name, n)
			}
		}
	}
}

// A deployment that still sets a removed variable, or its _FILE form, does not start: ignoring
// it would turn a source off or drop a required setting without a word.
func TestRemovedVariablesFailStartup(t *testing.T) {
	const canary = "canary-value-1234"
	for name, uses := range removedNames {
		for _, file := range []bool{false, true} {
			variable := name
			if file {
				variable += "_FILE"
			}
			t.Run(variable, func(t *testing.T) {
				_, _, err := resolveEnv(t, variable+"="+canary)
				if err == nil {
					t.Fatal("expected startup to fail")
				}
				msg := err.Error()
				if !strings.Contains(msg, "removed in v0.5.0") || !strings.Contains(msg, "  "+variable+" -> ") {
					t.Errorf("message does not name %s:\n%s", variable, msg)
				}
				for _, u := range uses {
					want := u
					if file {
						want += "_FILE"
					}
					if !strings.Contains(msg, want) {
						t.Errorf("message does not name the replacement %s:\n%s", want, msg)
					}
				}
				if strings.Contains(msg, canary) {
					t.Errorf("the value was echoed:\n%s", msg)
				}
			})
		}
	}
}

// All offenders come in one message, so an operator fixes the manifest once.
func TestRemovedVariablesAreListedTogether(t *testing.T) {
	_, _, err := resolveEnv(t, "DASH_PUBLIC_URL=https://dash.example.com", "DASH_VM_URL=http://prom.example.com:9090",
		"DASH_PROXMOX_TOKEN_FILE=/run/secrets/pve", "DASH_PLUGINS_GRAFANA_URL=https://grafana.example.com")
	if err == nil {
		t.Fatal("expected startup to fail")
	}
	msg := err.Error()
	for _, want := range []string{
		"  DASH_PUBLIC_URL -> DASH_SERVER_PUBLIC_URL",
		"  DASH_VM_URL -> DASH_PLUGINS_PROMETHEUS_URL",
		"  DASH_PROXMOX_TOKEN_FILE -> DASH_PLUGINS_PROXMOX_TOKEN_ID_FILE and DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE",
		`see "Removed variable names" in the README`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "GRAFANA") {
		t.Errorf("a generated name is not an offender:\n%s", msg)
	}
}

// The old value is two variables now: the message says so and never echoes the credential.
func TestDashProxmoxTokenMessageMentionsBothVariables(t *testing.T) {
	_, _, err := resolveEnv(t, "DASH_PROXMOX_URL=https://pve.example.com:8006", "DASH_PROXMOX_TOKEN=dash@pve!ci=canary-secret")
	if err == nil {
		t.Fatal("expected startup to fail")
	}
	msg := err.Error()
	for _, want := range []string{"DASH_PLUGINS_PROXMOX_TOKEN_ID", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "user@realm!name=secret"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "canary") {
		t.Errorf("the credential was echoed:\n%s", msg)
	}
}

// KUBECONFIG stays supported without a warning, and does not need to be set via a DASH_ name.
// It is the only variable outside dash's own namespace that the environment layer reads.
func TestKubeconfigStillHonoured(t *testing.T) {
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

// KUBECONFIG belongs to kubectl and client-go, which have no _FILE form: KUBECONFIG_FILE is
// not ours to interpret, alone or next to KUBECONFIG.
func TestKubeconfigHasNoFileForm(t *testing.T) {
	res, log, err := resolveEnv(t, "KUBECONFIG=/a", "KUBECONFIG_FILE=/never/read")
	if err != nil || res.Config.Kubeconfig != "/a" {
		t.Errorf("both set: kubeconfig %q, err %v", res.Config.Kubeconfig, err)
	}
	res, _, err = resolveEnv(t, "KUBECONFIG_FILE=/never/read")
	if err != nil || res.Config.Kubeconfig != "" {
		t.Errorf("KUBECONFIG_FILE alone: kubeconfig %q, err %v", res.Config.Kubeconfig, err)
	}
	if strings.Contains(log, "WARN") {
		t.Errorf("unexpected warning:\n%s", log)
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
// removed variable: each variable has exactly one meaning.
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
	for _, r := range removedEnv {
		claim(r.name, "removed "+r.name)
		claim(r.name+"_FILE", "removed "+r.name+" (file)")
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

func TestEnvBeatsTheFileLayer(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte("plugins: {grafana: {url: 'https://file.example.com'}}\n")}
	log, _ := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_GRAFANA_URL=https://new.example.com"},
		File:    file, Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	if g := res.Tree.Plugins.Grafana.URL; g != "https://new.example.com" || res.Provenance["plugins.grafana.url"].Ref != "DASH_PLUGINS_GRAFANA_URL" {
		t.Errorf("grafana %q, %+v", g, res.Provenance["plugins.grafana.url"])
	}
	res, _ = Resolve(Sources{Environ: []string{"DASH_DEMO=true"}, File: file, Log: log})
	if g := res.Tree.Plugins.Grafana.URL; g != "https://file.example.com" {
		t.Errorf("without env the file value applies: %q", g)
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
	t.Run("both set is an error naming both", func(t *testing.T) {
		p := write("v", "https://file.example.com")
		for _, pair := range [][2]string{
			{"DASH_PLUGINS_GRAFANA_URL", "DASH_PLUGINS_GRAFANA_URL_FILE"},
			{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE"},
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
		if f := res.Tree.Plugins.Launcher.File; err != nil || f != "/etc/dash/launcher.yaml" {
			t.Errorf("launcher file %q, err %v", f, err)
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

// A DASH_ name that is neither generated nor removed is still ignored with a warning, and a
// removed name set empty counts as unset (it does not warn either).
func TestUnknownDashVariableStillWarns(t *testing.T) {
	_, log, err := resolveEnv(t, "DASH_LISTN=127.0.0.1:1", "DASH_VM_URL=", "DASH_VM_URL_FILE=")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(log, "ignoring unknown environment variable"); n != 1 || !strings.Contains(log, "name=DASH_LISTN") {
		t.Errorf("want one warning, for DASH_LISTN:\n%s", log)
	}
}

// The table has no LEGACY column: the aliases are gone, and a removed name is not a setting.
func TestEnvTableHasNoLegacyColumn(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteEnvTable(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "LEGACY") || strings.Contains(out, "deprecat") {
		t.Errorf("the table still talks about legacy names:\n%s", out)
	}
	for _, r := range removedEnv {
		if strings.Contains(out, r.name+" ") {
			t.Errorf("the table lists the removed %s", r.name)
		}
	}
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
	if v := byPath["plugins.proxmox.tokenSecret"]; v.Name != "DASH_PLUGINS_PROXMOX_TOKEN_SECRET" || !v.Secret {
		t.Errorf("tokenSecret row: %+v", v)
	}
	if v := byPath["server.listen"]; !v.Restart || v.Secret || v.Type != "string" {
		t.Errorf("listen row: %+v", v)
	}
	if v := byPath["plugins.proxmox.enabled"]; v.Type != "boolean" || v.Name != "DASH_PLUGINS_PROXMOX_ENABLED" || v.Default != "" {
		t.Errorf("enabled row: %+v", v)
	}
	if v := byPath["plugins.hardware.hosts"]; v.Type != "list of objects (JSON)" {
		t.Errorf("hosts row: %+v", v)
	}

	var buf bytes.Buffer
	if err := WriteEnvTable(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if v := byPath["settings.ui"]; !v.Restart {
		t.Errorf("settings.ui is read once at startup: %+v", v)
	}
	for _, want := range []string{"PATH", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "DASH_DATA_DIR", "KUBECONFIG", "_FILE", "applies live for prometheus", "plugins that use or require it", "proxmox, hardware, kubernetes or flux"} {
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
