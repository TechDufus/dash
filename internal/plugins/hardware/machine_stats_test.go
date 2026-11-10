package hardware

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/svc"
)

// statsMachines are a Linux box, a macOS one and a Gatus one.
func statsMachines(c *pluginconf.Hardware) {
	c.Machines = []pluginconf.HardwareMachine{
		{Name: "nas", NodeExporterNodename: "nas-1"},
		{Name: "studio", NodeExporterNodename: "studio.local"},
		{Name: "pi", GatusEndpoint: "pi SSH"},
	}
}

// fillStats answers the batched stats queries the way a Linux node-exporter (nas-1, with a hwmon
// temperature) and a macOS one (studio.local, darwin memory names, no temperature) do: each
// figure query returns one sample per nodename that has it.
func fillStats(q *fakeQuerier, x exprs) {
	qs := x.machineStats([]string{"nas-1", "studio.local"})
	n := func(v float64, nodename string, kv ...string) svc.Sample {
		return sample(v, append([]string{"nodename", nodename}, kv...)...)
	}
	q.set(qs["uname"],
		n(1, "nas-1", "sysname", "Linux", "machine", "x86_64", "release", "6.12.0"),
		n(1, "studio.local", "sysname", "Darwin", "machine", "arm64", "release", "25.0.0"))
	q.set(qs["cpus"], n(8, "nas-1"), n(10, "studio.local"))
	q.set(qs["cpu"], n(0.25, "nas-1"), n(0.1, "studio.local"))
	// The memory queries fall back with or: the Linux names answer for nas-1, the darwin names
	// for studio.local, so each figure has one sample per machine.
	q.set(qs["memTotal"], n(32<<30, "nas-1"), n(16<<30, "studio.local"))
	q.set(qs["memUsed"], n(8<<30, "nas-1"), n(10<<30, "studio.local"))
	q.set(qs["diskTotal"], n(200<<30, "nas-1"), n(460<<30, "studio.local"))
	q.set(qs["diskAvail"], n(150<<30, "nas-1"), n(260<<30, "studio.local"))
	q.set(qs["load1"], n(1.5, "nas-1"), n(2, "studio.local"))
	q.set(qs["load5"], n(1.2, "nas-1"), n(1.8, "studio.local"))
	q.set(qs["load15"], n(1, "nas-1"), n(1.6, "studio.local"))
	q.set(qs["uptime"], n(86400, "nas-1"), n(3600, "studio.local"))
	q.set(qs["temp"], n(47, "nas-1"))
	q.set(x.machineUp("nas-1"), sample(1))
	q.set(x.machineUp("studio.local"), sample(1))
}

func statsRuntime(t *testing.T) (*Plugin, *runtime, *stubHost, *fakeQuerier) {
	t.Helper()
	p, rt, h := stubRuntime(t, statsMachines)
	q := newFakeQuerier()
	fillStats(q, rt.q)
	h.setService("prometheus", q)
	h.setService("gatus", newFakeEndpoints(1))
	return p, rt, h, q
}

func lastMachines(t *testing.T, h *stubHost) map[string]hwmodel.Machine {
	t.Helper()
	pubs := h.published("machines")
	if len(pubs) == 0 {
		t.Fatal("no machines published")
	}
	out := map[string]hwmodel.Machine{}
	for _, m := range pubs[len(pubs)-1].data.(hwmodel.MachinesData).Machines {
		out[m.Name] = m
	}
	return out
}

// A Linux machine reports its memory as MemTotal - MemAvailable and a temperature; a macOS one
// its darwin memory and no temperature; a Gatus machine has no stats. Every figure joins its
// machine by nodename.
func TestMachineStatsLinuxAndDarwin(t *testing.T) {
	p, rt, h, _ := statsRuntime(t)
	p.machinesTick(context.Background(), rt)
	ms := lastMachines(t, h)

	nas := ms["nas"].Stats
	if nas == nil || nas.OS != "Linux" || nas.Arch != "x86_64" || nas.Kernel != "6.12.0" || nas.CPUs != 8 || nas.CPURatio != 0.25 ||
		nas.MemTotalBytes != 32<<30 || nas.MemUsedBytes != 8<<30 || nas.DiskTotalBytes != 200<<30 || nas.DiskUsedBytes != 50<<30 ||
		nas.Load1 != 1.5 || nas.Load5 != 1.2 || nas.Load15 != 1 || nas.UptimeSeconds != 86400 || nas.TempC == nil || *nas.TempC != 47 {
		t.Errorf("linux stats = %+v", nas)
	}
	mac := ms["studio"].Stats
	if mac == nil || mac.OS != "Darwin" || mac.Arch != "arm64" || mac.CPUs != 10 || mac.MemTotalBytes != 16<<30 || mac.MemUsedBytes != 10<<30 ||
		mac.DiskUsedBytes != 200<<30 || mac.UptimeSeconds != 3600 {
		t.Errorf("darwin stats = %+v", mac)
	}
	if mac != nil && mac.TempC != nil {
		t.Errorf("darwin temperature = %v, want none", *mac.TempC)
	}
	if ms["pi"].Stats != nil {
		t.Errorf("a Gatus machine has stats: %+v", ms["pi"].Stats)
	}
}

