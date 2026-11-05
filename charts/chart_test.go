// Package charts_test renders the Helm chart in charts/dash with the helm that mise.toml pins
// and checks the chart's contract (design D.4, P5.4) on the parsed manifests: the four value
// sets under charts/dash/ci lint and render, the render-time refusals, the Recreate strategy,
// the listen address, RBAC without patch unless an action is on, and the guardrail policy.
package charts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

const chartDir = "dash"

// valueSets are the value files under charts/dash/ci, which CI also lints, renders and checks
// with kubeconform.
var valueSets = []string{"default", "readonly", "actions", "sidecar", "direct"}

type obj = map[string]any

func helm(t *testing.T, args ...string) (string, error) {
	t.Helper()
	bin, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal("helm is not on PATH: run the tests through mise (mise exec -- go test ./charts), which pins it")
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// The error carries helm's stderr only: the refusal tests check it never repeats a value.
		return stdout.String(), fmt.Errorf("helm %s: %w: %s", args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

func valuesFile(set string) string { return filepath.Join(chartDir, "ci", set+"-values.yaml") }

// render runs helm template for the release dash in the namespace dash with a value set (none
// for "") and extra flags, and returns the parsed objects.
func render(t *testing.T, set string, extra ...string) ([]obj, error) {
	t.Helper()
	args := []string{"template", "dash", chartDir, "--namespace", "dash"}
	if set != "" {
		args = append(args, "-f", valuesFile(set))
	}
	out, err := helm(t, append(args, extra...)...)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	var objs []obj
	for {
		var o obj
		if err := dec.Decode(&o); errors.Is(err, io.EOF) {
			return objs, nil
		} else if err != nil {
			t.Fatalf("helm template output does not parse: %v", err)
		}
		if len(o) > 0 {
			objs = append(objs, o)
		}
	}
}

func mustRender(t *testing.T, set string, extra ...string) []obj {
	t.Helper()
	objs, err := render(t, set, extra...)
	if err != nil {
		t.Fatalf("render %s %v: %v", set, extra, err)
	}
	return objs
}

func ofKind(objs []obj, kind string) []obj {
	var out []obj
	for _, o := range objs {
		if o["kind"] == kind {
			out = append(out, o)
		}
	}
	return out
}

func one(t *testing.T, objs []obj, kind string) obj {
	t.Helper()
	found := ofKind(objs, kind)
	if len(found) != 1 {
		t.Fatalf("%d %s objects, want 1", len(found), kind)
	}
	return found[0]
}

// dig follows map keys (string) and list indexes (int); nil when the path is missing.
func dig(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(obj)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			l, ok := v.([]any)
			if !ok || k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func list(v any) []any { l, _ := v.([]any); return l }

func strs(v any) []string {
	var out []string
	for _, e := range list(v) {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func podSpec(t *testing.T, objs []obj) obj {
	t.Helper()
	spec, _ := dig(one(t, objs, "Deployment"), "spec", "template", "spec").(obj)
	return spec
}

func container(t *testing.T, objs []obj, name string) obj {
	t.Helper()
	for _, c := range list(podSpec(t, objs)["containers"]) {
		if dig(c, "name") == name {
			return c.(obj)
		}
	}
	t.Fatalf("no container %s", name)
	return nil
}

func hasContainer(t *testing.T, objs []obj, name string) bool {
	t.Helper()
	return slices.ContainsFunc(list(podSpec(t, objs)["containers"]), func(c any) bool { return dig(c, "name") == name })
}

func env(c obj) map[string]any {
	out := map[string]any{}
	for _, e := range list(c["env"]) {
		m := e.(obj)
		if v, ok := m["value"]; ok {
			out[m["name"].(string)] = v
		} else {
			out[m["name"].(string)] = m["valueFrom"]
		}
	}
	return out
}

func volume(t *testing.T, objs []obj, name string) obj {
	t.Helper()
	for _, v := range list(podSpec(t, objs)["volumes"]) {
		if dig(v, "name") == name {
			return v.(obj)
		}
	}
	t.Fatalf("no volume %s", name)
	return nil
}

func mountPath(c obj, volume string) string {
	for _, m := range list(c["volumeMounts"]) {
		if dig(m, "name") == volume {
			return fmt.Sprint(dig(m, "mountPath"))
		}
	}
	return ""
}

func containerPort(c obj, name string) any {
	for _, p := range list(c["ports"]) {
		if dig(p, "name") == name {
			return dig(p, "containerPort")
		}
	}
	return nil
}

// dashConfig parses config.yaml from the ConfigMap the pod mounts at /etc/dash.
func dashConfig(t *testing.T, objs []obj) obj {
	t.Helper()
	cm := one(t, objs, "ConfigMap")
	var cfg obj
	if err := sigsyaml.Unmarshal([]byte(dig(cm, "data", "config.yaml").(string)), &cfg); err != nil {
		t.Fatalf("config.yaml: %v", err)
	}
	return cfg
}

// rules collects the rules of every ClusterRole and Role.
func rules(objs []obj) []obj {
	var out []obj
	for _, o := range objs {
		if o["kind"] == "ClusterRole" || o["kind"] == "Role" {
			for _, r := range list(o["rules"]) {
				out = append(out, r.(obj))
			}
		}
	}
	return out
}

// grants reports whether some rule gives verb on resource in group.
func grants(objs []obj, group, resource, verb string) bool {
	return slices.ContainsFunc(rules(objs), func(r obj) bool {
		return slices.Contains(strs(r["apiGroups"]), group) && slices.Contains(strs(r["resources"]), resource) &&
			slices.Contains(strs(r["verbs"]), verb)
	})
}

func anyVerb(objs []obj, verb string) bool {
	return slices.ContainsFunc(rules(objs), func(r obj) bool {
		return slices.Contains(strs(r["verbs"]), verb) || slices.Contains(strs(r["verbs"]), "*")
	})
}

func TestValueSetsLintAndRender(t *testing.T) {
	for _, set := range valueSets {
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			if _, err := helm(t, "lint", "--strict", chartDir, "-f", valuesFile(set)); err != nil {
				t.Fatal(err)
			}
			objs := mustRender(t, set)
			for _, kind := range []string{"Deployment", "Service", "ConfigMap", "ServiceAccount"} {
				one(t, objs, kind)
			}
		})
	}
}

func TestDeploymentIsRecreate(t *testing.T) {
	for _, set := range valueSets {
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			if got := dig(one(t, mustRender(t, set), "Deployment"), "spec", "strategy", "type"); got != "Recreate" {
				t.Errorf("strategy.type = %v, want Recreate", got)
			}
		})
	}
}

func TestListenIsExplicit(t *testing.T) {
	for _, set := range valueSets {
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			objs := mustRender(t, set)
			dash := container(t, objs, "dash")
			want, port := "0.0.0.0:8080", any(float64(8080))
			switch set {
			case "sidecar":
				want, port = "127.0.0.1:8080", nil // nothing outside the pod reaches dash directly
			case "direct":
				port = nil // dash's port is named direct; http is the proxy's
			}
			if got := env(dash)["DASH_SERVER_LISTEN"]; got != want {
				t.Errorf("DASH_SERVER_LISTEN = %v, want %s", got, want)
			}
			if got := containerPort(dash, "http"); got != port {
				t.Errorf("dash's http port = %v, want %v", got, port)
			}
			if got := env(dash)["DASH_SERVER_HEALTH_LISTEN"]; got != "0.0.0.0:8081" {
				t.Errorf("DASH_SERVER_HEALTH_LISTEN = %v", got)
			}
		})
	}
}

