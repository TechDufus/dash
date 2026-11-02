package hardware

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

const scrubPrefix = "node_zfs_scrub"

// homelabShaped is a host configured the way a rack server is: a model, a 16-bay chassis and
// fans that top out at 18000 RPM.
func homelabShaped(c *pluginconf.Hardware) {
	c.Hosts = []pluginconf.HardwareHost{{Model: "Dell R730", Bays: 16, FanMaxRPM: 18000}}
	c.ZFSScrubMetricPrefix = scrubPrefix
}

func hostRig(t *testing.T, mod func(*pluginconf.Hardware)) *rig {
	t.Helper()
	r := newRig(t, rigOpts{
		cfg: func(c *pluginconf.Hardware) {
			homelabShaped(c)
			if mod != nil {
				mod(c)
			}
		},
		prefill: func(r *rig) { r.q.fillHost(r.ex, node, scrubPrefix) },
	})
	return r
}

// The host is polled from the querier and published with the plugin's own fields, every one
// filled: the refs, the layout settings and the thresholds (design 5.3: no empty placeholder).
func TestHostPublishesPolledValuesAndPluginFields(t *testing.T) {
	r := hostRig(t, nil)
	d, srcs := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts > 0 })

	if d.Name != node || d.Ref != "hardware:Host/"+node || d.Model != "Dell R730" {
		t.Errorf("name, ref, model = %q %q %q", d.Name, d.Ref, d.Model)
	}
	if d.PowerWatts != 214 || d.CPURatio != 0.25 || d.MemTotalBytes != 64<<30 || d.MemUsedBytes != 16<<30 || d.SELFreeBytes != 16384 {
		t.Errorf("scalars = %v %v %d %d %d", d.PowerWatts, d.CPURatio, d.MemTotalBytes, d.MemUsedBytes, d.SELFreeBytes)
	}
	if !slices.Equal(d.PackageTempsC, []float64{58, 62}) {
		t.Errorf("package temps = %v, want sorted [58 62]", d.PackageTempsC)
	}
	if len(d.Sensors) != 2 || d.Sensors[0].Name != "Exhaust Temp" || len(d.Fans) != 2 || d.Fans[0].Name != "Fan1" || len(d.PSUs) != 2 {
		t.Errorf("sensors, fans, psus = %+v %+v %+v", d.Sensors, d.Fans, d.PSUs)
	}
	if d.Bays != 16 || d.FanMaxRPM != 18000 || d.TempWarnC != 85 || d.TempCritC != 90 || d.CPUTempHealth != model.HealthOK || !d.ScrubTracked {
		t.Errorf("bays %d, fan scale %d, thresholds %d/%d, health %s, scrub tracked %v", d.Bays, d.FanMaxRPM, d.TempWarnC, d.TempCritC, d.CPUTempHealth, d.ScrubTracked)
	}
	if len(d.Pools) != 2 || d.Pools[0].Name != "rpool" || d.Pools[0].Ref != "hardware:Pool/"+node+"/rpool" || d.Pools[0].LastScrubEnd == nil {
		t.Errorf("pools = %+v", d.Pools)
	}
	if len(d.Disks) != 2 || d.Disks[0].Bay != "0" || d.Disks[1].Serial != "SER1" || d.Disks[1].LifeRemainingPct == nil || *d.Disks[1].LifeRemainingPct != 97 {
		t.Errorf("disks = %+v", d.Disks)
	}
	if d.Links == nil {
		t.Error("links is null, want []")
	}
	if !hasSource(srcs, "prometheus", true, "") || len(srcs) != 1 {
		t.Errorf("sources = %+v, want only prometheus, ok", srcs)
	}
	plugintest.AssertRefs(t, r.sup, ID, d, model.HostData{}, model.Pool{}, model.Disk{})
}

// A disk without a serial is identified by its bay.
func TestDiskRefUsesTheBayWhenThereIsNoSerial(t *testing.T) {
	r := hostRig(t, nil)
	d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return len(d.Disks) == 2 })
	if d.Disks[0].Ref != "hardware:Disk/"+node+"/bay-0" || d.Disks[1].Ref != "hardware:Disk/"+node+"/SER1" {
		t.Errorf("disk refs = %q %q", d.Disks[0].Ref, d.Disks[1].Ref)
	}
}