// The stats queries are one per figure for every machine: the job and an anchored alternation of
// the escaped nodenames, the darwin memory fallback, the root file system without rootfs, and the
// thermal-zone fallback of the temperature.
func TestMachineStatsQueriesAreBatched(t *testing.T) {
	x := newExprs(pluginconf.DefaultHardware().Selectors)
	qs := x.machineStats([]string{"nas-1", "studio.local"})
	sel := `job="node-exporter",nodename=~"^(nas-1|studio\\.local)$"`
	for key, want := range map[string][]string{
		"memTotal":  {"node_memory_MemTotal_bytes{" + sel + "}", " or ", "node_memory_total_bytes{" + sel + "}"},
		"memUsed":   {"node_memory_MemAvailable_bytes", " or ", "node_memory_internal_bytes", "node_memory_purgeable_bytes", "node_memory_wired_bytes", "node_memory_compressed_bytes"},
		"diskTotal": {`mountpoint="/",fstype!="rootfs"`, "max by (nodename)"},
		"cpus":      {"count by (nodename)", `mode="idle"`},
		"temp":      {`chip=~"platform_coretemp_.*",sensor="temp1"`, " or ", `node_thermal_zone_temp{` + sel + `,type=~"cpu-thermal|cpu_thermal|x86_pkg_temp|soc-thermal|soc_thermal"}`},
	} {
		for _, w := range want {
			if !strings.Contains(qs[key], w) {
				t.Errorf("%s = %s, want it to contain %s", key, qs[key], w)
			}
		}
	}

	// One poll of 2 and of 4 node-exporter machines sends the same number of stats queries: only
	// the per-machine up queries grow.
	statsSent := func(n int) int {
		p, rt, h := stubRuntime(t, func(c *pluginconf.Hardware) {
			for i := range n {
				name := fmt.Sprintf("m-%d", i)
				c.Machines = append(c.Machines, pluginconf.HardwareMachine{Name: name, NodeExporterNodename: name})
			}
		})
		q := newFakeQuerier()
		h.setService("prometheus", q)
		p.pollMachines(context.Background(), rt, q)
		ups := 0
		for _, e := range q.asked() {
			if strings.HasPrefix(e, "up{") {
				ups++
			}
		}
		if ups != n {
			t.Errorf("%d machines asked %d up queries", n, ups)
		}
		return len(q.asked()) - ups
	}
	if two, four := statsSent(2), statsSent(4); two != four || two == 0 {
		t.Errorf("stats queries: %d for two machines, %d for four; want the same, not zero", two, four)
	}
}

// A figure whose query fails keeps its last value; a query that succeeds without the machine
// clears it (the temperature to none); a down machine shows no stats; a machine that reports
// neither uname nor CPUs has none.
func TestMachineStatsKeepClearAndHide(t *testing.T) {
	p, rt, h, q := statsRuntime(t)
	ctx := context.Background()
	p.machinesTick(ctx, rt)
	qs := rt.q.machineStats([]string{"nas-1", "studio.local"})

	q.setErr(qs["memTotal"], errors.New("boom"))
	q.set(qs["temp"]) // nas-1's sensor went away
	h.advance(fastTiming().machines)
	p.machinesTick(ctx, rt)
	nas := lastMachines(t, h)["nas"].Stats
	if nas == nil || nas.MemTotalBytes != 32<<30 || nas.TempC != nil {
		t.Errorf("after a failed memory query and a missing temperature: %+v", nas)
	}

	q.set(rt.q.machineUp("studio.local"), sample(0))
	h.advance(fastTiming().machines)
	p.machinesTick(ctx, rt)
	if m := lastMachines(t, h)["studio"]; m.Up || m.Stats != nil {
		t.Errorf("a down machine = %+v, want no stats", m)
	}

	q.set(qs["uname"])
	q.set(qs["cpus"])
	h.advance(fastTiming().machines)
	p.machinesTick(ctx, rt)
	if s := lastMachines(t, h)["nas"].Stats; s != nil {
		t.Errorf("a machine without uname and CPUs = %+v, want no stats", s)
	}
}

// The context of a machine carries its stats as facts.
func TestMachineContextCarriesStats(t *testing.T) {
	f := statsFacts(hwmodel.MachineStats{OS: "Darwin", CPUs: 10, MemTotalBytes: 16 << 30})
	byName := map[string]string{}
	for _, x := range f {
		byName[x.Name] = x.Value
	}
	if byName["stats.os"] != "Darwin" || byName["stats.cpus"] != "10" || byName["stats.memTotalBytes"] != "17179869184" {
		t.Errorf("facts = %+v", f)
	}
	if _, ok := byName["stats.tempC"]; ok {
		t.Error("a missing temperature is a fact")
	}
}
