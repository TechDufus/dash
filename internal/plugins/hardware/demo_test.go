package hardware

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// rawTopic is the stored envelope of a topic as JSON text.
func rawTopic(t *testing.T, h *hub.Hub, topic string) string {
	t.Helper()
	ev, ok := h.Latest("", model.Topic(topic))
	if !ok {
		t.Fatalf("%s has no snapshot", topic)
	}
	return string(ev.Data)
}

// In demo mode the twin runs in place of the plugin, enabled and configured with nothing set,
// publishes both topics from the fake world and contacts no upstream: not the querier, not the
// Proxmox node service, not Gatus.
func TestDemoRunsTheTwinAndContactsNoUpstream(t *testing.T) {
	r := newRig(t, rigOpts{demo: true})
	host, srcs := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.Ref != "" })
	machines, msrcs := waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return len(d.Machines) > 0 })

	if host.Name != world.HostName || host.Model != world.HostModel {
		t.Errorf("host name %q model %q, want the fake host's", host.Name, host.Model)
	}
	if host.Bays != demoBays || host.FanMaxRPM != demoFanMaxRPM || host.SensorAliases.Inlet != "inlet" || host.TempWarnC != 85 || host.TempCritC != 90 {
		t.Errorf("layout = bays %d fan %d aliases %+v thresholds %d/%d", host.Bays, host.FanMaxRPM, host.SensorAliases, host.TempWarnC, host.TempCritC)
	}
	if len(host.Pools) == 0 || len(host.Disks) == 0 || len(host.Sensors) == 0 || len(host.Fans) == 0 {
		t.Errorf("the demo host is empty: %+v", host)
	}
	if host.CPUTempHealth != model.HealthOK {
		t.Errorf("cpu health = %s", host.CPUTempHealth)
	}
	if len(machines.Machines) != len(world.New(time.Now, time.Now(), noLinks{}).Machines()) {
		t.Errorf("machines = %+v", machines.Machines)
	}
	if !hasSource(srcs, "prometheus", true, "") || !hasSource(msrcs, "gatus", true, "") {
		t.Errorf("sources: hosts %+v machines %+v", srcs, msrcs)
	}

	// Every ref of both payloads is filled, and the twin's plugin is the publisher.
	plugintest.AssertRefs(t, r.sup, ID, host, model.HostData{}, model.Pool{}, model.Disk{})
	plugintest.AssertRefs(t, r.sup, ID, machines, hwmodel.Machine{})
	for _, topic := range []string{"hardware.hosts", "hardware.machines"} {
		if body := rawTopic(t, r.hub, topic); strings.Contains(body, `"ref":""`) {
			t.Errorf("%s carries an empty ref: %s", topic, body)
		}
	}

	if st := r.waitState(model.PluginHealthy); st.Message != "" || len(st.Actions) != 0 {
		t.Errorf("row = %+v", st)
	}
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("the twin sent %d queries to the querier", n)
	}
}

// The twin contributes what the plugin does: KPIs, a topology host and (for a healthy world) no
// attention, and it answers the host series for the fake host.
func TestDemoTwinContributesAndAnswersSeries(t *testing.T) {
	r := newRig(t, rigOpts{demo: true})
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.Ref != "" })
	c := contributor(t, r)

	kpis := map[string]model.KPI{}
	for _, k := range c.KPIs.KPIs() {
		kpis[k.Key] = k
	}
	if kpis["power"].Value <= 0 || kpis["power"].Unit != "W" || kpis["cpuTemp"].Value <= 0 || kpis["cpuTemp"].Unit != "°C" {
		t.Errorf("kpis = %+v", kpis)
	}
	if h := hostOf(c.Topology.Topology()); h.Name != world.HostName || h.PowerWatts <= 0 || h.Health != model.HealthOK {
		t.Errorf("topology host = %+v", h)
	}
	if items := c.Attention.Attention(time.Now()); len(items) != 0 {
		t.Errorf("a healthy demo world raised %+v", items)
	}
	spec, id, ok := r.sup.Series("host.cpu")
	if !ok || id != ID {
		t.Fatalf("host.cpu: %v %v", id, ok)
	}
	if expr, err := spec.Expr(plugin.SeriesArgs{}); err != nil || !strings.Contains(expr, `nodename="`+world.HostName+`"`) {
		t.Errorf("host.cpu = %q (%v)", expr, err)
	}
}

// The twin takes the layout of a configured host, so a demo can show another chassis.
func TestDemoConfigKeepsAConfiguredHost(t *testing.T) {
	c := pluginconf.DefaultHardware()
	c.Hosts = []pluginconf.HardwareHost{{Model: "Test Model", Bays: 4}}
	got := demoConfig(&c)
	if got.Hosts[0].Model != "Test Model" || got.Hosts[0].Bays != 4 {
		t.Errorf("host = %+v", got.Hosts[0])
	}
	if empty := demoConfig(nil); empty.Hosts[0].Bays != demoBays || empty.Hosts[0].Model != world.HostModel {
		t.Errorf("no section: %+v", empty.Hosts)
	}
}

// The twin's goroutine ends with Stop.
func TestDemoTwinStopEndsItsTicker(t *testing.T) {
	tw := New().(*Plugin).Demo()
	h := newStubHost()
	if err := tw.Start(context.Background(), h, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.published("hosts")) != 1 || len(h.published("machines")) != 1 {
		t.Errorf("the twin published %d hosts and %d machines at Start", len(h.published("hosts")), len(h.published("machines")))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tw.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := (&twin{}).Stop(ctx); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
	if st := (&twin{}).Status(); st.State != plugin.StateStarting {
		t.Errorf("status before Start = %+v", st)
	}
}

// The twin's manifest is the plugin's.
func TestDemoManifestMatchesThePlugin(t *testing.T) {
	a, b := New().Manifest(), New().(*Plugin).Demo().Manifest()
	if a.ID != b.ID || len(a.Topics) != len(b.Topics) || len(a.Series) != len(b.Series) || len(a.UI.Nav) != len(b.UI.Nav) {
		t.Errorf("manifests differ: %+v vs %+v", a, b)
	}
	for i := range a.Topics {
		if a.Topics[i].Name != b.Topics[i].Name {
			t.Errorf("topic %d: %s vs %s", i, a.Topics[i].Name, b.Topics[i].Name)
		}
	}
}
