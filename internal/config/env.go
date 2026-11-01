package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// The environment layer (design B.3, B.5). Every schema leaf has a generated name
// (DASH_PLUGINS_PROXMOX_URL); lists and objects are JSON. Every name also takes a <NAME>_FILE
// form holding the path of a file with the value, resolved here because the image has no shell
// entrypoint. The only other variable read is KUBECONFIG (kubeconfigEnv).

// processEnv are variables of the process or of its deployment that are not tree fields. They
// never warn as unknown.
var processEnv = map[string]bool{
	"DASH_DEV": true, "DASH_DEMO": true, "DASH_CONFIG": true,
	"DASH_API": true, // the Vite dev proxy's backend address (web/vite.config.ts)
	// The secret-key variables are read by SecretKeys (keys.go), not by the tree.
	"DASH_SECRET_KEY": true, "DASH_SECRET_KEY_FILE": true,
	"DASH_SECRET_KEY_PREVIOUS": true, "DASH_SECRET_KEY_PREVIOUS_FILE": true,
}

// serviceLinkRE matches the variables Kubernetes injects for a Service named "dash" in the same
// namespace (DASH_SERVICE_HOST, DASH_PORT_4180_TCP_ADDR, ...).
var serviceLinkRE = regexp.MustCompile(`^DASH_(SERVICE_HOST|SERVICE_PORT(_[A-Z0-9_]+)?|PORT|PORT_[0-9]+_(TCP|UDP|SCTP)(_PROTO|_PORT|_ADDR)?)$`)

// envEntry is one value the environment layer sets: JSON for the leaf at path.
type envEntry struct {
	path string
	data []byte
	ref  string // the variable it came from
}

// removedEnv are the environment variables dash 0.4.0 still read as deprecated aliases and 0.5.0
// does not (ADR 0001 decision 5, design J.5). A deployment that still sets one fails at startup
// with the replacement, because ignoring it would silently turn a source off or drop a required
// setting. The table is deleted one release later; a stale name then only gets the
// unknown-variable warning. KUBECONFIG is not here: see kubeconfigEnv.
var removedEnv = []struct{ name, use string }{
	{"DASH_PUBLIC_URL", "DASH_SERVER_PUBLIC_URL"},
	{"DASH_LISTEN", "DASH_SERVER_LISTEN"},
	{"DASH_HEALTH_LISTEN", "DASH_SERVER_HEALTH_LISTEN"},
	{"DASH_ALLOWED_HOSTS", `DASH_SERVER_ALLOWED_HOSTS (a JSON list, e.g. '["dash.example.com"]')`},
	{"DASH_VM_URL", "DASH_PLUGINS_PROMETHEUS_URL"},
	{"DASH_ALERTMANAGER_URL", "DASH_PLUGINS_ALERTMANAGER_URL"},
	{"DASH_VLOGS_URL", "DASH_PLUGINS_VICTORIALOGS_URL"},
	{"DASH_GATUS_URL", "DASH_PLUGINS_GATUS_URL"},
	{"DASH_PROXMOX_URL", "DASH_PLUGINS_PROXMOX_URL"},
	{"DASH_PROXMOX_NODE", "DASH_PLUGINS_PROXMOX_NODE"},
	{"DASH_PROXMOX_TOKEN", "DASH_PLUGINS_PROXMOX_TOKEN_ID and DASH_PLUGINS_PROXMOX_TOKEN_SECRET (split the old user@realm!name=secret value)"},
	{"DASH_HOST_NODENAME", `DASH_PLUGINS_HARDWARE_HOSTS (a JSON list, e.g. '[{"nodename":"box","model":"Dell R730"}]')`},
	{"DASH_HOST_MODEL", `DASH_PLUGINS_HARDWARE_HOSTS (a JSON list, e.g. '[{"nodename":"box","model":"Dell R730"}]')`},
	{"DASH_HEADLAMP_URL", "DASH_PLUGINS_HEADLAMP_URL"},
	{"DASH_HEADLAMP_CLUSTER", "DASH_PLUGINS_HEADLAMP_CLUSTER"},
	{"DASH_GRAFANA_URL", "DASH_PLUGINS_GRAFANA_URL"},
	{"DASH_LAUNCHER_FILE", "DASH_PLUGINS_LAUNCHER_FILE"},
	{"DASH_LABEL_PROTECTED", "DASH_PLUGINS_KUBERNETES_CONVENTIONS_PROTECTED_LABEL"},
	{"DASH_LABEL_TENANT", "DASH_PLUGINS_KUBERNETES_CONVENTIONS_TENANT_LABEL"},
	{"DASH_APPS_FLUX_PREFIX", "DASH_PLUGINS_KUBERNETES_CONVENTIONS_APPS_FLUX_PREFIX"},
	{"DASH_ANNOTATION_SUSPEND_REASON", "DASH_PLUGINS_FLUX_SUSPEND_REASON_ANNOTATION"},
	{"DASH_ZFS_SCRUB_METRIC_PREFIX", "DASH_PLUGINS_HARDWARE_ZFS_SCRUB_METRIC_PREFIX"},
	{"DASH_NAS_BACKUP_NAMESPACE", `DASH_PLUGINS_KUBERNETES_BACKUP_JOBS (a JSON list, e.g. '[{"namespace":"backup","cronjob":"nightly"}]')`},
	{"DASH_NAS_BACKUP_CRONJOB", `DASH_PLUGINS_KUBERNETES_BACKUP_JOBS (a JSON list, e.g. '[{"namespace":"backup","cronjob":"nightly"}]')`},
}

