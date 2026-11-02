package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/hardware"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
)

// The environment of the homelab deployment, one file per release: the lists that internal/config's
// TestDeploymentEnvV08 and TestDeploymentEnvV09 and this test read.
var (
	deploymentEnvV08 = filepath.Join("..", "..", "internal", "config", "testdata", "deployment-v0.8.0.env")
	deploymentEnvV09 = filepath.Join("..", "..", "internal", "config", "testdata", "deployment-v0.9.0.env")
)

func deploymentEnv(t *testing.T, file string) []string {
	t.Helper()
	b, err := os.ReadFile(file)
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

// The deployment environments (contract 10.2 and 10.3) reach the plugins as the deploy needs it:
// the configuration each is started with carries the settings that changed and the ones that
// stayed, the running hardware plugin publishes the host with its layout and the two machines,
// and the kubernetes plugin gets its conventions and backup CronJob, and the Restart action only
// from v0.9.0 on.
func TestDeploymentEnvReachesThePlugins(t *testing.T) {
	t.Run("v0.8.0", func(t *testing.T) { deploymentEnvReachesThePlugins(t, deploymentEnvV08, false) })
	t.Run("v0.9.0", func(t *testing.T) { deploymentEnvReachesThePlugins(t, deploymentEnvV09, true) })
}

func deploymentEnvReachesThePlugins(t *testing.T, file string, wantRestart bool) {
	// What the environment must resolve to, pinned here and not read back from the file it
	// checks (internal/config's TestDeploymentEnvV08 and V09 pin the same values).
	wantMachines := []pluginconf.HardwareMachine{
		{Name: "mac-mini", NodeExporterNodename: "mac-mini"},
		{Name: "rpi-control", GatusEndpoint: "rpi-control SSH"},
	}
	var mu sync.Mutex
	var asked []string
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		asked = append(asked, r.Form.Get("query"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prom.Close()
	pve := newFakePVEFor(t, "pve-730")
	// The deployment's own hosts are not reachable here: the two upstreams are local fakes.
	env := append(deploymentEnv(t, file), "DASH_PLUGINS_PROMETHEUS_URL="+prom.URL, "DASH_PLUGINS_PROXMOX_URL="+pve.URL)

	// The configuration of each plugin as the supervisor starts it.
	t.Run("start config", func(t *testing.T) {
		startEnv(t, t.TempDir(), env...)
		fw, res := loadFramework(t)
		seen := map[plugin.ID]*plugintest.Fake{}
		var facts []plugin.Factory
		for _, real := range []plugin.Plugin{proxmox.New(), hardware.New(), kubernetes.New()} {
			m := real.Manifest()
			f := plugintest.New(m.ID, plugintest.Config(m.Config))
			seen[m.ID] = f
			facts = append(facts, f.Factory())
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := fw.start(ctx, facts, func() {}); err != nil {
			t.Fatal(err)
		}
		defer fw.sup.Shutdown(context.Background())
		waitFor(t, func() bool { return seen["proxmox"].Starts() == 1 && seen["hardware"].Starts() == 1 && seen["kubernetes"].Starts() == 1 })
		px, ok := seen["proxmox"].Config().(*pluginconf.Proxmox)
		if !ok {
			t.Fatalf("proxmox is started with %T", seen["proxmox"].Config())
		}
		if px.Node != "pve-730" || !slices.Equal(px.CriticalVMs.IDs, []int{2020, 2021, 2022}) || px.StorageSource() != "prometheus" {
			t.Errorf("proxmox start config: node %q, critical ids %v, storage source %q", px.Node, px.CriticalVMs.IDs, px.StorageSource())
		}
		hw, ok := seen["hardware"].Config().(*pluginconf.Hardware)
		if !ok {
			t.Fatalf("hardware is started with %T", seen["hardware"].Config())
		}
		wantHosts := []pluginconf.HardwareHost{{Model: "Dell R730", Bays: 16, FanMaxRPM: 18000}}
		if !slices.Equal(hw.Hosts, wantHosts) || !slices.Equal(hw.Machines, wantMachines) || hw.ZFSScrubMetricPrefix != "homelab_zfs_scrub" {
			t.Errorf("hardware start config: hosts %+v, machines %+v, zfs prefix %q", hw.Hosts, hw.Machines, hw.ZFSScrubMetricPrefix)
		}
		if res.Tree.Plugins.Proxmox.Node != px.Node {
			t.Errorf("the tree and the plugin disagree on the node: %q and %q", res.Tree.Plugins.Proxmox.Node, px.Node)
		}
		k8s, ok := seen["kubernetes"].Config().(*pluginconf.Kubernetes)
		if !ok {
			t.Fatalf("kubernetes is started with %T", seen["kubernetes"].Config())
		}
		wantConventions := pluginconf.KubernetesConventions{ProtectedLabel: "example.com/protected", TenantLabel: "example.com/tenant", AppsFluxPrefix: "apps-"}
		wantJobs := []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nightly"}}
		if k8s.Conventions != wantConventions || !slices.Equal(k8s.BackupJobs, wantJobs) {
			t.Errorf("kubernetes start config: conventions %+v, backup jobs %+v", k8s.Conventions, k8s.BackupJobs)
		}
		if k8s.Actions.Enabled != wantRestart || len(k8s.Actions.DenyNamespaces) != 0 {
			t.Errorf("kubernetes start config: actions %+v, want enabled=%v and no denied namespace", k8s.Actions, wantRestart)
		}
		// The apps list is v0.9.0's: before it the keys do not exist in the environment and the
		// generic defaults apply.
		wantApps := pluginconf.KubernetesApps{AnnotationPrefix: "gethomepage.dev"}
		if wantRestart {
			wantApps.Gateways = []pluginconf.AppsGateway{{Namespace: "gateway", Name: "home"}}
			wantApps.ExcludeNamespaces = []string{"dash"}
		}
		if !reflect.DeepEqual(k8s.Apps, wantApps) {
			t.Errorf("kubernetes start config: apps %+v, want %+v", k8s.Apps, wantApps)
		}
	})

	// The same environment with the real plugins: what a viewer's stream carries.
	t.Run("published", func(t *testing.T) {
		startEnv(t, t.TempDir(), env...)
		fw, _ := loadFramework(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := fw.start(ctx, []plugin.Factory{prometheus.New, proxmox.New, hardware.New}, func() {}); err != nil {
			t.Fatal(err)
		}
		defer fw.sup.Shutdown(context.Background())
		var host model.HostData
		var machines hwmodel.MachinesData
		waitFor(t, func() bool {
			var e model.Envelope
			ev, ok := fw.hub.Latest("", model.TopicHost)
			if !ok || json.Unmarshal(ev.Data, &e) != nil || json.Unmarshal(e.Data, &host) != nil {
				return false
			}
			ev, ok = fw.hub.Latest("", model.TopicHardwareMachines)
			return ok && json.Unmarshal(ev.Data, &e) == nil && json.Unmarshal(e.Data, &machines) == nil
		})
		if host.Ref != "hardware:Host/pve-730" || host.Name != "pve-730" || host.Model != "Dell R730" || host.Bays != 16 || host.FanMaxRPM != 18000 ||
			host.SensorAliases != (model.SensorAliases{Inlet: "inlet", Exhaust: "exhaust"}) || host.TempWarnC != 85 || host.TempCritC != 90 || !host.ScrubTracked {
			t.Errorf("hardware.hosts = %+v", host)
		}
		got := map[string]hwmodel.Machine{}
		for _, m := range machines.Machines {
			got[m.Name] = m
		}
		if len(got) != len(wantMachines) {
			t.Errorf("hardware.machines = %+v, want the %d of the environment", machines.Machines, len(wantMachines))
		}
		wantQueries := []string{`nodename="pve-730"`, "homelab_zfs_scrub_last_errors"}
		for _, w := range wantMachines {
			wantSource := "gatus"
			if w.NodeExporterNodename != "" {
				wantSource = "prometheus"
				wantQueries = append(wantQueries, `up{job="node-exporter",nodename="`+w.NodeExporterNodename+`"}`)
			}
			if m, ok := got[w.Name]; !ok || m.Source != wantSource {
				t.Errorf("machine %q = %+v, want source %s", w.Name, m, wantSource)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		for _, want := range wantQueries {
			if !slices.ContainsFunc(asked, func(q string) bool { return strings.Contains(q, want) }) {
				t.Errorf("Prometheus was never asked for %q: %v", want, asked)
			}
		}
	})
}