func TestPodProbesAndSecurity(t *testing.T) {
	objs := mustRender(t, "default")
	dash := container(t, objs, "dash")
	if got := containerPort(dash, "health"); got != float64(8081) {
		t.Errorf("health port = %v, want 8081", got)
	}
	for probe, path := range map[string]string{"livenessProbe": "/healthz", "readinessProbe": "/readyz", "startupProbe": "/readyz"} {
		if got := dig(dash, probe, "httpGet", "path"); got != path {
			t.Errorf("%s path = %v, want %s", probe, got, path)
		}
		if got := dig(dash, probe, "httpGet", "port"); got != "health" {
			t.Errorf("%s port = %v, want health", probe, got)
		}
	}
	pod := podSpec(t, objs)
	for _, c := range []struct {
		v    any
		want any
	}{
		{dig(pod, "securityContext", "runAsNonRoot"), true},
		{dig(pod, "securityContext", "seccompProfile", "type"), "RuntimeDefault"},
		{dig(pod, "securityContext", "fsGroup"), float64(65532)},
		{dig(dash, "securityContext", "readOnlyRootFilesystem"), true},
		{dig(dash, "securityContext", "allowPrivilegeEscalation"), false},
		{dig(dash, "securityContext", "capabilities", "drop", 0), "ALL"},
		{pod["enableServiceLinks"], false},
	} {
		if c.v != c.want {
			t.Errorf("got %v, want %v", c.v, c.want)
		}
	}
}

func TestConfigMapAndSecretKey(t *testing.T) {
	objs := mustRender(t, "readonly")
	dash := container(t, objs, "dash")
	e := env(dash)

	if e["DASH_CONFIG"] != "/etc/dash/config.yaml" || mountPath(dash, "config") != "/etc/dash" {
		t.Errorf("DASH_CONFIG = %v, config mounted at %q", e["DASH_CONFIG"], mountPath(dash, "config"))
	}
	if got, want := dig(volume(t, objs, "config"), "configMap", "name"), dig(one(t, objs, "ConfigMap"), "metadata", "name"); got != want {
		t.Errorf("config volume = %v, ConfigMap = %v", got, want)
	}
	cfg := dashConfig(t, objs)
	if got := dig(cfg, "plugins", "proxmox", "tokenSecret"); got != "env:PROXMOX_TOKEN_SECRET" {
		t.Errorf("config.yaml proxmox.tokenSecret = %v", got)
	}
	if got := dig(e["PROXMOX_TOKEN_SECRET"], "secretKeyRef", "name"); got != "proxmox" {
		t.Errorf("secretEnv PROXMOX_TOKEN_SECRET from %v, want Secret proxmox", got)
	}

	if e["DASH_SECRET_KEY_FILE"] != "/run/secrets/dash/secret-key" || mountPath(dash, "secret-key") != "/run/secrets/dash" {
		t.Errorf("DASH_SECRET_KEY_FILE = %v, secret-key mounted at %q", e["DASH_SECRET_KEY_FILE"], mountPath(dash, "secret-key"))
	}
	sk := volume(t, objs, "secret-key")
	if dig(sk, "secret", "secretName") != "dash-secret-key" || dig(sk, "secret", "items", 0, "key") != "secretKey" ||
		dig(sk, "secret", "items", 0, "path") != "secret-key" {
		t.Errorf("secret-key volume = %v", sk)
	}
	if _, ok := e["DASH_SECRET_KEY"]; ok {
		t.Error("the secret key must come from the file, not the environment")
	}
	other := volume(t, mustRender(t, "default", "--set", "secretKey.key=other"), "secret-key")
	if got := dig(other, "secret", "items", 0, "key"); got != "other" {
		t.Errorf("secret-key volume key = %v with secretKey.key=other", got)
	}
}

// The pod carries a checksum of the config file, so a config change restarts it; nothing else
// moves the checksum.
func TestConfigChecksum(t *testing.T) {
	sum := func(extra ...string) any {
		return dig(one(t, mustRender(t, "default", extra...), "Deployment"), "spec", "template", "metadata", "annotations", "checksum/config")
	}
	base := sum()
	if s, _ := base.(string); len(s) != 64 {
		t.Fatalf("checksum/config = %v, want a sha256", base)
	}
	if got := sum("--set", "config.ui.iconBaseURL=https://icons.example.com"); got == base {
		t.Error("the checksum did not change with config")
	}
	if got := sum("--set", "resources.limits.memory=128Mi", "--set", "podLabels.team=x"); got != base {
		t.Errorf("the checksum changed without a config change: %v, was %v", got, base)
	}
}

func TestSettingsAndPersistence(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		objs := mustRender(t, "default")
		if got := env(container(t, objs, "dash"))["DASH_SETTINGS_UI"]; got != "editable" {
			t.Errorf("DASH_SETTINGS_UI = %v, want editable", got)
		}
		pvc := one(t, objs, "PersistentVolumeClaim")
		if got := strs(dig(pvc, "spec", "accessModes")); !slices.Equal(got, []string{"ReadWriteOnce"}) {
			t.Errorf("accessModes = %v", got)
		}
		if got, want := dig(volume(t, objs, "data"), "persistentVolumeClaim", "claimName"), dig(pvc, "metadata", "name"); got != want {
			t.Errorf("data volume claim = %v, PVC = %v", got, want)
		}
		if _, ok := dig(pvc, "spec").(obj)["storageClassName"]; ok {
			t.Error("storageClassName rendered without persistence.storageClassName (it would override the default StorageClass)")
		}
		if got := dig(one(t, mustRender(t, "default", "--set", "persistence.storageClassName=fast"), "PersistentVolumeClaim"), "spec", "storageClassName"); got != "fast" {
			t.Errorf("storageClassName = %v, want fast", got)
		}
		if got := mountPath(container(t, objs, "dash"), "data"); got != "/data" {
			t.Errorf("data mounted at %q", got)
		}
	})
	t.Run("ReadWriteOncePod", func(t *testing.T) {
		objs := mustRender(t, "default", "--set-json", `persistence.accessModes=["ReadWriteOncePod"]`)
		if got := strs(dig(one(t, objs, "PersistentVolumeClaim"), "spec", "accessModes")); !slices.Equal(got, []string{"ReadWriteOncePod"}) {
			t.Errorf("accessModes = %v", got)
		}
	})
	t.Run("readonly", func(t *testing.T) {
		objs := mustRender(t, "readonly")
		if got := env(container(t, objs, "dash"))["DASH_SETTINGS_UI"]; got != "readonly" {
			t.Errorf("DASH_SETTINGS_UI = %v, want readonly", got)
		}
		if n := len(ofKind(objs, "PersistentVolumeClaim")); n != 0 {
			t.Errorf("%d PVCs with persistence off", n)
		}
		if _, ok := volume(t, objs, "data")["emptyDir"]; !ok {
			t.Error("the data volume is not an emptyDir")
		}
		if got := dig(one(t, objs, "Deployment"), "spec", "replicas"); got != float64(2) {
			t.Errorf("replicas = %v, want 2", got)
		}
	})
}