// A query that fails keeps the last good value of what it fed, and the sources say why; a poll
// that recovers clears it.
func TestFailedQueryKeepsTheLastGoodValues(t *testing.T) {
	r := hostRig(t, nil)
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts == 214 })

	r.q.setErr(r.ex.power(node), errors.New("boom"))
	r.q.set(r.ex.cpu(node), sample(0.5))
	d, srcs := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.CPURatio == 0.5 })
	if d.PowerWatts != 214 {
		t.Errorf("power = %v after a failed query, want the last good 214", d.PowerWatts)
	}
	if !hasSource(srcs, "prometheus", false, "unreachable") {
		t.Errorf("sources = %+v, want prometheus unreachable", srcs)
	}

	r.q.set(r.ex.power(node), sample(50))
	d, srcs = waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts == 50 })
	if !hasSource(srcs, "prometheus", true, "") {
		t.Errorf("sources = %+v, want prometheus ok after recovery", srcs)
	}
}

// A whole host that cannot be read still publishes (empty lists, never null) with the failing
// source, so the page says why it is empty.
func TestFailingPrometheusPublishesEmptyListsAndTheCategory(t *testing.T) {
	r := newRig(t, rigOpts{cfg: homelabShaped, qfail: errors.New("down")})
	d, srcs := waitTopic(t, r.hub, "hardware.hosts", anyHost)
	if d.PackageTempsC == nil || d.Sensors == nil || d.Fans == nil || d.PSUs == nil || d.Pools == nil || d.Disks == nil || d.Links == nil {
		t.Errorf("a list is null: %+v", d)
	}
	if !hasSource(srcs, "prometheus", false, "unreachable") {
		t.Errorf("sources = %+v", srcs)
	}
	if st := r.waitState(model.PluginError); st.Message == "" {
		t.Errorf("row = %+v, want an error with its category", st)
	}
}

// The host's name: hosts[0].name, else the Proxmox node, else the nodename; the nodename itself
// is hosts[0].nodename, else the Proxmox node. It is read on every poll.
func TestHostNameChain(t *testing.T) {
	t.Run("the configured name wins", func(t *testing.T) {
		r := hostRig(t, func(c *pluginconf.Hardware) { c.Hosts[0].Name, c.Hosts[0].Nodename = "Rack 1", "box-9" })
		r.q.fillHost(r.ex, "box-9", scrubPrefix)
		d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.Name == "Rack 1" })
		if d.Ref != "hardware:Host/Rack 1" || !r.q.askedContaining(`nodename="box-9"`) || r.q.askedContaining(`nodename="`+node+`"`) {
			t.Errorf("ref %q, queries %v", d.Ref, r.q.asked())
		}
	})
	t.Run("the Proxmox node names the host and its series", func(t *testing.T) {
		r := hostRig(t, nil)
		d, _ := r.hostOf()
		if d.Name != node || !r.q.askedContaining(`nodename="`+node+`"`) {
			t.Errorf("name %q, queries %v", d.Name, r.q.asked())
		}
	})
	t.Run("the configured nodename names the host when no Proxmox runs", func(t *testing.T) {
		r := newRig(t, rigOpts{noNode: true, cfg: func(c *pluginconf.Hardware) { c.Hosts = []pluginconf.HardwareHost{{Nodename: "box-9"}} }})
		r.q.fillHost(r.ex, "box-9", "")
		d, _ := r.hostOf()
		if d.Name != "box-9" || d.Ref != "hardware:Host/box-9" {
			t.Errorf("name %q ref %q", d.Name, d.Ref)
		}
	})
	t.Run("the Proxmox node names the host while the nodename is configured", func(t *testing.T) {
		r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) { c.Hosts = []pluginconf.HardwareHost{{Nodename: "box-9"}} }})
		r.q.fillHost(r.ex, "box-9", "")
		d, _ := r.hostOf()
		if d.Name != node { // the name is the node, the series carry the configured nodename
			t.Errorf("name = %q, want the Proxmox node %q", d.Name, node)
		}
	})
}

