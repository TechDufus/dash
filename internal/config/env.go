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
// (DASH_PLUGINS_PROXMOX_URL); lists and objects are JSON. Every name, generated or legacy, also
// takes a <NAME>_FILE form holding the path of a file with the value, resolved here because the
// image has no shell entrypoint. The legacy names are aliases that warn once each.

// processEnv are variables of the process or of its deployment that are not tree fields. They
// never warn as unknown.
var processEnv = map[string]bool{
	"DASH_DEV": true, "DASH_DEMO": true, "DASH_CONFIG": true,
	"DASH_API": true, // the Vite dev proxy's backend address (web/vite.config.ts)
	// The secret-key variables are read by the secrets package (P1.5).
	"DASH_SECRET_KEY": true, "DASH_SECRET_KEY_FILE": true, "DASH_SECRET_KEY_PREVIOUS": true,
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

// alias maps legacy variables onto the tree (design B.5). A few variables are read together.
type alias struct {
	names []string
	// targets are the paths build may return; their generated names are the replacement.
	targets []string
	// keep means the variable stays supported, without a deprecation warning (KUBECONFIG).
	keep bool
	// build receives the trimmed, non-empty values among names and returns JSON-able values by path.
	build func(v map[string]string) (map[string]any, error)
}

// simpleAlias is a variable that holds the string at one path.
func simpleAlias(name, path string) alias {
	return alias{names: []string{name}, targets: []string{path}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{path: v[name]}, nil
	}}
}

var aliases = []alias{
	simpleAlias("DASH_PUBLIC_URL", "server.publicURL"),
	simpleAlias("DASH_LISTEN", "server.listen"),
	simpleAlias("DASH_HEALTH_LISTEN", "server.healthListen"),
	{names: []string{"DASH_ALLOWED_HOSTS"}, targets: []string{"server.allowedHosts"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"server.allowedHosts": splitHosts(v["DASH_ALLOWED_HOSTS"])}, nil
	}},
	simpleAlias("DASH_VM_URL", "plugins.prometheus.url"),
	simpleAlias("DASH_ALERTMANAGER_URL", "plugins.alertmanager.url"),
	simpleAlias("DASH_VLOGS_URL", "plugins.victorialogs.url"),
	simpleAlias("DASH_GATUS_URL", "plugins.gatus.url"),
	simpleAlias("DASH_PROXMOX_URL", "plugins.proxmox.url"),
	simpleAlias("DASH_PROXMOX_NODE", "plugins.proxmox.node"),
	{names: []string{"DASH_PROXMOX_TOKEN"}, targets: []string{"plugins.proxmox.tokenID", "plugins.proxmox.tokenSecret"}, build: func(v map[string]string) (map[string]any, error) {
		id, secret, ok := strings.Cut(v["DASH_PROXMOX_TOKEN"], "=")
		if !ok || id == "" || secret == "" {
			// The value is a credential: describe the shape, never echo it.
			return nil, errors.New("DASH_PROXMOX_TOKEN must look like user@realm!name=secret")
		}
		return map[string]any{"plugins.proxmox.tokenID": id, "plugins.proxmox.tokenSecret": secret}, nil
	}},
	{names: []string{"DASH_HOST_NODENAME", "DASH_HOST_MODEL"}, targets: []string{"plugins.hardware.hosts"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"plugins.hardware.hosts": []HardwareHost{{Nodename: v["DASH_HOST_NODENAME"], Model: v["DASH_HOST_MODEL"]}}}, nil
	}},
	simpleAlias("DASH_HEADLAMP_URL", "plugins.headlamp.url"),
	simpleAlias("DASH_HEADLAMP_CLUSTER", "plugins.headlamp.cluster"),
	simpleAlias("DASH_GRAFANA_URL", "plugins.grafana.url"),
	simpleAlias("DASH_LAUNCHER_FILE", "plugins.launcher.file"),
	{names: []string{"KUBECONFIG"}, targets: []string{"plugins.kubernetes.kubeconfig"}, keep: true, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"plugins.kubernetes.kubeconfig": v["KUBECONFIG"]}, nil
	}},
	simpleAlias("DASH_LABEL_PROTECTED", "plugins.kubernetes.conventions.protectedLabel"),
	simpleAlias("DASH_LABEL_TENANT", "plugins.kubernetes.conventions.tenantLabel"),
	simpleAlias("DASH_APPS_FLUX_PREFIX", "plugins.kubernetes.conventions.appsFluxPrefix"),
	simpleAlias("DASH_ANNOTATION_SUSPEND_REASON", "plugins.flux.suspendReasonAnnotation"),
	simpleAlias("DASH_ZFS_SCRUB_METRIC_PREFIX", "plugins.hardware.zfsScrubMetricPrefix"),
	{names: []string{"DASH_NAS_BACKUP_NAMESPACE", "DASH_NAS_BACKUP_CRONJOB"}, targets: []string{"plugins.kubernetes.backupJobs"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"plugins.kubernetes.backupJobs": []BackupJob{{Namespace: v["DASH_NAS_BACKUP_NAMESPACE"], CronJob: v["DASH_NAS_BACKUP_CRONJOB"]}}}, nil
	}},
}

