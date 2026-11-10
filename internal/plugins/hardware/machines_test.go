package hardware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// twoMachines is one machine behind a node-exporter and one behind a Gatus check.
func twoMachines(c *pluginconf.Hardware) {
	c.Machines = []pluginconf.HardwareMachine{
		{Name: "nas", NodeExporterNodename: "nas-1"},
		{Name: "pi", GatusEndpoint: "pi SSH"},
	}
}

func machineByName(d hwmodel.MachinesData, name string) (hwmodel.Machine, bool) {
	for _, m := range d.Machines {
		if m.Name == name {
			return m, true
		}
	}
	return hwmodel.Machine{}, false
}

func anyMachines(hwmodel.MachinesData) bool { return true }

// A machine behind a node-exporter is up while its up series is 1 or more; a machine behind a
// Gatus check follows the check. Each names its source, and both sources are on the envelope.
func TestMachinesViaPrometheusAndViaGatus(t *testing.T) {
	r := newRig(t, rigOpts{cfg: twoMachines})
	r.q.set(r.ex.machineUp("nas-1"), sample(1))
	r.ep.set("pi ssh", true) // compared without regard to case

	d, srcs := waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool {
		nas, _ := machineByName(d, "nas")
		pi, _ := machineByName(d, "pi")
		return nas.Up && pi.Up
	})
	if len(d.Machines) != 2 || d.Machines[0].Name != "nas" || d.Machines[1].Name != "pi" {
		t.Errorf("machines = %+v, want the configured order", d.Machines)
	}
	if d.Machines[0].Source != "prometheus" || d.Machines[1].Source != "gatus" {
		t.Errorf("sources of the machines = %q, %q", d.Machines[0].Source, d.Machines[1].Source)
	}
	if d.Machines[0].Ref != "hardware:Machine/nas" || d.Machines[1].Ref != "hardware:Machine/pi" {
		t.Errorf("refs = %q, %q", d.Machines[0].Ref, d.Machines[1].Ref)
	}
	if !hasSource(srcs, "prometheus", true, "") || !hasSource(srcs, "gatus", true, "") {
		t.Errorf("envelope sources = %+v, want prometheus and gatus", srcs)
	}
	plugintest.AssertRefs(t, r.sup, ID, d, hwmodel.Machine{})
	// With no host configured the hosts topic is an empty list at once, so the page knows there
	// is none rather than waiting.
	hosts, _ := waitTopic(t, r.hub, "hardware.hosts", func(model.HostsData) bool { return true })
	if hosts.Hosts == nil || len(hosts.Hosts) != 0 {
		t.Errorf("hosts = %#v, want an empty list that is not null", hosts.Hosts)
	}
	if !r.q.askedContaining(`up{job="node-exporter",nodename="nas-1"}`) {
		t.Errorf("the machine's up series was not asked: %v", r.q.asked())
	}
}

// up below 1, and no sample at all, are down.
func TestMachineIsDownBelowOneAndWithNoSample(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "a", NodeExporterNodename: "a"}, {Name: "b", NodeExporterNodename: "b"}, {Name: "c", NodeExporterNodename: "c"}}
	}})
	r.q.set(r.ex.machineUp("a"), sample(0))
	r.q.set(r.ex.machineUp("c"), sample(1))
	d, _ := waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool {
		c, _ := machineByName(d, "c")
		return c.Up
	})
	a, _ := machineByName(d, "a")
	b, _ := machineByName(d, "b")
	if a.Up || b.Up {
		t.Errorf("a up=%v, b up=%v, want both down (value 0, and no sample)", a.Up, b.Up)
	}
}

// A failed query keeps the last value and the source says why; recovery clears it.
func TestFailedMachineQueryKeepsTheLastValue(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
	}})
	r.q.set(r.ex.machineUp("nas-1"), sample(1))
	waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return d.Machines[0].Up })

	r.q.setErr(r.ex.machineUp("nas-1"), errors.New("boom"))
	_, srcs := waitTopic(t, r.hub, "hardware.machines", anyMachines)
	eventually(t, "the failing source", func() bool {
		d, s, _ := stored[hwmodel.MachinesData](t, r.hub, "hardware.machines")
		srcs = s
		return hasSource(s, "prometheus", false, "unreachable") && d.Machines[0].Up
	})
	if !hasSource(srcs, "prometheus", false, "unreachable") {
		t.Errorf("sources = %+v", srcs)
	}
	r.q.set(r.ex.machineUp("nas-1"), sample(0))
	waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return !d.Machines[0].Up })
}