// hosts[0].bays and fanMaxRPM reach the payload as configured, 0 meaning "derive it".
func TestHostCarriesBaysAndFanScale(t *testing.T) {
	r := hostRig(t, nil)
	d, _ := r.hostOf()
	if d.Bays != 16 || d.FanMaxRPM != 18000 {
		t.Errorf("bays %d fanMaxRPM %d, want the homelab's 16 and 18000", d.Bays, d.FanMaxRPM)
	}
	r2 := hostRig(t, func(c *pluginconf.Hardware) { c.Hosts[0].Bays, c.Hosts[0].FanMaxRPM = 0, 0 })
	d2, _ := r2.hostOf()
	if d2.Bays != 0 || d2.FanMaxRPM != 0 {
		t.Errorf("unset: bays %d fanMaxRPM %d, want 0 (the page derives them)", d2.Bays, d2.FanMaxRPM)
	}
}

func TestSensorAliasesReachThePayload(t *testing.T) {
	r := hostRig(t, nil)
	d, _ := r.hostOf()
	if d.SensorAliases.Inlet != "inlet" || d.SensorAliases.Exhaust != "exhaust" {
		t.Errorf("default aliases = %+v", d.SensorAliases)
	}
	r2 := hostRig(t, func(c *pluginconf.Hardware) {
		c.Hosts[0].SensorAliases = pluginconf.HardwareSensorAliases{Inlet: "Ambient", Exhaust: "Outlet"}
	})
	d2, _ := r2.hostOf()
	if d2.SensorAliases.Inlet != "Ambient" || d2.SensorAliases.Exhaust != "Outlet" {
		t.Errorf("configured aliases = %+v", d2.SensorAliases)
	}
}

// An empty zfsScrubMetricPrefix asks no scrub query and says the scrub is not tracked; a prefix
// asks the three.
func TestEmptyScrubPrefixQueriesNoScrubData(t *testing.T) {
	r := hostRig(t, func(c *pluginconf.Hardware) { c.ZFSScrubMetricPrefix = "" })
	d, _ := r.hostOf()
	if d.ScrubTracked {
		t.Error("scrubTracked with no prefix")
	}
	if r.q.askedContaining("_last_end_timestamp_seconds") || r.q.askedContaining("_last_errors") || r.q.askedContaining("_in_progress") {
		t.Errorf("a scrub query was sent with no prefix: %v", r.q.asked())
	}
	for _, p := range d.Pools {
		if p.LastScrubEnd != nil || p.ScrubErrors != 0 {
			t.Errorf("pool %+v carries scrub data", p)
		}
	}

	r2 := hostRig(t, nil)
	d2, _ := waitTopic(t, r2.hub, "hardware.hosts", func(d model.HostData) bool { return len(d.Pools) > 0 })
	if !d2.ScrubTracked || !r2.q.askedContaining(scrubPrefix+"_last_errors") {
		t.Errorf("scrubTracked %v, queries %v", d2.ScrubTracked, r2.q.asked())
	}
}

// Every health of the plugin comes from tempThresholds, at the edges and with custom values:
// the host's, the KPI's, the topology host's and the attention item.
func TestTempThresholdsDriveEveryHealth(t *testing.T) {
	for _, c := range []struct {
		warn, crit int
		temp       float64
		want       model.Health
	}{
		{85, 90, 84.9, model.HealthOK},
		{85, 90, 85, model.HealthWarn},
		{85, 90, 89.9, model.HealthWarn},
		{85, 90, 90, model.HealthCrit},
		{70, 80, 70, model.HealthWarn},
		{70, 80, 79.9, model.HealthWarn},
		{70, 80, 80, model.HealthCrit},
		{70, 80, 69.9, model.HealthOK},
	} {
		r := hostRig(t, func(h *pluginconf.Hardware) { h.TempThresholds = pluginconf.TempThresholds{Warn: c.warn, Crit: c.crit} })
		r.q.set(r.ex.packageTemps(node), sample(c.temp, "sensor", "temp1"))
		d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return len(d.PackageTempsC) == 1 })
		if d.CPUTempHealth != c.want || d.TempWarnC != c.warn || d.TempCritC != c.crit {
			t.Errorf("%d/%d at %v: host health %s (thresholds %d/%d), want %s", c.warn, c.crit, c.temp, d.CPUTempHealth, d.TempWarnC, d.TempCritC, c.want)
		}
		con := contributor(t, r)
		var kpi *model.KPI
		for _, k := range con.KPIs.KPIs() {
			if k.Key == "cpuTemp" {
				kpi = &k
			}
		}
		if kpi == nil || kpi.Health != c.want || kpi.Value != c.temp || kpi.Unit != "°C" {
			t.Errorf("%d/%d at %v: KPI = %+v, want health %s", c.warn, c.crit, c.temp, kpi, c.want)
		}
		if got := con.Topology.Topology().Host.CPUTempHealth; got != c.want {
			t.Errorf("%d/%d at %v: topology cpuTempHealth = %s, want %s", c.warn, c.crit, c.temp, got, c.want)
		}
		items := con.Attention.Attention(time.Now())
		raised := slices.ContainsFunc(items, func(it model.AttentionItem) bool { return it.ID == "host:cpu-temp" })
		if raised != (c.want != model.HealthOK) {
			t.Errorf("%d/%d at %v: host:cpu-temp raised = %v, want %v", c.warn, c.crit, c.temp, raised, c.want != model.HealthOK)
		}
	}
}

