package config

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// The settings of the sections that live in pluginconf, as the operator meets them: the
// variable, the type, the default and whether a restart applies them.
func TestPluginconfSectionsInTheEnvTable(t *testing.T) {
	byEnv := map[string]EnvVar{}
	for _, v := range EnvVars() {
		byEnv[v.Name] = v
	}
	type row struct {
		path, typ, def  string
		secret, restart bool
	}
	for name, want := range map[string]row{
		"DASH_PLUGINS_PROMETHEUS_URL":                      {"plugins.prometheus.url", "string", "", false, false},
		"DASH_PLUGINS_PROMETHEUS_TIMEOUT":                  {"plugins.prometheus.timeout", "duration (e.g. 30s)", "5s", false, false},
		"DASH_PLUGINS_PROMETHEUS_AUTH_BASIC_USERNAME":      {"plugins.prometheus.auth.basic.username", "string", "", false, false},
		"DASH_PLUGINS_PROMETHEUS_AUTH_BASIC_PASSWORD":      {"plugins.prometheus.auth.basic.password", "string", "", true, false},
		"DASH_PLUGINS_PROMETHEUS_AUTH_BEARER_TOKEN":        {"plugins.prometheus.auth.bearerToken", "string", "", true, false},
		"DASH_PLUGINS_PROMETHEUS_HEADERS":                  {"plugins.prometheus.headers", "object (JSON)", "", false, false},
		"DASH_PLUGINS_PROMETHEUS_TLS_CA_FILE":              {"plugins.prometheus.tls.caFile", "string", "", false, false},
		"DASH_PLUGINS_PROMETHEUS_TLS_INSECURE_SKIP_VERIFY": {"plugins.prometheus.tls.insecureSkipVerify", "boolean", "", false, false},
		"DASH_PLUGINS_ALERTMANAGER_URL":                    {"plugins.alertmanager.url", "string", "", false, false},
		"DASH_PLUGINS_ALERTMANAGER_AUTH_BEARER_TOKEN":      {"plugins.alertmanager.auth.bearerToken", "string", "", true, false},
		"DASH_PLUGINS_ALERTMANAGER_IGNORE_ALERTS":          {"plugins.alertmanager.ignoreAlerts", "list of strings (JSON)", `["Watchdog","InfoInhibitor"]`, false, false},
		"DASH_PLUGINS_ALERTMANAGER_SEVERITY_MAP":           {"plugins.alertmanager.severityMap", "object (JSON)", `{"critical":"crit","warning":"warn"}`, false, false},
		"DASH_PLUGINS_ALERTMANAGER_SILENCE_DURATIONS":      {"plugins.alertmanager.silenceDurations", "list of strings (JSON)", `["1h","4h","24h","7d"]`, false, false},
		"DASH_PLUGINS_ALERTMANAGER_PROTECTED_ALERTS":       {"plugins.alertmanager.protectedAlerts", "list of strings (JSON)", `["Watchdog"]`, false, false},
		"DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED":        {"plugins.alertmanager.actions.enabled", "boolean", "", false, false},
		"DASH_PLUGINS_GATUS_URL":                           {"plugins.gatus.url", "string", "", false, false},
		"DASH_PLUGINS_GATUS_CRITICAL_GROUPS":               {"plugins.gatus.criticalGroups", "list of strings (JSON)", "", false, false},
		"DASH_PLUGINS_GATUS_PAGE_SIZE":                     {"plugins.gatus.pageSize", "integer", "30", false, false},
		"DASH_PLUGINS_VICTORIALOGS_URL":                    {"plugins.victorialogs.url", "string", "", false, false},
		"DASH_PLUGINS_VICTORIALOGS_FIELDS_NAMESPACE":       {"plugins.victorialogs.fields.namespace", "string", "kubernetes.pod_namespace", false, false},
		"DASH_PLUGINS_VICTORIALOGS_FIELDS_POD":             {"plugins.victorialogs.fields.pod", "string", "kubernetes.pod_name", false, false},
		"DASH_PLUGINS_VICTORIALOGS_FIELDS_CONTAINER":       {"plugins.victorialogs.fields.container", "string", "kubernetes.container_name", false, false},
		"DASH_PLUGINS_VICTORIALOGS_LIMIT":                  {"plugins.victorialogs.limit", "integer", "500", false, false},
		"DASH_PLUGINS_VICTORIALOGS_DENY_NAMESPACES":        {"plugins.victorialogs.denyNamespaces", "list of strings (JSON)", "", false, false},
		"DASH_PLUGINS_GRAFANA_URL":                         {"plugins.grafana.url", "string", "", false, false},
		"DASH_PLUGINS_HEADLAMP_URL":                        {"plugins.headlamp.url", "string", "", false, false},
		"DASH_PLUGINS_HEADLAMP_CLUSTER":                    {"plugins.headlamp.cluster", "string", "main", false, false},
		"DASH_PLUGINS_LAUNCHER_FILE":                       {"plugins.launcher.file", "string", "", false, false},
		"DASH_PLUGINS_LAUNCHER_LINKS":                      {"plugins.launcher.links", "list of objects (JSON)", "", false, false},
		"DASH_UI_ICON_BASE_URL":                            {"ui.iconBaseURL", "string", "", false, true},
	} {
		got, ok := byEnv[name]
		if !ok {
			t.Errorf("%s is not a setting", name)
			continue
		}
		have := row{got.Path, got.Type, got.Default, got.Secret, got.Restart}
		if have != want {
			t.Errorf("%s = %+v, want %+v", name, have, want)
		}
		if got.Description == "" {
			t.Errorf("%s has no description", name)
		}
	}
	// Every plugin section keeps its type and enabled settings.
	for _, id := range []string{"prometheus", "alertmanager", "gatus", "victorialogs", "grafana", "headlamp", "launcher"} {
		for _, leaf := range []string{"TYPE", "ENABLED"} {
			if _, ok := byEnv["DASH_PLUGINS_"+strings.ToUpper(id)+"_"+leaf]; !ok {
				t.Errorf("plugins.%s lost %s", id, leaf)
			}
		}
	}
}