func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   string
		extra []string
		want  string // "" renders
	}{
		{"chart defaults alone", "", nil, "secretKey.existingSecret is required"},
		{"empty existingSecret", "default", []string{"--set", "secretKey.existingSecret="}, "secretKey.existingSecret is required"},
		{"literal secret", "default", []string{"--set", "config.plugins.proxmox.tokenSecret=hunter2"}, "config.plugins.proxmox.tokenSecret is a secret and holds a literal"},
		{"literal secret behind a $ref", "default", []string{"--set", "config.plugins.gatus.auth.basic.password=hunter2"}, "config.plugins.gatus.auth.basic.password is a secret"},
		{"non-string literal secret", "default", []string{"--set-json", `config.auth.oidc={"clientSecret":1234}`}, "config.auth.oidc.clientSecret is a secret"},
		{"two replicas, editable, persistence on", "default", []string{"--set", "replicaCount=2", "--set", "config.auth.mode=proxy"}, "replicaCount > 1 needs settings.ui: readonly and persistence.enabled: false"},
		{"two replicas, editable, persistence off", "default", []string{"--set", "replicaCount=2", "--set", "persistence.enabled=false", "--set", "config.auth.mode=proxy"}, "replicaCount > 1 needs settings.ui: readonly and persistence.enabled: false"},
		{"two replicas, readonly, persistence on", "default", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "config.auth.mode=proxy"}, "replicaCount > 1 needs settings.ui: readonly and persistence.enabled: false"},
		{"two replicas, readonly, persistence off, local auth", "default", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "persistence.enabled=false"}, "replicaCount > 1 needs auth.mode proxy"},
		{"two replicas, readonly, persistence off, oidc auth", "default", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "persistence.enabled=false", "--set", "config.auth.mode=oidc"}, "replicaCount > 1 needs auth.mode proxy"},
		{"two replicas, readonly, persistence off, proxy auth", "default", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "persistence.enabled=false", "--set", "config.auth.mode=proxy"}, ""},
		{"two replicas, readonly, persistence off, sidecar", "sidecar", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "persistence.enabled=false"}, ""},
		{"a secretRefs namespace twice", "default", []string{"--set-json", `rbac.secretRefs=[{"namespace":"a","names":["x"]},{"namespace":"a","names":["y"]}]`}, "lists namespace a twice"},
		{"a secretRefs entry with empty names", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"namespace":"a","names":[]}]`}, "namespace a needs names"},
		{"a secretRefs entry without names", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"namespace":"a"}]`}, "namespace a needs names"},
		{"a secretRefs entry with an empty name", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"namespace":"a","names":[""]}]`}, "namespace a needs names"},
		{"a secretRefs entry without a namespace", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"names":["x"]}]`}, "entry without a namespace"},
		{"serviceAccount.create false without a name", "default", []string{"--set", "serviceAccount.create=false"}, "serviceAccount.name is empty"},
		{"serviceAccount.create false with a name", "default", []string{"--set", "serviceAccount.create=false", "--set", "serviceAccount.name=existing"}, ""},
		{"config.server.listen", "default", []string{"--set", "config.server.listen=127.0.0.1:8080"}, "config.server.listen is set by the chart"},
		{"config.server.healthListen", "default", []string{"--set", "config.server.healthListen=127.0.0.1:8081"}, "config.server.healthListen is set by the chart"},
		{"config.settings.ui", "default", []string{"--set", "config.settings.ui=readonly"}, "use the chart value settings.ui"},
		{"config.dataDir", "default", []string{"--set", "config.dataDir=/tmp/dash"}, "config.dataDir is set by the chart"},
		{"env DASH_SERVER_LISTEN", "default", []string{"--set-json", `env=[{"name":"DASH_SERVER_LISTEN","value":"127.0.0.1:8080"}]`}, "env sets DASH_SERVER_LISTEN, which the chart sets"},
		{"env DASH_SETTINGS_UI_FILE", "default", []string{"--set-json", `env=[{"name":"DASH_SETTINGS_UI_FILE","value":"/x"}]`}, "env sets DASH_SETTINGS_UI_FILE, which the chart sets"},
		{"env DASH_DATA_DIR", "default", []string{"--set-json", `env=[{"name":"DASH_DATA_DIR","value":"/tmp"}]`}, "env sets DASH_DATA_DIR, which the chart sets"},
		{"env DASH_CONFIG", "default", []string{"--set-json", `env=[{"name":"DASH_CONFIG","value":"/x"}]`}, "env sets DASH_CONFIG, which the chart sets"},
		{"env DASH_SERVER_HEALTH_LISTEN", "default", []string{"--set-json", `env=[{"name":"DASH_SERVER_HEALTH_LISTEN","value":"127.0.0.1:8081"}]`}, "env sets DASH_SERVER_HEALTH_LISTEN, which the chart sets"},
		{"secretEnv DASH_SECRET_KEY", "default", []string{"--set-json", `secretEnv=[{"name":"DASH_SECRET_KEY","secretKeyRef":{"name":"k","key":"k"}}]`}, "secretEnv sets DASH_SECRET_KEY, which the chart sets"},
		{"secretEnv DASH_SECRET_KEY_FILE", "default", []string{"--set-json", `secretEnv=[{"name":"DASH_SECRET_KEY_FILE","secretKeyRef":{"name":"k","key":"k"}}]`}, "secretEnv sets DASH_SECRET_KEY_FILE, which the chart sets"},
		{"env literal secret", "default", []string{"--set-json", `env=[{"name":"DASH_PLUGINS_PROXMOX_TOKEN_SECRET","value":"hunter2"}]`}, "env DASH_PLUGINS_PROXMOX_TOKEN_SECRET is a secret and holds a literal"},
		{"env literal oidc secret", "default", []string{"--set-json", `env=[{"name":"DASH_AUTH_OIDC_CLIENT_SECRET","value":"hunter2"}]`}, "env DASH_AUTH_OIDC_CLIENT_SECRET is a secret and holds a literal"},
		{"env literal previous secret key", "default", []string{"--set-json", `env=[{"name":"DASH_SECRET_KEY_PREVIOUS","value":"hunter2"}]`}, "env DASH_SECRET_KEY_PREVIOUS holds a literal: take it from a Secret (secretEnv), or mount the old key and set DASH_SECRET_KEY_PREVIOUS_FILE"},
		{"env previous secret key as a reference", "default", []string{"--set-json", `env=[{"name":"DASH_SECRET_KEY_PREVIOUS","value":"env:OLD_KEY"}]`}, "env DASH_SECRET_KEY_PREVIOUS holds a literal"},
		{"env DASH_DATA_DIR_FILE", "default", []string{"--set-json", `env=[{"name":"DASH_DATA_DIR_FILE","value":"/x"}]`}, "env sets DASH_DATA_DIR_FILE, which the chart sets"},
		{"sidecar --client-secret arg", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.args=["--client-secret=hunter2"]`}, "auth.oauth2ProxySidecar.args sets --client-secret: give OAUTH2_PROXY_CLIENT_SECRET through valueFrom"},
		{"sidecar --cookie-secret arg", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.args=["--cookie-secret","hunter2"]`}, "auth.oauth2ProxySidecar.args sets --cookie-secret: give OAUTH2_PROXY_COOKIE_SECRET through valueFrom"},
		{"sidecar --client-secret-file arg", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.args=["--client-secret-file=/run/secrets/oauth2/client-secret"]`}, ""},
		{"URL with a token as user name", "default", []string{"--set", "config.plugins.prometheus.url=https://hunter2@prometheus.example.com"}, "config.plugins.prometheus.url is a URL with credentials"},
		{"URL with userinfo after a space", "default", []string{"--set-json", `config.plugins.prometheus.url=" https://user:hunter2@prometheus.example.com"`}, "config.plugins.prometheus.url is a URL with credentials"},
		{"a secretRefs entry with a null name", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"namespace":"a","names":[null]}]`}, "namespace a needs names"},
		{"a secretRefs entry with a blank name", "default", []string{"--skip-schema-validation", "--set-json", `rbac.secretRefs=[{"namespace":"a","names":["x","  "]}]`}, "namespace a needs names"},
		{"auth.mode none with allowRemote from env", "default", []string{"--set", "config.auth.mode=none", "--set-json", `env=[{"name":"DASH_AUTH_NONE_ALLOW_REMOTE","value":"true"}]`}, ""},
		{"DASH_AUTH_MODE from secretEnv", "default", []string{"--set-json", `secretEnv=[{"name":"DASH_AUTH_MODE","secretKeyRef":{"name":"m","key":"m"}}]`}, "secretEnv sets DASH_AUTH_MODE from a Secret or a file"},
		{"DASH_AUTH_MODE from valueFrom in env", "default", []string{"--set-json", `env=[{"name":"DASH_AUTH_MODE","valueFrom":{"configMapKeyRef":{"name":"m","key":"m"}}}]`}, "env sets DASH_AUTH_MODE from a Secret or a file"},
		{"DASH_AUTH_MODE_FILE in env", "default", []string{"--set-json", `env=[{"name":"DASH_AUTH_MODE_FILE","value":"/x"}]`}, "env sets DASH_AUTH_MODE_FILE from a Secret or a file"},
		{"DASH_AUTH_MODE literal in env", "default", []string{"--set-json", `env=[{"name":"DASH_AUTH_MODE","value":"oidc"}]`}, ""},
		{"env secret reference", "default", []string{"--set-json", `env=[{"name":"DASH_PLUGINS_PROXMOX_TOKEN_SECRET","value":"env:PVE"}]`}, ""},
		{"env secret from a Secret", "default", []string{"--set-json", `env=[{"name":"DASH_SECRET_KEY_PREVIOUS","valueFrom":{"secretKeyRef":{"name":"k","key":"old"}}}]`}, ""},
		{"sidecar literal client secret", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.env=[{"name":"OAUTH2_PROXY_CLIENT_SECRET","value":"hunter2"}]`}, "auth.oauth2ProxySidecar.env OAUTH2_PROXY_CLIENT_SECRET holds a literal"},
		{"sidecar literal cookie secret", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.env=[{"name":"OAUTH2_PROXY_COOKIE_SECRET","value":"hunter2"}]`}, "auth.oauth2ProxySidecar.env OAUTH2_PROXY_COOKIE_SECRET holds a literal"},
		{"sidecar client secret from a Secret", "sidecar", []string{"--set-json", `auth.oauth2ProxySidecar.env=[{"name":"OAUTH2_PROXY_CLIENT_SECRET","valueFrom":{"secretKeyRef":{"name":"o","key":"c"}}}]`}, ""},
		{"URL with userinfo", "default", []string{"--set", "config.plugins.prometheus.url=https://user:hunter2@prometheus.example.com"}, "config.plugins.prometheus.url is a URL with credentials"},
		{"URL with userinfo in a list", "default", []string{"--set-json", `config.plugins.launcher.links=[{"name":"x","url":"https://u:hunter2@x.example.com/"}]`}, "config.plugins.launcher.links[0].url is a URL with credentials"},
		{"auth.mode none", "default", []string{"--set", "config.auth.mode=none"}, "config.auth.mode none without auth.oauth2ProxySidecar.enabled"},
		{"auth.mode none from env", "default", []string{"--set-json", `env=[{"name":"DASH_AUTH_MODE","value":"none"}]`}, "config.auth.mode none without auth.oauth2ProxySidecar.enabled"},
		{"auth.mode none with allowRemote", "default", []string{"--set", "config.auth.mode=none", "--set", "config.auth.none.allowRemote=true"}, ""},
		{"auth.mode none with the sidecar", "sidecar", []string{"--set", "config.auth.mode=none"}, ""},
		{"guardrails not auto, true or false", "default", []string{"--set", "guardrails.enabled=maybe"}, "guardrails/enabled"},
		{"ReadOnlyMany volume", "default", []string{"--set-json", `persistence.accessModes=["ReadOnlyMany"]`}, "persistence/accessModes"},
		{"routes.direct without the sidecar", "default", []string{"--set", "routes.direct.enabled=true", "--set", "routes.direct.host=dash-direct.example.com"}, "routes.direct.enabled needs auth.oauth2ProxySidecar.enabled"},
		{"routes.direct without a host", "direct", []string{"--set", "routes.direct.host="}, "routes.direct.enabled needs routes.direct.host"},
		{"routes.direct with a URL for a host", "direct", []string{"--set", "routes.direct.host=https://dash-direct.example.com"}, "routes/direct/host"},
		{"routes.direct with two replicas", "direct", []string{"--set", "replicaCount=2", "--set", "settings.ui=readonly", "--set", "persistence.enabled=false"}, "routes.direct.enabled needs replicaCount 1"},
		{"routes.direct with auth.mode none", "direct", []string{"--set", "config.auth.mode=none"}, "routes.direct.enabled with auth.mode none"},
		{"routes.direct with auth.mode local", "direct", []string{"--set", "config.auth.mode=local"}, ""},
		{"routes.direct with service.port 8082", "direct", []string{"--set", "service.port=8082"}, "service.port 8082 is the Service port of routes.direct"},
		{"routes.direct with config.server.directURL", "direct", []string{"--set", "config.server.directURL=https://other.example.com"}, "config.server.directURL is set by the chart with routes.direct"},
		{"routes.direct with trustedProxies", "direct", []string{"--set-json", `config.auth.proxy.trustedProxies=["10.0.0.0/8"]`}, "config.auth.proxy.trustedProxies is set by the chart with routes.direct"},
		{"routes.direct with allowAnyPeer", "direct", []string{"--set", "config.auth.proxy.allowAnyPeer=true"}, "config.auth.proxy.allowAnyPeer is set by the chart with routes.direct"},
		{"routes.direct with env DASH_SERVER_DIRECT_URL", "direct", []string{"--set-json", `env=[{"name":"DASH_SERVER_DIRECT_URL","value":"https://other.example.com"}]`}, "env sets DASH_SERVER_DIRECT_URL, which the chart sets with routes.direct"},
		{"routes.direct with secretEnv DASH_AUTH_PROXY_TRUSTED_PROXIES", "direct", []string{"--set-json", `secretEnv=[{"name":"DASH_AUTH_PROXY_TRUSTED_PROXIES","secretKeyRef":{"name":"p","key":"p"}}]`}, "secretEnv sets DASH_AUTH_PROXY_TRUSTED_PROXIES, which the chart sets with routes.direct"},
		{"routes.direct with env DASH_AUTH_PROXY_ALLOW_ANY_PEER_FILE", "direct", []string{"--set-json", `env=[{"name":"DASH_AUTH_PROXY_ALLOW_ANY_PEER_FILE","value":"/x"}]`}, "env sets DASH_AUTH_PROXY_ALLOW_ANY_PEER_FILE, which the chart sets with routes.direct"},
		{"routes.direct with config.server.listen", "direct", []string{"--set", "config.server.listen=0.0.0.0:8080"}, "config.server.listen is set by the chart"},
		{"routes.direct with env DASH_SERVER_LISTEN", "direct", []string{"--set-json", `env=[{"name":"DASH_SERVER_LISTEN","value":"0.0.0.0:8080"}]`}, "env sets DASH_SERVER_LISTEN, which the chart sets"},
		{"directURL and trustedProxies without routes.direct", "sidecar", []string{"--set", "config.server.directURL=https://dash-direct.example.com", "--set-json", `config.auth.proxy.trustedProxies=["127.0.0.1/32"]`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			objs, err := render(t, tc.set, tc.extra...)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want == "":
				one(t, objs, "Deployment")
			case err == nil:
				t.Fatalf("rendered; want a refusal naming %q", tc.want)
			case !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error does not say %q: %v", tc.want, err)
			case strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "1234"):
				t.Fatalf("the error repeats the secret: %v", err)
			}
		})
	}
}