// contributor is the plugin's entry in the overview contributors.
func contributor(t *testing.T, r *rig) plugin.Contributor {
	t.Helper()
	var out plugin.Contributor
	eventually(t, "the plugin to contribute", func() bool {
		for _, c := range r.sup.Contributors() {
			if c.ID == ID {
				out = c
				return true
			}
		}
		return false
	})
	return out
}

// applyHost replaces what a query returned and keeps what it did not.
func TestApplyHostKeepsWhatFailed(t *testing.T) {
	x := defaultExprs()
	qs := x.hostQueries("n", "")
	if len(qs) == 0 {
		t.Fatal("no queries")
	}
	good := map[string]qres{
		"cpu":   {samples: []svc.Sample{sample(0.4)}},
		"power": {samples: []svc.Sample{sample(10), sample(5)}},
		"pkg":   {samples: []svc.Sample{sample(70), sample(60)}},
	}
	v1 := applyHost(model.HostData{}, good)
	if v1.CPURatio != 0.4 || v1.PowerWatts != 15 || !slices.Equal(v1.PackageTempsC, []float64{60, 70}) {
		t.Fatalf("first apply = %+v", v1)
	}
	v2 := applyHost(v1, map[string]qres{
		"cpu":   {err: errors.New("x")},
		"power": {samples: []svc.Sample{sample(1)}},
		"pkg":   {err: errors.New("x")},
	})
	if v2.CPURatio != 0.4 || v2.PowerWatts != 1 || !slices.Equal(v2.PackageTempsC, []float64{60, 70}) {
		t.Errorf("second apply = %+v, want cpu and package kept, power replaced", v2)
	}
	if v1.PowerWatts != 15 {
		t.Error("applyHost changed its input")
	}
}

func TestPSUsNumberDuplicateNames(t *testing.T) {
	got := psus([]svc.Sample{
		sample(0, "name", "PS", "id", "1"), sample(1, "name", "PS", "id", "2"), sample(2, "name", "Other", "id", "1"),
	})
	want := []model.PSU{{Name: "Other", State: model.HealthCrit}, {Name: "PS #1", State: model.HealthOK}, {Name: "PS #2", State: model.HealthWarn}}
	if !slices.Equal(got, want) {
		t.Errorf("psus = %+v, want %+v", got, want)
	}
}

// buildHostData adds refs and layout to the polled values and leaves its input alone.
func TestBuildHostDataDoesNotChangeItsInput(t *testing.T) {
	vals := model.HostData{Pools: []model.Pool{{Name: "tank", State: "ONLINE"}}, Disks: []model.Disk{{Bay: "3", Healthy: true}}}
	cfg := pluginconf.DefaultHardware()
	cfg.Hosts = []pluginconf.HardwareHost{{Model: "M", Bays: 4}}
	cfg = cfg.Normalized()
	d := buildHostData(vals, cfg, "h1")
	if d.Pools[0].Ref != "hardware:Pool/h1/tank" || d.Disks[0].Ref != "hardware:Disk/h1/bay-3" || d.Bays != 4 {
		t.Errorf("built = %+v", d)
	}
	if vals.Pools[0].Ref != "" || vals.Disks[0].Ref != "" || vals.Name != "" {
		t.Errorf("the input changed: %+v", vals)
	}
}
