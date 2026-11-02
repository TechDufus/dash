package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/hardware"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
)

// reachLeaf is one setting of a plugin's section: its schema path, a non-default value for the
// environment, and another valid one for the file layer (empty for the two settings the
// supervisor itself reads, type and enabled, which no reload restarts an instance for).
type reachLeaf struct{ path, env, next string }

func proxmoxLeaves() []reachLeaf {
	return []reachLeaf{
		{"plugins.proxmox.type", "proxmox", ""},
		{"plugins.proxmox.enabled", "true", ""},
		{"plugins.proxmox.url", "https://pve-a.example.com:8006", "https://pve-b.example.com:8006"},
		{"plugins.proxmox.node", "pve", "pve-2"},
		{"plugins.proxmox.tokenID", "u@pve!t", "u@pve!t2"},
		{"plugins.proxmox.tokenSecret", "token-secret-0123456789", "token-secret-9876543210"},
		{"plugins.proxmox.tls.caFile", "/etc/dash/pve-ca.pem", "/etc/dash/pve-ca-2.pem"},
		{"plugins.proxmox.tls.insecureSkipVerify", "true", "false"},
		{"plugins.proxmox.criticalVMs.ids", "[101,2020]", "[101,2021]"},
		{"plugins.proxmox.criticalVMs.tags", `["prod","DB"]`, `["prod"]`},
		{"plugins.proxmox.storage.source", "prometheus", "api"},
		{"plugins.proxmox.series.vmSelector", `vmid="{vmid}"`, `id="qemu/{vmid}"`},
	}
}

func hardwareLeaves() []reachLeaf {
	return []reachLeaf{
		{"plugins.hardware.type", "hardware", ""},
		{"plugins.hardware.enabled", "true", ""},
		{"plugins.hardware.hosts",
			`[{"name":"Rack 1","nodename":"box-1","model":"Model X","bays":8,"fanMaxRPM":9000,"sensorAliases":{"inlet":"ambient","exhaust":"outlet"}}]`,
			`[{"name":"Rack 2","nodename":"box-2","model":"Model Y","bays":12,"fanMaxRPM":12000,"sensorAliases":{"inlet":"intake","exhaust":"exit"}}]`},
		{"plugins.hardware.machines",
			`[{"name":"nas-1","nodeExporterNodename":"nas-1"},{"name":"edge-1","gatusEndpoint":"edge-1 SSH"}]`,
			`[{"name":"nas-2","nodeExporterNodename":"nas-2"}]`},
		{"plugins.hardware.selectors.nodeExporterJob", "nodes", "nodes-2"},
		{"plugins.hardware.selectors.hwmonChip", "platform_k10temp_.*", "platform_coretemp_.*"},
		{"plugins.hardware.tempThresholds.warn", "80", "81"},
		{"plugins.hardware.tempThresholds.crit", "95", "96"},
		{"plugins.hardware.zfsScrubMetricPrefix", "example_zfs_scrub", "example_zfs_scrub_2"},
	}
}

// kubernetesLeaves is every setting of plugins.kubernetes that exists in P3.8; the rows of
// plugins.kubernetes.apps.* join with their keys in P3.9. The values never reach a cluster: the
// test starts a fake that records the plugin's configuration.
func kubernetesLeaves() []reachLeaf {
	return []reachLeaf{
		{"plugins.kubernetes.type", "kubernetes", ""},
		{"plugins.kubernetes.enabled", "true", ""},
		{"plugins.kubernetes.mode", "kubeconfig", "inCluster"},
		{"plugins.kubernetes.kubeconfig", "/etc/dash/kubeconfig", "/etc/dash/kubeconfig-2"},
		{"plugins.kubernetes.context", "lab", "lab-2"},
		{"plugins.kubernetes.qps", "30", "31"},
		{"plugins.kubernetes.burst", "60", "61"},
		{"plugins.kubernetes.etcd.job", "etcd-main", "etcd-2"},
		{"plugins.kubernetes.optional.certificates", "true", "false"},
		{"plugins.kubernetes.optional.externalSecrets", "true", "false"},
		{"plugins.kubernetes.optional.httpRoutes", "true", "false"},
		{"plugins.kubernetes.conventions.protectedLabel", "example.com/protected", "example.com/protected-2"},
		{"plugins.kubernetes.conventions.tenantLabel", "example.com/tenant", "example.com/tenant-2"},
		{"plugins.kubernetes.conventions.appsFluxPrefix", "apps-", "apps2-"},
		{"plugins.kubernetes.upstreamViaAPIServer", "true", "false"},
		{"plugins.kubernetes.actions.enabled", "true", "false"},
		{"plugins.kubernetes.actions.denyNamespaces", `["kube-system","secrets"]`, `["kube-system"]`},
		{"plugins.kubernetes.backupJobs",
			`[{"namespace":"backup","cronjob":"nas-backup","label":"NAS"}]`,
			`[{"namespace":"backup","cronjob":"db-backup","label":"DB"}]`},
	}
}