// secretPaths walks dash's config schema the way the chart's guard does and returns a concrete
// path (map keys; 0 for a list item, "x" for a free-form map key) to every x-dash-secret field,
// with the JSON pointers of the schema nodes it reached them at.
func secretPaths(t *testing.T, root obj) (paths [][]any, reached map[string]bool) {
	t.Helper()
	reached = map[string]bool{}
	var walk func(s obj, ptr string, path []any)
	walk = func(s obj, ptr string, path []any) {
		if len(path) > 12 {
			t.Fatalf("schema nests deeper than 12 at %v", path)
		}
		if ref, ok := s["$ref"].(string); ok {
			ptr = strings.TrimPrefix(ref, "#")
			s = dig(root, "$defs", strings.TrimPrefix(ref, "#/$defs/")).(obj)
		}
		if _, ok := s["x-dash-secret"]; ok {
			paths = append(paths, slices.Clone(path))
			reached[ptr] = true
			return
		}
		if props, ok := s["properties"].(obj); ok {
			for k, v := range props {
				walk(v.(obj), ptr+"/properties/"+k, append(path, k))
			}
		}
		if ap, ok := s["additionalProperties"].(obj); ok {
			walk(ap, ptr+"/additionalProperties", append(path, "x"))
		}
		if it, ok := s["items"].(obj); ok {
			walk(it, ptr+"/items", append(path, 0))
		}
	}
	walk(root, "", nil)
	return paths, reached
}