func TestPluginconfDefaultsAndTheFlatView(t *testing.T) {
	tree := DefaultTree()
	p := tree.Plugins
	if p.Prometheus.Timeout != pluginconf.Duration(5*time.Second) || p.Gatus.PageSize != 30 || p.VictoriaLogs.Limit != 500 ||
		p.Headlamp.Cluster != "main" || p.Alertmanager.Actions.Enabled ||
		!slices.Equal(p.Alertmanager.ProtectedAlerts, []string{"Watchdog"}) || tree.UI.IconBaseURL != "" {
		t.Errorf("defaults = %+v / %+v", p, tree.UI)
	}
	if err := validate(tree, false, true, nil); err != nil {
		t.Errorf("the defaults are invalid: %v", err)
	}

	res, _, err := resolveWith(t, Sources{Environ: []string{
		"DASH_DEMO=true",
		"DASH_PLUGINS_PROMETHEUS_URL=http://prom.example.com:9090/",
		"DASH_PLUGINS_ALERTMANAGER_URL=http://am.example.com:9093",
		"DASH_PLUGINS_GATUS_URL=http://gatus.example.com",
		"DASH_PLUGINS_VICTORIALOGS_URL=http://vl.example.com:9428",
		"DASH_PLUGINS_GRAFANA_URL=https://grafana.example.com",
		"DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com",
		"DASH_PLUGINS_HEADLAMP_CLUSTER=lab",
		"DASH_UI_ICON_BASE_URL= https://cdn.example.com/icons/png/ ",
	}})
	if err != nil {
		t.Fatal(err)
	}
	c := res.Config
	if res.Tree.Plugins.Alertmanager.URL != "http://am.example.com:9093" || c.GatusURL != "http://gatus.example.com" ||
		res.Tree.Plugins.Prometheus.URL != "http://prom.example.com:9090" ||
		c.VLogsURL != "http://vl.example.com:9428" || res.Tree.Plugins.Grafana.URL != "https://grafana.example.com" ||
		res.Tree.Plugins.Headlamp.URL != "https://headlamp.example.com" || res.Tree.Plugins.Headlamp.Cluster != "lab" {
		t.Errorf("flat view = %+v", c)
	}
	if c.IconBaseURL != "https://cdn.example.com/icons/png" {
		t.Errorf("IconBaseURL = %q, want the trimmed value of ui.iconBaseURL", c.IconBaseURL)
	}
}