// kubeconfigEnv is the one variable of another tool that dash honours: the standard kubectl
// variable fills plugins.kubernetes.kubeconfig, and the generated name wins over it. It has no
// _FILE form and no deprecation warning.
const kubeconfigEnv = "KUBECONFIG"

var envNameRE = regexp.MustCompile(`DASH_[A-Z0-9_]+`)

// removedEnvError lists every removed variable (or its _FILE form) the environment sets, with
// its replacement, or returns nil. Values are never echoed.
func removedEnvError(env map[string]string) error {
	var lines []string
	for _, r := range removedEnv {
		if strings.TrimSpace(env[r.name]) != "" {
			lines = append(lines, fmt.Sprintf("  %s -> %s", r.name, r.use))
		}
		if strings.TrimSpace(env[r.name+"_FILE"]) != "" {
			// The replacement's variable names, each in their _FILE form; the text in
			// parentheses is advice and stays as written.
			use, advice, hasAdvice := strings.Cut(r.use, " (")
			use = envNameRE.ReplaceAllStringFunc(use, func(n string) string { return n + "_FILE" })
			if hasAdvice {
				use += " (" + advice
			}
			lines = append(lines, fmt.Sprintf("  %s_FILE -> %s", r.name, use))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("config: these environment variables were removed in v0.5.0 (they were deprecated aliases in v0.4.0):\n%s\nsee \"Removed variable names\" in the README", strings.Join(lines, "\n"))
}

// removedName reports whether name is in removedEnv. An empty one counts as unset and is not
// reported as unknown either.
func removedName(name string) bool {
	return slices.ContainsFunc(removedEnv, func(r struct{ name, use string }) bool { return r.name == name })
}

// knownEnvNames are the variables with a meaning: the generated names.
func knownEnvNames() map[string]bool {
	known := map[string]bool{}
	for name := range schema.byEnv {
		known[name] = true
	}
	return known
}

type envValue struct{ val, ref string }

// readEnvVar returns the value of name or of name_FILE. Empty counts as unset. Both set is an
// error naming both; so is an unreadable file (the message carries the path, never contents).
func readEnvVar(env map[string]string, name string) (envValue, error) {
	direct, file := strings.TrimSpace(env[name]), strings.TrimSpace(env[name+"_FILE"])
	switch {
	case direct != "" && file != "":
		return envValue{}, fmt.Errorf("%s and %s_FILE are both set; set only one", name, name)
	case file != "" && schema.byEnv[name] != nil && schema.byEnv[name].secret:
		// Kept as a reference, resolved with the rest of the secrets: the file stays watchable
		// for hot reload and an unreadable one fails naming the field.
		return envValue{"file:" + file, name + "_FILE"}, nil
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return envValue{}, fmt.Errorf("%s_FILE: %w", name, err)
		}
		return envValue{strings.TrimSpace(string(data)), name + "_FILE"}, nil
	}
	return envValue{direct, name}, nil
}

// envLayer reads the environment layer. It fails when a removed variable is set, errors on an
// unknown DASH_PLUGINS_* variable (typo protection) and warns about other unknown DASH_*
// variables.
func envLayer(env map[string]string, log *slog.Logger) ([]envEntry, error) {
	if err := removedEnvError(env); err != nil {
		return nil, err
	}
	known := knownEnvNames()
	var errs []error

	vals := map[string]envValue{}
	for _, name := range slices.Sorted(maps.Keys(known)) {
		v, err := readEnvVar(env, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if v.val != "" {
			vals[name] = v
		}
	}

	for _, k := range slices.Sorted(maps.Keys(env)) {
		base, isFile := strings.CutSuffix(k, "_FILE")
		if !strings.HasPrefix(k, "DASH_") || processEnv[k] || serviceLinkRE.MatchString(k) || known[k] || (isFile && known[base]) || removedName(base) {
			continue
		}
		if strings.HasPrefix(k, "DASH_PLUGINS_") {
			errs = append(errs, fmt.Errorf("%s is not a plugin setting (a typo?); run `dash config env` for the names", k))
			continue
		}
		log.Warn("ignoring unknown environment variable", "name", k)
	}

	var out []envEntry
	if v := strings.TrimSpace(env[kubeconfigEnv]); v != "" {
		// Before the generated names, so DASH_PLUGINS_KUBERNETES_KUBECONFIG wins.
		data, _ := json.Marshal(v)
		out = append(out, envEntry{path: "plugins.kubernetes.kubeconfig", data: data, ref: kubeconfigEnv})
	}
	for _, l := range schema.leaves {
		v, ok := vals[l.env]
		if !ok {
			continue
		}
		data := []byte(v.val)
		if l.typ.Kind() == reflect.String || l.secret || l.typ == durationType { // 30m, not "30m", in the environment
			data, _ = json.Marshal(v.val)
		}
		out = append(out, envEntry{path: l.path, data: data, ref: v.ref})
	}
	return out, errors.Join(errs...)
}