// secretNodes lists the JSON pointer of every node of the schema that carries x-dash-secret,
// wherever it sits.
func secretNodes(v any, ptr string, out map[string]bool) {
	switch n := v.(type) {
	case obj:
		if _, ok := n["x-dash-secret"]; ok {
			out[ptr] = true
		}
		for k, c := range n {
			secretNodes(c, ptr+"/"+k, out)
		}
	case []any:
		for i, c := range n {
			secretNodes(c, fmt.Sprintf("%s/%d", ptr, i), out)
		}
	}
}

// nested builds the value at path, with lists of one item for list steps.
func nested(path []any, leaf any) any {
	if len(path) == 0 {
		return leaf
	}
	inner := nested(path[1:], leaf)
	if _, ok := path[0].(int); ok {
		return []any{inner}
	}
	return obj{path[0].(string): inner}
}

func displayPath(path []any) string {
	s := "config"
	for _, p := range path {
		if i, ok := p.(int); ok {
			s += fmt.Sprintf("[%d]", i)
		} else {
			s += "." + p.(string)
		}
	}
	return s
}

// Every secret field of the schema, not only the ones TestRefusals names, refuses a literal
// and takes the three references and the empty value.
func TestEverySchemaSecretRefusesALiteral(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(chartDir, "files", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema obj
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	paths, reached := secretPaths(t, schema)
	// The walk reaches every x-dash-secret node of the schema, so every secret field is rendered
	// below; a field the walk cannot reach would also be one the chart's guard cannot.
	all := map[string]bool{}
	secretNodes(schema, "", all)
	if len(all) == 0 || !maps.Equal(all, reached) {
		t.Fatalf("x-dash-secret nodes %v, the walk reached %v", slices.Sorted(maps.Keys(all)), slices.Sorted(maps.Keys(reached)))
	}
	var names []string
	for _, p := range paths {
		names = append(names, displayPath(p))
	}
	for _, want := range []string{"config.plugins.proxmox.tokenSecret", "config.auth.oidc.clientSecret"} {
		if !slices.Contains(names, want) {
			t.Fatalf("the schema walk misses %s: %v", want, names)
		}
	}
	for _, p := range paths {
		t.Run(displayPath(p), func(t *testing.T) {
			t.Parallel()
			for _, v := range []string{"literal", "env:A_SECRET", "file:/run/secrets/a", "k8s:dash/a#key", ""} {
				js, _ := json.Marshal(nested(p, v))
				_, err := render(t, "default", "--set-json", "config="+string(js))
				if refused := err != nil; refused != (v == "literal") {
					t.Errorf("%q: refused = %v (%v)", v, refused, err)
				} else if refused && !strings.Contains(err.Error(), displayPath(p)+" is a secret") {
					t.Errorf("%q: the error does not name the field: %v", v, err)
				}
			}
		})
	}
}

// The chart's copy of the schema is what `dash config schema` prints (make generate writes it;
// make check fails on a diff).
func TestChartSchemaMatchesDash(t *testing.T) {
	chart, err := os.ReadFile(filepath.Join(chartDir, "files", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("..", "internal", "config", "testdata", "config.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chart, golden) {
		t.Error("charts/dash/files/config.schema.json differs from dash config schema: run make generate")
	}
}

func TestRBAC(t *testing.T) {
	t.Run("no patch without actions", func(t *testing.T) {
		for _, set := range []string{"default", "readonly", "sidecar"} {
			objs := mustRender(t, set)
			if anyVerb(objs, "patch") {
				t.Errorf("%s: a rule grants patch with every action off", set)
			}
			if !grants(objs, "", "pods", "watch") || !grants(objs, "apps", "deployments", "list") {
				t.Errorf("%s: the read rules are missing", set)
			}
		}
	})
	t.Run("patch per action", func(t *testing.T) {
		workloads := []string{"deployments", "statefulsets", "daemonsets"}
		flux := map[string][]string{
			"kustomize.toolkit.fluxcd.io": {"kustomizations"},
			"helm.toolkit.fluxcd.io":      {"helmreleases"},
			"source.toolkit.fluxcd.io":    {"gitrepositories", "ocirepositories", "helmrepositories"},
		}
		for _, tc := range []struct {
			name               string
			flags              []string
			restart, reconcile bool
		}{
			{"actions set", nil, true, true},
			{"restart only", []string{"--set", "rbac.actions.fluxReconcile=false"}, true, false},
			{"reconcile only", []string{"--set", "rbac.actions.restart=false"}, false, true},
		} {
			objs := mustRender(t, "actions", tc.flags...)
			for _, r := range workloads {
				if grants(objs, "apps", r, "patch") != tc.restart {
					t.Errorf("%s: patch %s = %v", tc.name, r, !tc.restart)
				}
			}
			for g, rs := range flux {
				for _, r := range rs {
					if grants(objs, g, r, "patch") != tc.reconcile {
						t.Errorf("%s: patch %s = %v", tc.name, r, !tc.reconcile)
					}
				}
			}
		}
	})
	t.Run("secretRefs", func(t *testing.T) {
		objs := mustRender(t, "readonly")
		role := one(t, objs, "Role")
		if dig(role, "metadata", "namespace") != "monitoring" {
			t.Errorf("Role namespace = %v", dig(role, "metadata", "namespace"))
		}
		rs := list(role["rules"])
		if len(rs) != 1 || !slices.Equal(strs(dig(rs[0], "resources")), []string{"secrets"}) ||
			!slices.Equal(strs(dig(rs[0], "resourceNames")), []string{"alertmanager"}) ||
			!slices.Equal(strs(dig(rs[0], "verbs")), []string{"get", "list", "watch"}) {
			t.Errorf("Role rules = %v", rs)
		}
		rb := one(t, objs, "RoleBinding")
		if dig(rb, "metadata", "namespace") != "monitoring" || dig(rb, "roleRef", "name") != dig(role, "metadata", "name") ||
			dig(rb, "subjects", 0, "name") != "dash" || dig(rb, "subjects", 0, "namespace") != "dash" {
			t.Errorf("RoleBinding = %v", rb)
		}
	})
	t.Run("ingresses follow the kubernetes plugin's setting", func(t *testing.T) {
		if grants(mustRender(t, "default"), "networking.k8s.io", "ingresses", "list") {
			t.Error("ingresses granted without plugins.kubernetes.apps.ingress")
		}
		if !grants(mustRender(t, "default", "--set", "config.plugins.kubernetes.apps.ingress=true"), "networking.k8s.io", "ingresses", "list") {
			t.Error("ingresses not granted with plugins.kubernetes.apps.ingress")
		}
	})
	t.Run("no API access, no token", func(t *testing.T) {
		objs := mustRender(t, "default", "--set", "rbac.read=false")
		if n := len(ofKind(objs, "ClusterRole")); n != 0 {
			t.Errorf("%d ClusterRoles with every rbac flag off", n)
		}
		if got := podSpec(t, objs)["automountServiceAccountToken"]; got != false {
			t.Errorf("automountServiceAccountToken = %v, want false", got)
		}
		if got := podSpec(t, mustRender(t, "default"))["automountServiceAccountToken"]; got != true {
			t.Errorf("automountServiceAccountToken with rbac.read = %v, want true", got)
		}
		// k8s: references alone need the token too.
		refsOnly := mustRender(t, "default", "--set", "rbac.read=false", "--set-json", `rbac.secretRefs=[{"namespace":"a","names":["x"]}]`)
		if got := podSpec(t, refsOnly)["automountServiceAccountToken"]; got != true {
			t.Errorf("automountServiceAccountToken with secretRefs only = %v, want true", got)
		}
	})
	t.Run("bound to the chart's ServiceAccount", func(t *testing.T) {
		objs := mustRender(t, "default")
		crb := one(t, objs, "ClusterRoleBinding")
		if dig(crb, "roleRef", "name") != dig(one(t, objs, "ClusterRole"), "metadata", "name") ||
			dig(crb, "subjects", 0, "name") != podSpec(t, objs)["serviceAccountName"] {
			t.Errorf("ClusterRoleBinding = %v", crb)
		}
	})
	t.Run("an existing ServiceAccount", func(t *testing.T) {
		objs := mustRender(t, "default", "--set", "serviceAccount.create=false", "--set", "serviceAccount.name=existing")
		if n := len(ofKind(objs, "ServiceAccount")); n != 0 {
			t.Errorf("%d ServiceAccounts with serviceAccount.create=false", n)
		}
		if podSpec(t, objs)["serviceAccountName"] != "existing" || dig(one(t, objs, "ClusterRoleBinding"), "subjects", 0, "name") != "existing" {
			t.Errorf("the pod or the binding does not use the ServiceAccount existing")
		}
	})
	t.Run("cluster-scoped names carry the namespace", func(t *testing.T) {
		names := map[string]bool{}
		for _, ns := range []string{"dash", "other"} {
			objs := mustRender(t, "default", "--namespace", ns, "--set", "guardrails.enabled=true")
			for _, kind := range []string{"ClusterRole", "ClusterRoleBinding", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
				name := fmt.Sprint(dig(one(t, objs, kind), "metadata", "name"))
				if names[kind+"/"+name] {
					t.Errorf("%s %s is the same in two namespaces", kind, name)
				}
				names[kind+"/"+name] = true
			}
		}
	})
}

// The Kubernetes RBAC sections of the generated plugin docs (docs/plugins, from cmd/dash/docs.go)
// state, group by group, what the chart grants: with every RBAC value on, the chart's rules and
// the documented ones the chart grants are the same verbs on the same resources.
func TestRBACMatchesPluginDocs(t *testing.T) {
	objs := mustRender(t, "actions", "--set", "config.plugins.kubernetes.apps.ingress=true",
		"--set-json", `rbac.secretRefs=[{"namespace":"monitoring","names":["alertmanager"]}]`)
	chart := map[string]bool{}
	for _, r := range rules(objs) {
		for _, g := range strs(r["apiGroups"]) {
			for _, res := range strs(r["resources"]) {
				for _, v := range strs(r["verbs"]) {
					chart[g+"/"+res+":"+v] = true
				}
			}
		}
	}
	docs := documentedGrants(t)
	for k := range chart {
		if !docs[k] {
			t.Errorf("the chart grants %s and no plugin page says so", k)
		}
	}
	for k := range docs {
		if !chart[k] {
			t.Errorf("a plugin page says the chart grants %s, and it does not", k)
		}
	}
}

// chartBlock is a rule group the chart grants: its lead line names the chart value, then the
// rules follow as YAML.
var chartBlock = regexp.MustCompile("(?s)\\(chart: `[^`]+`\\)[^\n]*\n\n```yaml\n(.*?)```")

// documentedGrants are the group/resource:verb triples of the rule groups the plugin pages say
// the chart grants.
func documentedGrants(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "docs", "plugins", "*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no plugin pages: %v", err)
	}
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		_, section, ok := strings.Cut(string(b), "\n## Kubernetes RBAC\n")
		if !ok {
			continue
		}
		section, _, _ = strings.Cut(section, "\n## ")
		for _, m := range chartBlock.FindAllStringSubmatch(section, -1) {
			var doc struct {
				Rules []struct {
					APIGroups []string `json:"apiGroups"`
					Resources []string `json:"resources"`
					Verbs     []string `json:"verbs"`
				} `json:"rules"`
			}
			if err := sigsyaml.Unmarshal([]byte(m[1]), &doc); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			for _, r := range doc.Rules {
				for _, g := range r.APIGroups {
					for _, res := range r.Resources {
						for _, v := range r.Verbs {
							out[g+"/"+res+":"+v] = true
						}
					}
				}
			}
		}
	}
	return out
}