// With Gatus absent a Gatus machine is down and the gatus source says it is not configured; the
// machines that need no Gatus go on.
func TestGatusMachineWithoutGatusIsDownWithTheChip(t *testing.T) {
	r := newRig(t, rigOpts{cfg: twoMachines, noGat: true})
	r.q.set(r.ex.machineUp("nas-1"), sample(1))
	d, srcs := waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool {
		nas, _ := machineByName(d, "nas")
		return nas.Up
	})
	pi, _ := machineByName(d, "pi")
	if pi.Up || pi.Source != "gatus" {
		t.Errorf("pi = %+v, want down from gatus", pi)
	}
	if !hasSource(srcs, "gatus", false, "not_configured") || !hasSource(srcs, "prometheus", true, "") {
		t.Errorf("sources = %+v, want gatus not_configured and prometheus ok", srcs)
	}
	if st := r.waitState("degraded"); st.Message != "gatus: not_configured" {
		t.Errorf("row = %s %q, want degraded naming the source and its category", st.State, st.Message)
	}
}

// A check that does not exist is down, whatever the revision.
func TestUnknownGatusCheckIsDown(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "no such check"}}
	}})
	d, _ := waitTopic(t, r.hub, "hardware.machines", anyMachines)
	if d.Machines[0].Up {
		t.Errorf("a missing check is up: %+v", d.Machines[0])
	}
}

// A change in Gatus reaches the machines within a second (the watcher's tick).
func TestGatusRevisionChangeRepublishesWithinASecond(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	}})
	r.ep.set("pi SSH", true)
	waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return d.Machines[0].Up })

	start := time.Now()
	r.ep.set("pi SSH", false)
	waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return !d.Machines[0].Up })
	if took := time.Since(start); took > time.Second {
		t.Errorf("the change took %v to reach the topic", took)
	}
}

// The machines half republishes when Gatus' revision or the service itself differs from what
// the last publish read, so a service rebuilt by a reload (with a revision below its
// predecessor's) cannot hide a change; and it stays quiet while nothing differs.
func TestMachinesWatcherRepublishesOnRevisionAndOnServiceIdentity(t *testing.T) {
	p, rt, h := stubRuntime(t, func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	})
	old := newFakeEndpoints(100)
	old.set("pi SSH", true) // revision 101
	h.setService("gatus", old)
	ctx := context.Background()

	count := func() int { return len(h.published("machines")) }
	up := func() bool {
		pubs := h.published("machines")
		return pubs[len(pubs)-1].data.(hwmodel.MachinesData).Machines[0].Up
	}

	p.machinesTick(ctx, rt)
	if count() != 1 || !up() {
		t.Fatalf("first tick: %d publishes, up=%v", count(), up())
	}
	p.machinesTick(ctx, rt)
	p.machinesTick(ctx, rt)
	if count() != 1 {
		t.Errorf("a quiet watcher published %d times", count())
	}

	old.set("pi SSH", false) // revision 102
	p.machinesTick(ctx, rt)
	if count() != 2 || up() {
		t.Errorf("after a revision change: %d publishes, up=%v", count(), up())
	}

	// A successor service starts again at a lower revision with the check passing: same data as
	// a revision above would give, and a revision below the last seen. It must still publish.
	next := newFakeEndpoints(1)
	next.set("pi SSH", true) // revision 2 < 102
	h.setService("gatus", next)
	p.machinesTick(ctx, rt)
	if count() != 3 || !up() {
		t.Errorf("after the service was replaced by a lower revision: %d publishes, up=%v", count(), up())
	}
}

// The machines topic is published even with nothing in it once the host half can run: an
// unpublished topic would leave the web UI's read pending.
func TestEmptyMachinesTopicIsPublishedWhenTheHostRuns(t *testing.T) {
	r := newRig(t, rigOpts{cfg: withHost(pluginconf.HardwareHost{})})
	r.q.fillHost(r.ex, node, "")
	d, srcs := waitTopic(t, r.hub, "hardware.machines", anyMachines)
	if d.Machines == nil || len(d.Machines) != 0 {
		t.Errorf("machines = %#v, want an empty list that is not null", d.Machines)
	}
	if len(srcs) != 0 {
		t.Errorf("sources = %+v, want none: nothing is read", srcs)
	}
}

// The first publish waits for the first node-exporter poll, so a machine never flashes as down
// while its query is in flight.
func TestMachinesAreNotPublishedBeforeTheirFirstPoll(t *testing.T) {
	p, rt, h := stubRuntime(t, func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
	})
	q := newFakeQuerier()
	q.set(rt.q.machineUp("nas-1"), sample(1))
	h.setService("prometheus", q)

	p.mu.Lock()
	p.mach.last = h.Now() // a poll "started" and has not ended
	p.mach.possible = true
	p.mu.Unlock()
	p.machinesTick(context.Background(), rt)
	if n := len(h.published("machines")); n != 0 {
		t.Errorf("published %d times before the first poll ended", n)
	}
	h.advance(time.Second)
	p.machinesTick(context.Background(), rt)
	pubs := h.published("machines")
	if len(pubs) != 1 || !pubs[0].data.(hwmodel.MachinesData).Machines[0].Up {
		t.Errorf("after the poll: %+v", pubs)
	}
}
