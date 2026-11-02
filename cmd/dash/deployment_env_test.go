package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
)

// deploymentEnvFile is the environment of the homelab deployment at v0.8.0: the one list that
// internal/config's TestDeploymentEnvV08 and this test read.
var deploymentEnvFile = filepath.Join("..", "..", "internal", "config", "testdata", "deployment-v0.8.0.env")

func deploymentEnv(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(deploymentEnvFile)
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

// The v0.8.0 deployment environment (contract 10.2) reaches both plugins as the deploy needs it:
// the configuration each is started with carries the four changed settings and the ones that
// stayed, and the running hardware plugin publishes the host with its layout and the two machines.
func TestDeploymentEnvReachesThePlugins(t *testing.T) {
	// What the environment must resolve to, pinned here and not read back from the file it
	// checks (internal/config's TestDeploymentEnvV08 pins the same values).
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
	env := append(deploymentEnv(t), "DASH_PLUGINS_PROMETHEUS_URL="+prom.URL, "DASH_PLUGINS_PROXMOX_URL="+pve.URL)

	// The configuration of each plugin as the supervisor starts it.
	t.Run("start config", func(t *testing.T) {
		startEnv(t, t.TempDir(), env...)
		fw, res := loadFramework(t)
		seen := map[plugin.ID]*plugintest.Fake{}
		var facts []plugin.Factory
		for _, real := range []plugin.Plugin{proxmox.New(), hardware.New()} {
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
		waitFor(t, func() bool { return seen["proxmox"].Starts() == 1 && seen["hardware"].Starts() == 1 })
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