func TestGuardrail(t *testing.T) {
	for _, tc := range []struct {
		name               string
		flags              []string
		want               bool
		restart, reconcile string
	}{
		{"auto, actions off", nil, false, "", ""},
		{"auto, restart", []string{"--set", "rbac.actions.restart=true"}, true, "true", "false"},
		{"auto, reconcile", []string{"--set", "rbac.actions.fluxReconcile=true"}, true, "false", "true"},
		{"auto, both", []string{"--set", "rbac.actions.restart=true", "--set", "rbac.actions.fluxReconcile=true"}, true, "true", "true"},
		{"false, both", []string{"--set", "rbac.actions.restart=true", "--set", "rbac.actions.fluxReconcile=true", "--set", "guardrails.enabled=false"}, false, "", ""},
		{"true, actions off", []string{"--set", "guardrails.enabled=true"}, true, "false", "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			objs := mustRender(t, "default", tc.flags...)
			policies, bindings := ofKind(objs, "ValidatingAdmissionPolicy"), ofKind(objs, "ValidatingAdmissionPolicyBinding")
			if !tc.want {
				if len(policies)+len(bindings) != 0 {
					t.Fatalf("%d policies and %d bindings, want none", len(policies), len(bindings))
				}
				return
			}
			if len(policies) != 1 || len(bindings) != 1 {
				t.Fatalf("%d policies and %d bindings, want one each", len(policies), len(bindings))
			}
			vap, binding := policies[0], bindings[0]
			if dig(binding, "spec", "policyName") != dig(vap, "metadata", "name") ||
				!slices.Equal(strs(dig(binding, "spec", "validationActions")), []string{"Deny"}) {
				t.Errorf("binding = %v", binding)
			}
			if got := dig(vap, "spec", "matchConditions", 0, "expression"); !strings.Contains(fmt.Sprint(got), "'system:serviceaccount:dash:dash'") {
				t.Errorf("matchCondition = %v, want the chart's ServiceAccount", got)
			}
			custom := one(t, mustRender(t, "default", append(slices.Clone(tc.flags), "--set", "serviceAccount.name=custom")...), "ValidatingAdmissionPolicy")
			if got := dig(custom, "spec", "matchConditions", 0, "expression"); !strings.Contains(fmt.Sprint(got), "'system:serviceaccount:dash:custom'") {
				t.Errorf("matchCondition = %v with serviceAccount.name=custom", got)
			}
			if dig(vap, "spec", "failurePolicy") != "Fail" {
				t.Errorf("failurePolicy = %v", dig(vap, "spec", "failurePolicy"))
			}
			var kinds []string
			for _, r := range list(dig(vap, "spec", "matchConstraints", "resourceRules")) {
				kinds = append(kinds, strs(dig(r, "resources"))...)
				if ops := strs(dig(r, "operations")); !slices.Contains(ops, "CREATE") || !slices.Contains(ops, "UPDATE") || !slices.Contains(ops, "DELETE") {
					t.Errorf("operations = %v, want CREATE, UPDATE and DELETE", ops)
				}
			}
			for _, k := range []string{"deployments", "statefulsets", "daemonsets", "kustomizations", "helmreleases", "gitrepositories", "ocirepositories", "helmrepositories"} {
				if !slices.Contains(kinds, k) || !slices.Contains(kinds, k+"/*") {
					t.Errorf("the policy does not match %s and its subresources", k)
				}
			}
			validations := list(dig(vap, "spec", "validations"))
			if !slices.ContainsFunc(validations, func(v any) bool { return dig(v, "expression") == "variables.subresource == ''" }) {
				t.Error("no validation denies subresource requests")
			}
			if !slices.ContainsFunc(list(dig(vap, "spec", "variables")), func(v any) bool {
				return dig(v, "name") == "subresource" && dig(v, "expression") == "request.?subResource.orValue('')"
			}) {
				t.Error("the subresource variable does not read request.subResource")
			}
			if !slices.ContainsFunc(validations, func(v any) bool { return dig(v, "expression") == "request.operation == 'UPDATE'" }) {
				t.Error("no validation denies create and delete")
			}
			for _, v := range list(dig(vap, "spec", "variables")) {
				if dig(v, "name") == "annotationKey" &&
					!strings.HasPrefix(fmt.Sprint(dig(v, "expression")), "request.resource.resource == 'daemonsets' ? 'deprecated.daemonset.template.generation'") {
					t.Errorf("the DaemonSet generation annotation is not limited to daemonsets: %v", dig(v, "expression"))
				}
			}
			// The action switches are literals in the validations that name them.
			for flag, want := range map[string]string{"rbac.actions.restart": tc.restart, "rbac.actions.fluxReconcile": tc.reconcile} {
				found := false
				for _, v := range list(dig(vap, "spec", "validations")) {
					if strings.Contains(fmt.Sprint(dig(v, "message")), flag) {
						found = true
						if expr := fmt.Sprint(dig(v, "expression")); !strings.HasSuffix(expr, "|| "+want) {
							t.Errorf("%s validation = %q, want it to end in || %s", flag, expr, want)
						}
					}
				}
				if !found {
					t.Errorf("no validation names %s", flag)
				}
			}
		})
	}
}