// jsonField is the field of a struct (looking into embedded sections) that has the given json name.
func jsonField(v reflect.Value, name string) (reflect.Value, bool) {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && tag == "" {
			if inner, ok := jsonField(v.Field(i), name); ok {
				return inner, true
			}
			continue
		}
		if tag == name {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// leafOf is the field at path (relative to its section, dotted json names) of the section value.
func leafOf(t *testing.T, section any, path string) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(section)
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	for name := range strings.SplitSeq(path, ".") {
		f, ok := jsonField(v, name)
		if !ok {
			t.Fatalf("%T has no field %q (of %s)", section, name, path)
		}
		v = f
	}
	return v
}

// wantLeaf is the value of type typ that the environment text s stands for.
func wantLeaf(t *testing.T, typ reflect.Type, s string) reflect.Value {
	t.Helper()
	p := reflect.New(typ)
	switch {
	case typ.Kind() == reflect.String:
		p.Elem().SetString(s)
	default:
		if err := json.Unmarshal([]byte(s), p.Interface()); err != nil {
			t.Fatalf("%q is not a %v: %v", s, typ, err)
		}
	}
	return p.Elem()
}

// requireLeaf fails unless the section's field at path holds what the environment text want says.
func requireLeaf(t *testing.T, section any, leaf, want string) {
	t.Helper()
	got := leafOf(t, section, leaf)
	if r, ok := got.Interface().(interface{ Reveal() string }); ok {
		if r.Reveal() != want {
			t.Errorf("%s: the plugin received a different secret than the one set", leaf)
		}
		return
	}
	if w := wantLeaf(t, got.Type(), want); !reflect.DeepEqual(got.Interface(), w.Interface()) {
		t.Errorf("%s: the plugin received %#v, want %#v", leaf, got.Interface(), w.Interface())
	}
}

// allSet fails for a field of the struct type of v that no element is non-zero in: a key of a
// list entry that the test values never exercise.
func allSet(t *testing.T, what string, v reflect.Value) {
	t.Helper()
	if v.Kind() != reflect.Slice || v.Len() == 0 {
		t.Fatalf("%s is not a list with entries", what)
	}
	var walk func(prefix string, elems []reflect.Value)
	walk = func(prefix string, elems []reflect.Value) {
		for i := range elems[0].NumField() {
			name := elems[0].Type().Field(i).Name
			used := false
			var inner []reflect.Value
			for _, e := range elems {
				f := e.Field(i)
				if !f.IsZero() {
					used = true
				}
				if f.Kind() == reflect.Struct {
					inner = append(inner, f)
				}
			}
			if !used {
				t.Errorf("%s: %s%s has no value in any entry: the test does not cover this key", what, prefix, name)
			} else if len(inner) > 0 {
				walk(prefix+name+".", inner)
			}
		}
	}
	var elems []reflect.Value
	for i := range v.Len() {
		elems = append(elems, v.Index(i))
	}
	walk("", elems)
}

// fileDoc is a config file (JSON, which the file layer reads as YAML) that sets one setting.
func fileDoc(path, value string) string {
	var v any = value
	if json.Valid([]byte(value)) {
		v = json.RawMessage(value)
	}
	parts := strings.Split(path, ".")
	for i := len(parts) - 1; i >= 0; i-- {
		v = map[string]any{parts[i]: v}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}

// Every setting of plugins.proxmox and plugins.hardware arrives, through the real environment and
// file layers, treeSettings.Instances() and the supervisor, in the configuration the plugin is
// started with, and a reload of the file layer restarts the instance with the new value. The
// tables cover the whole sections (and the keys of the hosts and machines entries), so a key
// added without a row here fails.
func TestPR2SettingsReachThePlugin(t *testing.T) {
	for _, sec := range []struct {
		id     plugin.ID
		leaves []reachLeaf
		plugin plugin.Plugin
	}{
		{"proxmox", proxmoxLeaves(), proxmox.New()},
		{"hardware", hardwareLeaves(), hardware.New()},
	} {
		t.Run(string(sec.id), func(t *testing.T) { settingsReachThePlugin(t, sec.id, sec.leaves, sec.plugin) })
	}
}

// Every setting of plugins.kubernetes that exists in this release (P3.9 adds the rows of
// plugins.kubernetes.apps.* with the keys) arrives in the configuration the plugin is started
// with, and a reload of the file layer restarts the instance with the new value.
func TestPR3aSettingsReachThePlugin(t *testing.T) {
	settingsReachThePlugin(t, "kubernetes", kubernetesLeaves(), kubernetes.New())
}

// settingsReachThePlugin is the check of one section against its table of leaves: the table
// covers every environment variable of the section (a new key without a row fails), the leaf under
// test comes from the file layer while the others come from the environment, and the plugin's
// section type receives them all.
func settingsReachThePlugin(t *testing.T, id plugin.ID, leaves []reachLeaf, pl plugin.Plugin) {
	prefix := "plugins." + string(id) + "."
	names := map[string]string{}
	var paths, rows []string
	for _, v := range config.EnvVars() {
		if strings.HasPrefix(v.Path, prefix) {
			paths = append(paths, v.Path)
			names[v.Path] = v.Name
		}
	}
	for _, l := range leaves {
		rows = append(rows, l.path)
	}
	slices.Sort(paths)
	slices.Sort(rows)
	if !slices.Equal(paths, rows) {
		t.Fatalf("%s settings %v, this test sets %v", prefix, paths, rows)
	}
	// The keys of a list entry that no test value sets would be unexercised.
	switch id {
	case "hardware":
		var hosts []pluginconf.HardwareHost
		var machines []pluginconf.HardwareMachine
		for _, l := range leaves {
			var err error
			switch l.path {
			case "plugins.hardware.hosts":
				err = json.Unmarshal([]byte(l.env), &hosts)
			case "plugins.hardware.machines":
				err = json.Unmarshal([]byte(l.env), &machines)
			}
			if err != nil {
				t.Fatalf("%s: %v", l.path, err)
			}
		}
		allSet(t, "plugins.hardware.hosts", reflect.ValueOf(hosts))
		allSet(t, "plugins.hardware.machines", reflect.ValueOf(machines))
	case "kubernetes":
		var jobs []pluginconf.BackupJob
		for _, l := range leaves {
			if l.path == "plugins.kubernetes.backupJobs" {
				if err := json.Unmarshal([]byte(l.env), &jobs); err != nil {
					t.Fatalf("%s: %v", l.path, err)
				}
			}
		}
		allSet(t, "plugins.kubernetes.backupJobs", reflect.ValueOf(jobs))
	}

	for _, leaf := range leaves {
		t.Run(strings.TrimPrefix(leaf.path, prefix), func(t *testing.T) {
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.yaml")
			writeTestFile(t, cfgPath, fileDoc(leaf.path, leaf.env)) // the leaf under test comes from the file
			env := []string{"DASH_CONFIG=" + cfgPath}
			for _, other := range leaves {
				if other.path != leaf.path {
					env = append(env, names[other.path]+"="+other.env)
				}
			}
			startEnv(t, dir, env...)
			fw, res := loadFramework(t)

			// The instance the supervisor is given.
			var ic *plugin.InstanceConfig
			for _, x := range (treeSettings{func() config.Tree { return res.Tree }}).Instances() {
				if x.Name == id {
					ic = &x
				}
			}
			if ic == nil {
				t.Fatalf("no %s instance", id)
			}
			if ic.Type != id || ic.Enabled == nil || !*ic.Enabled {
				t.Errorf("instance = type %q enabled %v", ic.Type, ic.Enabled)
			}

			// The plugin's section type, under a fake that records what Start is given.
			f := plugintest.New(id, plugintest.Config(pl.Manifest().Config))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := fw.start(ctx, []plugin.Factory{f.Factory()}, func() {}); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { defer close(done); fw.reloader.Run(ctx) }()
			defer func() { cancel(); <-done; fw.sup.Shutdown(context.Background()) }()
			waitFor(t, func() bool { return f.Starts() == 1 })
			for _, l := range leaves {
				requireLeaf(t, f.Config(), strings.TrimPrefix(l.path, prefix), l.env)
			}

			if leaf.next == "" {
				return
			}
			// A reload of the file layer restarts the instance, which gets the new value.
			writeTestFile(t, cfgPath, fileDoc(leaf.path, leaf.next))
			waitFor(t, func() bool { return f.Starts() == 2 })
			requireLeaf(t, f.Config(), strings.TrimPrefix(leaf.path, prefix), leaf.next)
			for _, l := range leaves {
				if l.path != leaf.path {
					requireLeaf(t, f.Config(), strings.TrimPrefix(l.path, prefix), l.env)
				}
			}
		})
	}
}
