package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// envEntry is one value the environment layer sets: JSON for the leaf at path.
type envEntry struct {
	path string
	data []byte
	ref  string // the variable it came from
}

// alias maps legacy variables onto the tree (design B.5). A few variables are read together.
type alias struct {
	names []string
	// build receives the trimmed, non-empty values among names and returns JSON-able values by path.
	build func(v map[string]string) (map[string]any, error)
}

// simpleAlias is a variable that holds the string at one path.
func simpleAlias(name, path string) alias {
	return alias{names: []string{name}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{path: v[name]}, nil
	}}
}

var aliases = []alias{
	simpleAlias("DASH_PUBLIC_URL", "server.publicURL"),
	simpleAlias("DASH_LISTEN", "server.listen"),
	simpleAlias("DASH_HEALTH_LISTEN", "server.healthListen"),
	{names: []string{"DASH_ALLOWED_HOSTS"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"server.allowedHosts": splitHosts(v["DASH_ALLOWED_HOSTS"])}, nil
	}},
	simpleAlias("DASH_VM_URL", "plugins.prometheus.url"),
	simpleAlias("DASH_ALERTMANAGER_URL", "plugins.alertmanager.url"),
	simpleAlias("DASH_VLOGS_URL", "plugins.victorialogs.url"),
	simpleAlias("DASH_GATUS_URL", "plugins.gatus.url"),
	simpleAlias("DASH_PROXMOX_URL", "plugins.proxmox.url"),
	simpleAlias("DASH_PROXMOX_NODE", "plugins.proxmox.node"),
	{names: []string{"DASH_PROXMOX_TOKEN"}, build: func(v map[string]string) (map[string]any, error) {
		id, secret, ok := strings.Cut(v["DASH_PROXMOX_TOKEN"], "=")
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf("DASH_PROXMOX_TOKEN must look like user@realm!name=secret")
		}
		return map[string]any{"plugins.proxmox.tokenID": id, "plugins.proxmox.tokenSecret": secret}, nil
	}},
	{names: []string{"DASH_HOST_NODENAME", "DASH_HOST_MODEL"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"plugins.hardware.hosts": []HardwareHost{{Nodename: v["DASH_HOST_NODENAME"], Model: v["DASH_HOST_MODEL"]}}}, nil
	}},
	simpleAlias("DASH_HEADLAMP_URL", "plugins.headlamp.url"),
	simpleAlias("DASH_HEADLAMP_CLUSTER", "plugins.headlamp.cluster"),
	simpleAlias("DASH_GRAFANA_URL", "plugins.grafana.url"),
	simpleAlias("DASH_LAUNCHER_FILE", "plugins.launcher.file"),
	simpleAlias("KUBECONFIG", "plugins.kubernetes.kubeconfig"),
	simpleAlias("DASH_LABEL_PROTECTED", "plugins.kubernetes.conventions.protectedLabel"),
	simpleAlias("DASH_LABEL_TENANT", "plugins.kubernetes.conventions.tenantLabel"),
	simpleAlias("DASH_APPS_FLUX_PREFIX", "plugins.kubernetes.conventions.appsFluxPrefix"),
	simpleAlias("DASH_ANNOTATION_SUSPEND_REASON", "plugins.flux.suspendReasonAnnotation"),
	simpleAlias("DASH_ZFS_SCRUB_METRIC_PREFIX", "plugins.hardware.zfsScrubMetricPrefix"),
	{names: []string{"DASH_NAS_BACKUP_NAMESPACE", "DASH_NAS_BACKUP_CRONJOB"}, build: func(v map[string]string) (map[string]any, error) {
		return map[string]any{"plugins.kubernetes.backupJobs": []BackupJob{{Namespace: v["DASH_NAS_BACKUP_NAMESPACE"], CronJob: v["DASH_NAS_BACKUP_CRONJOB"]}}}, nil
	}},
}

// envLayer reads the environment layer. An empty variable counts as unset.
func envLayer(env map[string]string) ([]envEntry, error) {
	var out []envEntry
	for _, a := range aliases {
		vals, set := map[string]string{}, []string{}
		for _, n := range a.names {
			if v := strings.TrimSpace(env[n]); v != "" {
				vals[n], set = v, append(set, n)
			}
		}
		if len(set) == 0 {
			continue
		}
		built, err := a.build(vals)
		if err != nil {
			return nil, err
		}
		for _, l := range schema.leaves { // schema order keeps the result deterministic
			v, ok := built[l.path]
			if !ok {
				continue
			}
			data, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", strings.Join(set, ", "), err)
			}
			out = append(out, envEntry{path: l.path, data: data, ref: strings.Join(set, ", ")})
		}
	}
	return out, nil
}