func TestSidecar(t *testing.T) {
	objs := mustRender(t, "sidecar")
	proxy := container(t, objs, "oauth2-proxy")
	args := strs(proxy["args"])
	for _, a := range []string{"--upstream=http://127.0.0.1:8080/", "--http-address=0.0.0.0:4180", "--github-user=alice", "--pass-user-headers=true", "--reverse-proxy=true"} {
		if !slices.Contains(args, a) {
			t.Errorf("proxy args %v lack %s", args, a)
		}
	}
	if got := containerPort(proxy, "http"); got != float64(4180) {
		t.Errorf("proxy http port = %v", got)
	}
	svc := one(t, objs, "Service")
	if dig(svc, "spec", "ports", 0, "targetPort") != "http" {
		t.Errorf("Service targetPort = %v, want the http port (the proxy's)", dig(svc, "spec", "ports", 0, "targetPort"))
	}
	cfg := dashConfig(t, objs)
	if dig(cfg, "auth", "mode") != "proxy" || !slices.Equal(strs(dig(cfg, "auth", "proxy", "trustedProxies")), []string{"127.0.0.1/32", "::1/128"}) {
		t.Errorf("auth = %v, want proxy mode trusting the loopback", cfg["auth"])
	}
	if !slices.Equal(strs(dig(cfg, "auth", "proxy", "adminUsers")), []string{"alice"}) {
		t.Errorf("the values' auth.proxy.adminUsers are lost: %v", cfg["auth"])
	}
	// Defaults only: config.auth.mode wins.
	if got := dig(dashConfig(t, mustRender(t, "sidecar", "--set", "config.auth.mode=local")), "auth", "mode"); got != "local" {
		t.Errorf("auth.mode = %v, want the values' local", got)
	}
	if got := dashConfig(t, mustRender(t, "default"))["auth"]; got != nil {
		t.Errorf("auth = %v without the sidecar, want unset", got)
	}
	if hasContainer(t, mustRender(t, "default"), "oauth2-proxy") {
		t.Error("the sidecar runs without auth.oauth2ProxySidecar.enabled")
	}

	np := one(t, objs, "NetworkPolicy")
	var ports []string
	for _, r := range list(dig(np, "spec", "ingress")) {
		ports = append(ports, fmt.Sprint(dig(r, "ports", 0, "port")))
	}
	if !slices.Equal(ports, []string{"http", "health"}) {
		t.Errorf("NetworkPolicy ports = %v", ports)
	}
	route := one(t, objs, "HTTPRoute")
	if dig(route, "spec", "rules", 0, "backendRefs", 0, "name") != dig(svc, "metadata", "name") ||
		dig(route, "spec", "rules", 0, "backendRefs", 0, "port") != dig(svc, "spec", "ports", 0, "port") {
		t.Errorf("HTTPRoute backend = %v", dig(route, "spec", "rules", 0, "backendRefs"))
	}
}

