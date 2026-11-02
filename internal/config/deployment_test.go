package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// The environment of the homelab deployment, one file per release, relative to this package: the
// lists TestDeploymentEnvV08 and TestDeploymentEnvV09 load and cmd/dash's
// TestDeploymentEnvReachesThePlugins reads too.
const (
	deploymentEnvFileV08 = "testdata/deployment-v0.8.0.env"
	deploymentEnvFileV09 = "testdata/deployment-v0.9.0.env"
)

// deploymentEnv reads NAME=value lines (blank lines and # comments skipped, the value verbatim).
func deploymentEnv(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(file))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s lists no variable", file)
	}
	return out
}

// TestDeploymentEnvV08 loads the whole environment the homelab deployment sets at v0.8.0
// (contract 10.2: the v0.7.0 environment, the three PR-1 additions and the four variables PR-2
// changes) through the real environment layer, and asserts the tree it resolves to. A name the
// layer does not know, or a value the validators refuse, fails here and not as a crash loop after
// the deploy.
func TestDeploymentEnvV08(t *testing.T) {
	res, err := Resolve(Sources{Environ: deploymentEnv(t, deploymentEnvFileV08)})
	if err != nil {
		t.Fatalf("the v0.8.0 environment does not load: %v", err)
	}
	assertHomelabEnvV08(t, res)
}

// TestDeploymentEnvV09 is the v0.8.0 environment plus what v0.9.0 adds (contract 10.3), loaded the
// same way: everything v0.8.0 resolved to still holds, and each new variable has its pinned value.
func TestDeploymentEnvV09(t *testing.T) {
	res, err := Resolve(Sources{Environ: deploymentEnv(t, deploymentEnvFileV09)})
	if err != nil {
		t.Fatalf("the v0.9.0 environment does not load: %v", err)
	}
	assertHomelabEnvV08(t, res)

	k := res.Tree.Plugins.Kubernetes
	// One row per variable v0.9.0 adds, with the value the deployment's behaviour needs. The two
	// rows of the apps list (plugins.kubernetes.apps.gateways = [{namespace: gateway, name: home}]
	// and plugins.kubernetes.apps.excludeNamespaces = [dash]) join with P3.9's keys.
	for _, row := range []struct {
		path      string
		got, want any
	}{
		{"plugins.kubernetes.actions.enabled", k.Actions.Enabled, true},
		// What the deployment leaves alone stays at the defaults: in-cluster, no dev proxy, no
		// denied namespace, the client's rates and etcd's job.
		{"plugins.kubernetes.mode", k.Mode, "auto"},
		{"plugins.kubernetes.kubeconfig", k.Kubeconfig, ""},
		{"plugins.kubernetes.upstreamViaAPIServer", k.UpstreamViaAPIServer, false},
		{"plugins.kubernetes.actions.denyNamespaces", len(k.Actions.DenyNamespaces), 0},
		{"plugins.kubernetes.qps", k.QPS, 20},
		{"plugins.kubernetes.burst", k.Burst, 40},
		{"plugins.kubernetes.etcd.job", k.Etcd.Job, "etcd"},
	} {
		if !reflect.DeepEqual(row.got, row.want) {
			t.Errorf("%s = %#v, want %#v", row.path, row.got, row.want)
		}
	}
}

// assertHomelabEnvV08 asserts what the v0.8.0 environment resolves to; the v0.9.0 environment
// keeps all of it.
func assertHomelabEnvV08(t *testing.T, res *Resolved) {
	t.Helper()
	p := res.Tree.Plugins

	if got := res.Config.PublicURL; got != "https://dash.example.com" {
		t.Errorf("public URL = %q", got)
	}
	if p.Proxmox.Node != "pve-730" {
		t.Errorf("proxmox node = %q", p.Proxmox.Node)
	}
	if want := []int{2020, 2021, 2022}; !reflect.DeepEqual(p.Proxmox.CriticalVMs.IDs, want) {
		t.Errorf("criticalVMs.ids = %v, want %v", p.Proxmox.CriticalVMs.IDs, want)
	}
	if p.Proxmox.Storage.Source != "prometheus" {
		t.Errorf("storage.source = %q, want prometheus", p.Proxmox.Storage.Source)
	}
	wantHosts := []pluginconf.HardwareHost{{Model: "Dell R730", Bays: 16, FanMaxRPM: 18000}}
	if !reflect.DeepEqual(p.Hardware.Hosts, wantHosts) {
		t.Errorf("hosts = %+v, want %+v", p.Hardware.Hosts, wantHosts)
	}
	wantMachines := []pluginconf.HardwareMachine{
		{Name: "mac-mini", NodeExporterNodename: "mac-mini"},
		{Name: "rpi-control", GatusEndpoint: "rpi-control SSH"},
	}
	if !reflect.DeepEqual(p.Hardware.Machines, wantMachines) {
		t.Errorf("machines = %+v, want %+v", p.Hardware.Machines, wantMachines)
	}
	if p.Hardware.ZFSScrubMetricPrefix != "homelab_zfs_scrub" {
		t.Errorf("zfsScrubMetricPrefix = %q", p.Hardware.ZFSScrubMetricPrefix)
	}
	// What the deployment leaves at the defaults, which equal v0.7.0's behaviour (contract 10.2).
	def := pluginconf.DefaultHardware()
	if p.Hardware.Selectors != def.Selectors || p.Hardware.TempThresholds != def.TempThresholds {
		t.Errorf("selectors %+v and thresholds %+v changed from the defaults", p.Hardware.Selectors, p.Hardware.TempThresholds)
	}
	if p.Hardware.Hosts[0].SensorAliases != (pluginconf.HardwareSensorAliases{}) {
		t.Errorf("sensorAliases = %+v, want unset (the plugin fills inlet and exhaust)", p.Hardware.Hosts[0].SensorAliases)
	}
	if !reflect.DeepEqual(p.Gatus.CriticalGroups, []string{"infrastructure"}) {
		t.Errorf("gatus criticalGroups = %v", p.Gatus.CriticalGroups)
	}
	if !p.Alertmanager.Actions.Enabled {
		t.Errorf("alertmanager actions.enabled = %v", p.Alertmanager.Actions.Enabled)
	}
	if got := res.Tree.UI.IconBaseURL; got != "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png" {
		t.Errorf("ui.iconBaseURL = %q", got)
	}
	// The sections that have no hosts or machines of their own keep what v0.7.0 set; the
	// kubernetes conventions and backup CronJob moved onto the plugin's keys.
	k := p.Kubernetes
	if k.Conventions.ProtectedLabel != "example.com/protected" || k.Conventions.TenantLabel != "example.com/tenant" ||
		k.Conventions.AppsFluxPrefix != "apps-" || res.Config.Conventions.SuspendReasonAnnotation != "example.com/suspended" ||
		len(k.BackupJobs) != 1 || k.BackupJobs[0].Namespace != "backup" || k.BackupJobs[0].CronJob != "nightly" {
		t.Errorf("conventions = %+v, backup jobs = %+v", k.Conventions, k.BackupJobs)
	}
}