func TestPluginconfSettingsFromFileAndEnvironment(t *testing.T) {
	file := &File{Path: "c.yaml", Data: []byte(`
ui:
  iconBaseURL: https://cdn.example.com/png
plugins:
  prometheus:
    url: http://prom.example.com:9090
    timeout: 90s
    headers: {X-Scope-OrgID: tenant-1}
    auth: {basic: {username: bob, password: ` + canary + `}}
    tls: {caFile: /etc/ssl/ca.pem}
  alertmanager:
    actions: {enabled: true}
    severityMap: {page: crit}
    silenceDurations: [1h, 24h]
  gatus:
    criticalGroups: [infrastructure]
    pageSize: 10
  victorialogs:
    fields: {namespace: ns, pod: p, container: c}
    limit: 100
    denyNamespaces: [kube-system]
`)}
	res, _, err := resolveWith(t, Sources{Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_GATUS_PAGE_SIZE=50", `DASH_PLUGINS_PROMETHEUS_HEADERS={"X-Env":"1"}`}, File: file})
	if err != nil {
		t.Fatal(err)
	}
	p := res.Tree.Plugins
	if p.Prometheus.Timeout != pluginconf.Duration(90*time.Second) || p.Prometheus.Auth.Basic.Username != "bob" || p.Prometheus.Auth.Basic.Password.Reveal() != canary ||
		p.Prometheus.TLS.CAFile != "/etc/ssl/ca.pem" {
		t.Errorf("prometheus = %+v", p.Prometheus)
	}
	if !sameMap(p.Prometheus.Headers, map[string]string{"X-Env": "1"}) {
		t.Errorf("headers = %v, want the environment's object to replace the file's as a whole", p.Prometheus.Headers)
	}
	if !p.Alertmanager.Actions.Enabled || !sameMap(p.Alertmanager.SeverityMap, map[string]string{"page": "crit"}) ||
		!slices.Equal(p.Alertmanager.SilenceDurations, []string{"1h", "24h"}) || !slices.Equal(p.Alertmanager.IgnoreAlerts, []string{"Watchdog", "InfoInhibitor"}) {
		t.Errorf("alertmanager = %+v", p.Alertmanager)
	}
	if !slices.Equal(p.Gatus.CriticalGroups, []string{"infrastructure"}) || p.Gatus.PageSize != 50 {
		t.Errorf("gatus = %+v (the environment wins over the file)", p.Gatus)
	}
	if v := p.VictoriaLogs; v.Fields.Namespace != "ns" || v.Limit != 100 || !slices.Equal(v.DenyNamespaces, []string{"kube-system"}) {
		t.Errorf("victorialogs = %+v", v)
	}
	if res.Tree.UI.IconBaseURL != "https://cdn.example.com/png" {
		t.Errorf("ui = %+v", res.Tree.UI)
	}
	if got := res.Provenance["plugins.prometheus.auth.basic.password"]; got.Source != SourceFile {
		t.Errorf("password provenance = %+v", got)
	}
}

// A credential of a shared Auth is a secret like the Proxmox token: referenced, registered for
// scrubbing, and shown as metadata only.
func TestPluginconfSecretsAreSecrets(t *testing.T) {
	reg := secret.NewRegistry()
	path := writeSecretFile(t, canary+"-file\n")
	res, _, err := resolveWith(t, Sources{
		Environ: []string{
			"DASH_DEMO=true",
			"DASH_PLUGINS_ALERTMANAGER_AUTH_BEARER_TOKEN=" + canary,
			"DASH_PLUGINS_GATUS_AUTH_BASIC_USERNAME=u",
			"DASH_PLUGINS_GATUS_AUTH_BASIC_PASSWORD_FILE=" + path,
		},
		Registry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	am, g := res.Tree.Plugins.Alertmanager.Auth, res.Tree.Plugins.Gatus.Auth
	if am.BearerToken.Reveal() != canary || g.Basic.Password.Reveal() != canary+"-file" || g.Basic.Password.Ref() != "file:"+path {
		t.Fatalf("secrets not resolved: %v %v", am.BearerToken.IsSet(), g.Basic.Password.Ref())
	}
	b, err := json.Marshal(res.Tree)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b) + fmt.Sprintf("%v %+v %#v", res.Tree.Plugins, res.Tree.Plugins, res.Tree.Plugins)
	if strings.Contains(out, canary) {
		t.Error("the tree shows a secret")
	}
	if !strings.Contains(string(b), `"bearerToken":{"set":true}`) {
		t.Errorf("the tree shows secret metadata only: %s", b)
	}
	if got := reg.Scrub("upstream said " + canary); strings.Contains(got, canary) {
		t.Errorf("the registry does not know the bearer token: %q", got)
	}

	// The section a plugin receives holds the same handle, so a rotation still reaches it.
	for _, sec := range res.Tree.PluginSections() {
		if sec.Name != "alertmanager" {
			continue
		}
		cp := sec.Value.(*pluginconf.Alertmanager)
		if !am.BearerToken.Update("rotated") || cp.Auth.BearerToken.Reveal() != "rotated" {
			t.Error("a rotation did not reach the copy of the section")
		}
	}
}

// The copy of a section a plugin gets shares no map or list with the tree.
func TestPluginSectionsOwnTheirMapsAndLists(t *testing.T) {
	tree := DefaultTree()
	tree.Plugins.Prometheus.Headers = map[string]string{"X-A": "1"}
	for _, sec := range tree.PluginSections() {
		switch v := sec.Value.(type) {
		case *pluginconf.Prometheus:
			v.Headers["X-A"] = "changed"
			v.Headers["X-B"] = "new"
		case *pluginconf.Alertmanager:
			v.SeverityMap["critical"] = "changed"
			v.IgnoreAlerts[0] = "changed"
			v.SilenceDurations[0] = "changed"
		}
	}
	if !sameMap(tree.Plugins.Prometheus.Headers, map[string]string{"X-A": "1"}) {
		t.Errorf("headers = %v", tree.Plugins.Prometheus.Headers)
	}
	am := tree.Plugins.Alertmanager
	if am.SeverityMap["critical"] != "crit" || am.IgnoreAlerts[0] != "Watchdog" || am.SilenceDurations[0] != "1h" {
		t.Errorf("alertmanager = %+v", am)
	}
}

// Every rule of pluginconf is reported at its full path with the layer that set the value, and
// a setting is never reported twice.
func TestPluginconfValidationAtResolve(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		file string
		want []string // the first is the path, which must appear exactly once
	}{
		{"basic needs a password", []string{"DASH_PLUGINS_PROMETHEUS_AUTH_BASIC_USERNAME=bob"}, "",
			[]string{"plugins.prometheus.auth.basic.password"}},
		{"basic excludes bearer", []string{"DASH_PLUGINS_GATUS_AUTH_BASIC_USERNAME=bob", "DASH_PLUGINS_GATUS_AUTH_BASIC_PASSWORD=x", "DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN=y"}, "",
			[]string{"plugins.gatus.auth.bearerToken", "cannot be set together with basic"}},
		{"forbidden header", []string{`DASH_PLUGINS_PROMETHEUS_HEADERS={"Authorization":"x"}`}, "",
			[]string{"plugins.prometheus.headers", "(env DASH_PLUGINS_PROMETHEUS_HEADERS)", `"Authorization"`}},
		{"header name", nil, "plugins: {prometheus: {headers: {'a b': c}}}",
			[]string{"plugins.prometheus.headers", "(file c.yaml)", `"a b"`}},
		{"silence duration", []string{`DASH_PLUGINS_ALERTMANAGER_SILENCE_DURATIONS=["1h","2h"]`}, "",
			[]string{"plugins.alertmanager.silenceDurations", "(env DASH_PLUGINS_ALERTMANAGER_SILENCE_DURATIONS)", `"2h"`}},
		{"page size", []string{"DASH_PLUGINS_GATUS_PAGE_SIZE=0"}, "",
			[]string{"plugins.gatus.pageSize", "(env DASH_PLUGINS_GATUS_PAGE_SIZE)", "greater than zero"}},
		{"field name", nil, "plugins: {victorialogs: {fields: {pod: 'a\" or \"1'}}}",
			[]string{"plugins.victorialogs.fields.pod", "(file c.yaml)"}},
		{"limit", []string{"DASH_PLUGINS_VICTORIALOGS_LIMIT=-1"}, "",
			[]string{"plugins.victorialogs.limit", "(env DASH_PLUGINS_VICTORIALOGS_LIMIT)"}},
		{"launcher link", nil, "plugins: {launcher: {links: [{name: x, href: 'javascript:alert(1)'}]}}",
			[]string{"plugins.launcher.links", "(file c.yaml)", "entry 1 needs a name and an http(s) href"}},
		{"launcher file and links", []string{"DASH_PLUGINS_LAUNCHER_FILE=/x.yaml"}, "plugins: {launcher: {links: [{name: x, href: 'https://x.example.com'}]}}",
			[]string{"plugins.launcher.links", "cannot be set together with plugins.launcher.file"}},
		{"icon base with a query", []string{"DASH_UI_ICON_BASE_URL=https://cdn.example.com/png?v=1"}, "",
			[]string{"ui.iconBaseURL", "(env DASH_UI_ICON_BASE_URL)", "without query or fragment"}},
		{"icon base not http", []string{"DASH_UI_ICON_BASE_URL=ftp://cdn.example.com"}, "",
			[]string{"ui.iconBaseURL", "(env DASH_UI_ICON_BASE_URL)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := Sources{Environ: append([]string{"DASH_DEMO=true"}, tc.env...)}
			if tc.file != "" {
				src.File = &File{Path: "c.yaml", Data: []byte(tc.file + "\n")}
			}
			_, _, err := resolveWith(t, src)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if n := len(regexp.MustCompile(regexp.QuoteMeta(tc.want[0])+`[ :]`).FindAllString(err.Error(), -1)); n != 1 {
				t.Errorf("%s is reported %d times: %v", tc.want[0], n, err)
			}
		})
	}
}

// A section reports every problem it has, also several at one path; only a path that already
// failed before the section ran (the generic checks) is not reported again.
func TestSectionReportsEveryProblemAtOnePath(t *testing.T) {
	src := Sources{
		Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_LAUNCHER_FILE=/x.yaml"},
		File:    &File{Path: "c.yaml", Data: []byte("plugins: {launcher: {links: [{name: x, href: 'javascript:alert(1)'}]}}\n")},
	}
	_, _, err := resolveWith(t, src)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, w := range []string{"cannot be set together with plugins.launcher.file", "entry 1 needs a name and an http(s) href"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not report %q", err, w)
		}
	}
	if n := strings.Count(err.Error(), "plugins.launcher.links"); n != 2 {
		t.Errorf("plugins.launcher.links is reported %d times, want 2: %v", n, err)
	}
}

// An inline launcher link that is invalid is rejected by validation (so by a reload too, which
// keeps the last good configuration); the file is not read by the configuration at all: the
// launcher plugin reads it at Start, so an unreadable file does not fail Resolve.
func TestLauncherValidationSplitsStaticAndFileChecks(t *testing.T) {
	src := Sources{Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_LAUNCHER_FILE=/missing/launcher.yaml"}}
	if _, _, err := resolveWith(t, src); err != nil {
		t.Fatalf("Resolve reads no launcher file: %v", err)
	}

	tree := DefaultTree()
	tree.Plugins.Launcher.Links = []model.LauncherLink{{Name: "x", Href: "javascript:alert(1)"}}
	if err := validate(tree, false, true, nil); err == nil || !strings.Contains(err.Error(), "plugins.launcher.links") {
		t.Errorf("validate = %v", err)
	}
}