func TestIngress(t *testing.T) {
	objs := mustRender(t, "default", "--set", "ingress.enabled=true", "--set-json",
		`ingress.hosts=[{"host":"dash.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]`)
	ing := one(t, objs, "Ingress")
	if dig(ing, "spec", "rules", 0, "host") != "dash.example.com" ||
		dig(ing, "spec", "rules", 0, "http", "paths", 0, "backend", "service", "name") != dig(one(t, objs, "Service"), "metadata", "name") {
		t.Errorf("Ingress = %v", ing["spec"])
	}
	if len(ofKind(mustRender(t, "default"), "Ingress")) != 0 {
		t.Error("an Ingress without ingress.enabled")
	}
}

func TestDirect(t *testing.T) {
	objs := mustRender(t, "direct")
	dash := container(t, objs, "dash")
	if got := env(dash)["DASH_SERVER_DIRECT_URL"]; got != "https://dash-direct.example.com" {
		t.Errorf("DASH_SERVER_DIRECT_URL = %v", got)
	}
	if got := containerPort(dash, "direct"); got != float64(8080) {
		t.Errorf("dash's direct port = %v, want 8080", got)
	}
	if got := containerPort(container(t, objs, "oauth2-proxy"), "http"); got != float64(4180) {
		t.Errorf("proxy http port = %v, want 4180", got)
	}
	cfg := dashConfig(t, objs)
	if dig(cfg, "auth", "mode") != "proxy" || !slices.Equal(strs(dig(cfg, "auth", "proxy", "trustedProxies")), []string{"127.0.0.1/32", "::1/128"}) {
		t.Errorf("auth = %v, want proxy mode trusting the loopback only", cfg["auth"])
	}

	svc := one(t, objs, "Service")
	ports := map[string]obj{}
	for _, p := range list(dig(svc, "spec", "ports")) {
		ports[fmt.Sprint(dig(p, "name"))] = p.(obj)
	}
	if len(ports) != 2 || ports["http"]["targetPort"] != "http" || ports["direct"]["targetPort"] != "direct" || ports["direct"]["port"] != float64(8082) {
		t.Errorf("Service ports = %v, want http to the proxy and direct (8082) to dash", ports)
	}

	ing := one(t, objs, "Ingress")
	var hosts []string
	for _, r := range list(dig(ing, "spec", "rules")) {
		hosts = append(hosts, fmt.Sprint(dig(r, "host"), "/", dig(r, "http", "paths", 0, "path"), "->",
			dig(r, "http", "paths", 0, "backend", "service", "name"), ":", dig(r, "http", "paths", 0, "backend", "service", "port", "name")))
	}
	if !slices.Equal(hosts, []string{"dash.example.com//->dash:http", "dash-direct.example.com//->dash:direct"}) {
		t.Errorf("Ingress rules = %v", hosts)
	}

	routes := ofKind(objs, "HTTPRoute")
	if len(routes) != 2 {
		t.Fatalf("%d HTTPRoutes, want the public one and the direct one", len(routes))
	}
	for _, r := range routes {
		name := dig(r, "metadata", "name")
		want := []any{"dash.example.com", float64(8080)}
		if name == "dash-direct" {
			want = []any{"dash-direct.example.com", float64(8082)}
		} else if name != "dash" {
			t.Errorf("HTTPRoute %v", name)
		}
		if !slices.Equal(strs(dig(r, "spec", "hostnames")), []string{fmt.Sprint(want[0])}) ||
			dig(r, "spec", "rules", 0, "backendRefs", 0, "name") != "dash" ||
			dig(r, "spec", "rules", 0, "backendRefs", 0, "port") != want[1] ||
			dig(r, "spec", "parentRefs", 0, "name") != "gateway" {
			t.Errorf("HTTPRoute %v = %v", name, r["spec"])
		}
	}

	np := one(t, objs, "NetworkPolicy")
	from := map[string]any{}
	var npPorts []string
	for _, r := range list(dig(np, "spec", "ingress")) {
		p := fmt.Sprint(dig(r, "ports", 0, "port"))
		npPorts = append(npPorts, p)
		from[p] = dig(r, "from")
	}
	if !slices.Equal(npPorts, []string{"http", "direct", "health"}) {
		t.Errorf("NetworkPolicy ports = %v", npPorts)
	}
	if from["direct"] == nil || fmt.Sprint(from["direct"]) != fmt.Sprint(from["http"]) {
		t.Errorf("NetworkPolicy direct from = %v, want http's %v", from["direct"], from["http"])
	}

	// Off, or with routes.direct.enabled false, nothing of it renders.
	for _, off := range [][]string{nil, {"--set", "routes.direct.enabled=false"}} {
		objs := mustRender(t, "sidecar", append([]string{"--set", "ingress.enabled=true", "--set-json",
			`ingress.hosts=[{"host":"dash.example.com","paths":[{"path":"/","pathType":"Prefix"}]}]`,
			"--set", "routes.direct.host=dash-direct.example.com"}, off...)...)
		dash := container(t, objs, "dash")
		if _, ok := env(dash)["DASH_SERVER_DIRECT_URL"]; ok || containerPort(dash, "direct") != nil {
			t.Errorf("routes.direct off: env %v, ports %v", env(dash), dash["ports"])
		}
		if n := len(list(dig(one(t, objs, "Service"), "spec", "ports"))); n != 1 {
			t.Errorf("routes.direct off: %d Service ports", n)
		}
		if n := len(list(dig(one(t, objs, "Ingress"), "spec", "rules"))); n != 1 {
			t.Errorf("routes.direct off: %d Ingress rules", n)
		}
		if n := len(ofKind(objs, "HTTPRoute")); n != 1 {
			t.Errorf("routes.direct off: %d HTTPRoutes", n)
		}
	}
}
