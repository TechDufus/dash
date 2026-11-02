package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// DeploymentEnvFile is the environment of the homelab deployment at v0.8.0, relative to this
// package: testdata/deployment-v0.8.0.env, the one list TestDeploymentEnvV08 and cmd/dash's
// TestDeploymentEnvReachesThePlugins both read.
const deploymentEnvFile = "testdata/deployment-v0.8.0.env"

// deploymentEnv reads NAME=value lines (blank lines and # comments skipped, the value verbatim).
func deploymentEnv(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(deploymentEnvFile))
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
		t.Fatalf("%s lists no variable", deploymentEnvFile)
	}
	return out
}

// TestDeploymentEnvV08 loads the whole environment the homelab deployment sets at v0.8.0
// (contract 10.2: the v0.7.0 environment, the three PR-1 additions and the four variables PR-2
// changes) through the real environment layer, and asserts the tree it resolves to. A name the
// layer does not know, or a value the validators refuse, fails here and not as a crash loop after
// the deploy.
func TestDeploymentEnvV08(t *testing.T) {
	env := deploymentEnv(t)
	res, err := Resolve(Sources{Environ: env})
	if err != nil {
		t.Fatalf("the v0.8.0 environment does not load: %v", err)
	}
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
	// The sections that have no hosts or machines of their own keep what v0.7.0 set.
	if c := res.Config.Conventions; c.ProtectedLabel != "example.com/protected" || c.TenantLabel != "example.com/tenant" ||
		c.AppsFluxPrefix != "apps-" || c.SuspendReasonAnnotation != "example.com/suspended" ||
		c.NASBackupNamespace != "backup" || c.NASBackupCronJob != "nightly" {
		t.Errorf("conventions = %+v", c)
	}
}
