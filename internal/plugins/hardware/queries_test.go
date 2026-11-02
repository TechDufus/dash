package hardware

import (
	"strconv"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
)

func defaultExprs() exprs { return newExprs(pluginconf.DefaultHardware().Selectors) }

// The default selectors give exactly the expressions the topic Manager has always sent, so a
// homelab that sets nothing sees the same queries. The strings are pinned.
func TestDefaultExpressionsAreTheLegacyOnes(t *testing.T) {
	x := defaultExprs()
	for name, c := range map[string]struct{ got, want string }{
		"machine up":   {x.machineUp("mac"), `up{job="node-exporter",nodename="mac"}`},
		"cpu":          {x.cpu("n"), `1 - avg(rate(node_cpu_seconds_total{nodename="n",mode="idle"}[2m]))`},
		"mem":          {x.mem("n"), `1 - node_memory_MemAvailable_bytes{nodename="n"} / node_memory_MemTotal_bytes{nodename="n"}`},
		"memTotal":     {x.memTotal("n"), `node_memory_MemTotal_bytes{nodename="n"}`},
		"memAvailable": {x.memAvailable("n"), `node_memory_MemAvailable_bytes{nodename="n"}`},
		"package":      {x.packageTemps("n"), `node_hwmon_temp_celsius{nodename="n",chip=~"platform_coretemp_.*",sensor="temp1"}`},
		"cpuTempMax":   {x.cpuTempMax("n"), `max(node_hwmon_temp_celsius{nodename="n",chip=~"platform_coretemp_.*",sensor="temp1"})`},
		"sensors":      {x.sensors("n"), `ipmi_temperature_celsius{nodename="n"}`},
		"fans":         {x.fans("n"), `ipmi_fan_speed_rpm{nodename="n"}`},
		"power":        {x.power("n"), `ipmi_power_watts{nodename="n"}`},
		"psus":         {x.psus("n"), `ipmi_sensor_state{nodename="n",type=~"Power Supply|Power Unit"}`},
		"selFree":      {x.selFree("n"), `ipmi_sel_free_space_bytes{nodename="n"}`},
		"pools":        {x.pools("n"), `node_zfs_zpool_state{nodename="n"} == 1`},
		"scrubEnd":     {x.scrubEnd("p", "n"), `p_last_end_timestamp_seconds{nodename="n"}`},
		"scrubErrors":  {x.scrubErrors("p", "n"), `p_last_errors{nodename="n"}`},
		"scrubRunning": {x.scrubRunning("p", "n"), `p_in_progress{nodename="n"}`},
		"healthy":      {x.smartHealthy("n"), `smartmon_device_smart_healthy{nodename="n"}`},
		"temp":         {x.smartTemp("n"), `smartmon_temperature_celsius_raw_value{nodename="n"}`},
		"life":         {x.smartLifeRemain("n"), `smartmon_percent_lifetime_remain_value{nodename="n"}`},
		"wear":         {x.smartWearout("n"), `smartmon_media_wearout_indicator_value{nodename="n"}`},
	} {
		if c.got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", name, c.got, c.want)
		}
	}
}

// plugins.hardware.selectors.* appear in the machine-up and the CPU-package queries, and in no
// other.
func TestSelectorsChangeTheQueries(t *testing.T) {
	x := newExprs(pluginconf.HardwareSelectors{NodeExporterJob: "ne-lab", HwmonChip: "k10temp_.*"})
	if got, want := x.machineUp("nas"), `up{job="ne-lab",nodename="nas"}`; got != want {
		t.Errorf("machine up = %s, want %s", got, want)
	}
	if got, want := x.packageTemps("n"), `node_hwmon_temp_celsius{nodename="n",chip=~"k10temp_.*",sensor="temp1"}`; got != want {
		t.Errorf("package = %s, want %s", got, want)
	}
	if got := x.cpuTempMax("n"); !strings.Contains(got, `chip=~"k10temp_.*"`) {
		t.Errorf("cpuTempMax = %s", got)
	}
	d := defaultExprs()
	if x.power("n") != d.power("n") || x.cpu("n") != d.cpu("n") || x.pools("n") != d.pools("n") {
		t.Error("a selector changed a query it does not belong to")
	}
}

// A value with a quote or a backslash stays one PromQL string literal: an injected matcher is
// text, not a query.
func TestConfiguredValuesCannotBreakOutOfTheirString(t *testing.T) {
	evil := `x"} or vector(1) or up{a="`
	x := newExprs(pluginconf.HardwareSelectors{NodeExporterJob: evil, HwmonChip: evil})
	for name, got := range map[string]string{
		"job":      x.machineUp(evil),
		"chip":     x.packageTemps(evil),
		"nodename": x.power(evil),
		"scrub":    x.scrubEnd("p", evil),
	} {
		lit := strconv.Quote(evil)
		if !strings.Contains(got, lit) {
			t.Errorf("%s: %s does not hold the escaped literal %s", name, got, lit)
		}
		if strings.Contains(strings.ReplaceAll(got, lit, ""), `vector(1)`) {
			t.Errorf("%s: the value escaped its string: %s", name, got)
		}
	}
}

// Without a prefix no scrub query is built; with one there are three.
func TestScrubQueriesFollowThePrefix(t *testing.T) {
	x := defaultExprs()
	for _, k := range []string{"scrubEnd", "scrubErr", "scrubRun"} {
		if _, ok := x.hostQueries("n", "")[k]; ok {
			t.Errorf("%s is queried with no prefix", k)
		}
		if _, ok := x.hostQueries("n", "node_zfs_scrub")[k]; !ok {
			t.Errorf("%s is missing with a prefix", k)
		}
	}
	if len(x.hostQueries("n", "")) != 14 || len(x.hostQueries("n", "p")) != 17 {
		t.Errorf("query counts = %d, %d, want 14 and 17", len(x.hostQueries("n", "")), len(x.hostQueries("n", "p")))
	}
}