// replacement names the generated variable(s) that take over from an alias; file selects the
// _FILE form.
func (a alias) replacement(file bool) string {
	var names []string
	for _, p := range a.targets {
		n := schema.byPath[p].env
		if file {
			n += "_FILE"
		} else if k := schema.byPath[p].typ.Kind(); k != reflect.String {
			n += " (JSON)"
		}
		names = append(names, n)
	}
	return strings.Join(names, " and ")
}

// knownEnvNames are the variables with a meaning: generated and legacy.
func knownEnvNames() map[string]bool {
	known := map[string]bool{}
	for name := range schema.byEnv {
		known[name] = true
	}
	for _, a := range aliases {
		for _, n := range a.names {
			known[n] = true
		}
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
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return envValue{}, fmt.Errorf("%s_FILE: %w", name, err)
		}
		return envValue{strings.TrimSpace(string(data)), name + "_FILE"}, nil
	}
	return envValue{direct, name}, nil
}

// envLayer reads the environment layer: legacy aliases first, so a generated name overrides the
// legacy name of the same field. It warns once per legacy variable used, errors on an unknown
// DASH_PLUGINS_* variable (typo protection) and warns about other unknown DASH_* variables.
func envLayer(env map[string]string, log *slog.Logger) ([]envEntry, error) {
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
		if !strings.HasPrefix(k, "DASH_") || processEnv[k] || serviceLinkRE.MatchString(k) || known[k] {
			continue
		}
		if base, ok := strings.CutSuffix(k, "_FILE"); ok && known[base] {
			continue
		}
		if strings.HasPrefix(k, "DASH_PLUGINS_") {
			errs = append(errs, fmt.Errorf("%s is not a plugin setting (a typo?); run `dash config env` for the names", k))
			continue
		}
		log.Warn("ignoring unknown environment variable", "name", k)
	}

	var out []envEntry
	for _, a := range aliases {
		got, refs := map[string]string{}, []string{}
		for _, n := range a.names {
			if v, ok := vals[n]; ok {
				got[n] = v.val
				refs = append(refs, v.ref)
				if !a.keep {
					log.Warn("deprecated environment variable", "name", v.ref, "use", a.replacement(v.ref != n))
				}
			}
		}
		if len(refs) == 0 {
			continue
		}
		built, err := a.build(got)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ref := strings.Join(refs, ", ")
		for _, p := range a.targets {
			if v, ok := built[p]; ok {
				data, err := json.Marshal(v)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", ref, err))
					continue
				}
				out = append(out, envEntry{path: p, data: data, ref: ref})
			}
		}
	}
	for _, l := range schema.leaves {
		v, ok := vals[l.env]
		if !ok {
			continue
		}
		data := []byte(v.val)
		if l.typ.Kind() == reflect.String {
			data, _ = json.Marshal(v.val)
		}
		out = append(out, envEntry{path: l.path, data: data, ref: v.ref})
	}
	return out, errors.Join(errs...)
}
