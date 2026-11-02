package hardware

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// The manifest: id, the two topics in wire order, the six series, the nav entry copied from the
// shim, and soft dependencies only (a Gatus-only list of machines must not block on Prometheus).
func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "hardware" || m.Name != "Hardware" || m.Version == "" || m.Description == "" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Requires) != 0 || !slices.Equal(m.Uses, []plugin.ID{"prometheus", "proxmox", "gatus"}) {
		t.Errorf("requires %v uses %v: every dependency is soft", m.Requires, m.Uses)
	}
	if len(m.Topics) != 2 || m.Topics[0].Name != "hosts" || m.Topics[1].Name != "machines" {
		t.Errorf("topics = %+v", m.Topics)
	}
	if _, ok := m.Topics[0].Type.(model.HostData); !ok {
		t.Errorf("hosts payload type %T", m.Topics[0].Type)
	}
	if _, ok := m.Topics[1].Type.(hwmodel.MachinesData); !ok {
		t.Errorf("machines payload type %T", m.Topics[1].Type)
	}
	if len(m.Series) != 6 || len(m.Actions) != 0 {
		t.Errorf("series %d actions %d", len(m.Series), len(m.Actions))
	}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != (model.NavItem{Path: "/hardware", Label: "Hardware", Title: "Hardware", Icon: "Cpu"}) {
		t.Errorf("nav = %+v", m.UI.Nav)
	}
	if _, ok := m.Config().(*pluginconf.Hardware); !ok {
		t.Errorf("config type %T", m.Config())
	}
}

// Configured is "hosts or machines": hosts: [{}] is a host and keeps the old implicit behaviour.
func TestConfiguredNeedsAHostOrAMachine(t *testing.T) {
	p := New().(*Plugin)
	d := pluginconf.DefaultHardware()
	if p.Configured(&d) {
		t.Error("nothing set: configured")
	}
	h := pluginconf.DefaultHardware()
	h.Hosts = []pluginconf.HardwareHost{{}}
	m := pluginconf.DefaultHardware()
	m.Machines = []pluginconf.HardwareMachine{{Name: "a", GatusEndpoint: "a"}}
	if !p.Configured(&h) || !p.Configured(&m) || !p.Configured(h) {
		t.Error("a host or a machine is not configured")
	}
	if p.Configured("not a section") || p.Configured((*pluginconf.Hardware)(nil)) {
		t.Error("a foreign value is configured")
	}
}

// The hint names the setting to make and starts with plugins.hardware.
func TestConfigHintNamesTheSetting(t *testing.T) {
	hint := New().(plugin.ConfigHinter).ConfigHint()
	if !strings.HasPrefix(hint, "plugins.hardware.") || hint != "plugins.hardware.hosts or machines" {
		t.Errorf("hint = %q", hint)
	}
}

// The plugin applies the section's own rules, so a configuration that reaches it another way
// than the loader is refused for the same reasons.
func TestValidateAppliesTheSectionRules(t *testing.T) {
	p := New().(*Plugin)
	ok := pluginconf.DefaultHardware()
	ok.Hosts = []pluginconf.HardwareHost{{Bays: 16}}
	if err := p.Validate(&ok); err != nil {
		t.Errorf("valid: %v", err)
	}
	bad := ok
	bad.Hosts = []pluginconf.HardwareHost{{}, {}}
	if p.Validate(&bad) == nil {
		t.Error("two hosts passed")
	}
	bad = pluginconf.DefaultHardware()
	bad.Machines = []pluginconf.HardwareMachine{{Name: "x"}}
	if p.Validate(&bad) == nil {
		t.Error("a machine with no source passed")
	}
	if p.Validate(42) == nil {
		t.Error("a foreign value passed")
	}
}

func TestStartRefusesAForeignConfiguration(t *testing.T) {
	if err := New().Start(context.Background(), newStubHost(), "nope"); err == nil {
		t.Error("Start accepted a string as its configuration")
	}
}

// Nothing configured is disabled; switched on with nothing configured it is unconfigured and
// says what to set.
func TestUnsetIsDisabledAndEnabledWithoutSettingsIsUnconfigured(t *testing.T) {
	r := newRig(t, rigOpts{})
	if got := r.row().State; got != model.PluginDisabled {
		t.Errorf("state = %s, want disabled", got)
	}
	on := true
	r.cfg = pluginconf.DefaultHardware()
	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Enabled: &on, Config: &r.cfg})
	r.sup.Reload()
	st := r.waitState(model.PluginUnconfigured)
	if st.Message != "not configured: set plugins.hardware.hosts or machines" {
		t.Errorf("message = %q", st.Message)
	}
}

// Until the host's nodename is known nothing can run: the row is unavailable (hidden) saying
// what it waits for, and no topic is published; the moment the node is known, it runs.
func TestHardwareUnavailableUntilTheNodeIsKnown(t *testing.T) {
	r := newRig(t, rigOpts{cfg: withHost(pluginconf.HardwareHost{}), nodeUnknown: true})
	r.q.fillHost(r.ex, node, "")

	st := r.waitState(model.PluginUnavailable)
	if st.Message != "waiting for the Proxmox node name" {
		t.Errorf("message = %q", st.Message)
	}
	time.Sleep(60 * time.Millisecond) // several polls' worth
	for _, topic := range []string{"hardware.hosts", "hardware.machines"} {
		if _, ok := r.hub.Latest("", model.Topic(topic)); ok {
			t.Errorf("%s was published while the row is unavailable", topic)
		}
	}
	if r.q.n.Load() != 0 {
		t.Errorf("%d queries were sent without a nodename: an empty nodename matches nothing", r.q.n.Load())
	}

	r.node.set(node, true)
	d, _ := waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts > 0 })
	if d.Name != node {
		t.Errorf("host name = %q", d.Name)
	}
	if st := r.waitState(model.PluginHealthy); !plugin.Visible(st.State) {
		t.Errorf("row = %+v, want visible", st)
	}
}

// Without the prometheus plugin the host half cannot run: unavailable, naming the setting; when
// prometheus starts later (a reload) the plugin goes on without a restart and publishes.
func TestHardwareWaitsForPrometheus(t *testing.T) {
	r := newRig(t, rigOpts{cfg: homelabShaped, noProm: true})
	r.q.fillHost(r.ex, node, scrubPrefix)
	st := r.waitState(model.PluginUnavailable)
	if st.Message != "not configured: set plugins.prometheus.url" {
		t.Errorf("message = %q", st.Message)
	}
	if _, ok := r.hub.Latest("", "hardware.hosts"); ok {
		t.Error("hosts published without a querier")
	}
	r.switchOn("prometheus")
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts == 214 })
	r.waitState(model.PluginHealthy)
}

// A list of Gatus machines needs no prometheus: the plugin runs and the host half stays out.
func TestGatusOnlyMachinesRunWithoutPrometheus(t *testing.T) {
	r := newRig(t, rigOpts{noProm: true, cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	}})
	r.ep.set("pi SSH", true)
	d, _ := waitTopic(t, r.hub, "hardware.machines", func(d hwmodel.MachinesData) bool { return len(d.Machines) == 1 && d.Machines[0].Up })
	if d.Machines[0].Source != "gatus" {
		t.Errorf("machine = %+v", d.Machines[0])
	}
	if st := r.waitState(model.PluginHealthy); st.Message != "" {
		t.Errorf("row = %+v: a Gatus-only plugin has no failing source", st)
	}
}

// A machine list that needs a source nothing provides says which setting to make.
func TestMachinesNeedingAMissingSourceSayWhatToSet(t *testing.T) {
	r := newRig(t, rigOpts{noGat: true, cfg: func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	}})
	if st := r.waitState(model.PluginUnavailable); st.Message != "not configured: set plugins.gatus.url" {
		t.Errorf("message = %q", st.Message)
	}
}

// A dependency that stops stops and restarts the plugin, so a row never returns to unavailable
// while it holds data: the restarted plugin has nothing published, the hub holds nothing for it.
func TestHardwareNeverHoldsDataWhileUnavailable(t *testing.T) {
	r := newRig(t, rigOpts{cfg: homelabShaped})
	r.q.fillHost(r.ex, node, scrubPrefix)
	waitTopic(t, r.hub, "hardware.hosts", func(d model.HostData) bool { return d.PowerWatts == 214 })

	r.switchOff("prometheus")
	eventually(t, "the plugin to have no data while unavailable", func() bool {
		if r.row().State != model.PluginUnavailable {
			return false
		}
		_, hosts := r.hub.Latest("", "hardware.hosts")
		_, machines := r.hub.Latest("", "hardware.machines")
		return !hosts && !machines
	})
	// And the invariant holds on every sample while the plugin is unavailable.
	for range 20 {
		if r.row().State == model.PluginUnavailable {
			if _, ok := r.hub.Latest("", "hardware.hosts"); ok {
				t.Fatal("hardware.hosts is held while the row is unavailable")
			}
		}
		time.Sleep(3 * time.Millisecond)
	}
}

// The latch: once the plugin has published, a service that disappears without a restart (a
// supervisor would restart the plugin, but the plugin must not rely on it) leaves the row
// visible, with the source saying why.
func TestStatusStaysVisibleOnceDataIsPublished(t *testing.T) {
	p, rt, h := stubRuntime(t, withHost(pluginconf.HardwareHost{Nodename: node}))
	q := newFakeQuerier()
	q.fillHost(rt.q, node, "")
	h.setService("prometheus", q)
	if st := p.Status(); st.State != plugin.StateStarting {
		t.Errorf("before the first poll: %+v", st)
	}
	p.hostTick(context.Background(), rt)
	if st := p.Status(); st.State != plugin.StateHealthy {
		t.Errorf("after it: %+v", st)
	}
	h.setService("prometheus", nil)
	st := p.Status()
	if st.State == plugin.StateUnavailable || !hasSource(st.Sources, "prometheus", false, "not_configured") {
		t.Errorf("with the querier gone: %+v, want a visible state with prometheus not_configured", st)
	}
}

// A hidden row is not asked for data it cannot get: with nothing available the plugin is
// unavailable before Start has even been seen by a poll.
func TestStatusBeforeStartIsStarting(t *testing.T) {
	if st := newPlugin(fastTiming()).Status(); st.State != plugin.StateStarting {
		t.Errorf("status = %+v", st)
	}
}

// Machines on Gatus alone have nothing to say until Gatus has polled once, so the row is
// starting (not healthy with no sources) while its revision is 0, and the first poll ends it,
// a failed one included. A node-exporter machine is not held back by a Gatus that has not polled.
func TestGatusMachinesStartUntilGatusHasPolled(t *testing.T) {
	p, _, h := stubRuntime(t, func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	})
	ep := newFakeEndpoints(0)
	h.setService("gatus", ep)
	if st := p.Status(); st.State != plugin.StateStarting || st.Message != "waiting for the first poll" || len(st.Sources) != 0 {
		t.Fatalf("before Gatus has polled: %+v, want starting", st)
	}
	ep.set("pi SSH", true) // revision 1
	if st := p.Status(); st.State != plugin.StateHealthy || !hasSource(st.Sources, "gatus", true, "") {
		t.Errorf("after the first poll: %+v, want healthy with the gatus source", st)
	}

	failed := newFakeEndpoints(0)
	h.setService("gatus", failed)
	failed.mu.Lock()
	failed.srcs = []model.SourceStatus{{Name: "gatus", OK: false, Error: "timeout"}}
	failed.rev++
	failed.mu.Unlock()
	if st := p.Status(); st.State != plugin.StateError || st.Message != "gatus: timeout" {
		t.Errorf("after a failed first poll: %+v, want error", st)
	}

	// Node-exporter machines only, with a Gatus service that has not polled.
	p2, rt2, h2 := stubRuntime(t, func(c *pluginconf.Hardware) {
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
	})
	q := newFakeQuerier()
	q.set(rt2.q.machineUp("nas-1"), sample(1))
	h2.setService("prometheus", q)
	h2.setService("gatus", newFakeEndpoints(0))
	p2.machinesTick(context.Background(), rt2)
	if st := p2.Status(); st.State != plugin.StateHealthy {
		t.Errorf("node-exporter machines with an idle Gatus: %+v, want healthy", st)
	}
}

// degraded: some sources fail; error: all of them. The message holds categories only.
func TestStatusStatesFollowTheSources(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Hardware) {
		homelabShaped(c)
		c.Machines = []pluginconf.HardwareMachine{{Name: "pi", GatusEndpoint: "pi SSH"}}
	}})
	r.q.fillHost(r.ex, node, scrubPrefix)
	r.ep.set("pi SSH", true)
	r.waitState(model.PluginHealthy)

	r.ep.mu.Lock()
	r.ep.srcs = []model.SourceStatus{{Name: "gatus", OK: false, Error: "timeout"}}
	r.ep.rev++
	r.ep.mu.Unlock()
	st := r.waitState(model.PluginDegraded)
	if st.Message != "gatus: timeout" {
		t.Errorf("message = %q", st.Message)
	}

	r.q.setFail(fmt.Errorf("Get %q: connection refused", "https://prom.internal:9090/api/v1/query"))
	st = r.waitState(model.PluginError)
	if strings.Contains(st.Message, "prom.internal") || !strings.Contains(st.Message, "prometheus: unreachable") {
		t.Errorf("message = %q, want categories only", st.Message)
	}
}

// The canary: an upstream error carrying a secret and a URL never reaches a viewer (the topics,
// the row) and reaches the log only through ScrubLog.
func TestFailingPrometheusLeaksNothing(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, canary, "[scrubbed]") }
	r := newRig(t, rigOpts{
		cfg: func(c *pluginconf.Hardware) {
			homelabShaped(c)
			c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
		},
		scrub: scrub,
		qfail: fmt.Errorf("Get \"https://prom.internal:9090/api/v1/query?token=%s\": dial tcp 192.0.2.7:9090: connection refused", canary),
	})
	waitTopic(t, r.hub, "hardware.hosts", anyHost)
	waitTopic(t, r.hub, "hardware.machines", anyMachines)
	r.waitState(model.PluginError)
	eventually(t, "the failure to be logged", func() bool { return strings.Contains(r.logs.String(), "upstream source failed") })

	var bodies []string
	for _, topic := range []string{"hardware.hosts", "hardware.machines", string(model.TopicPlugins)} {
		if ev, ok := r.hub.Latest("", model.Topic(topic)); ok {
			bodies = append(bodies, string(ev.Data))
		}
	}
	for _, pi := range r.sup.Infos() {
		bodies = append(bodies, pi.Message)
	}
	for _, b := range bodies {
		for _, secret := range []string{canary, "prom.internal", "192.0.2.7", "token="} {
			if strings.Contains(b, secret) {
				t.Errorf("%q reached a viewer: %s", secret, b)
			}
		}
	}
	logs := r.logs.String()
	if strings.Contains(logs, canary) {
		t.Errorf("the secret reached the log unscrubbed:\n%s", logs)
	}
	if !strings.Contains(logs, "[scrubbed]") || !strings.Contains(logs, "category=unreachable") {
		t.Errorf("the log lacks the scrubbed detail and the category:\n%s", logs)
	}
}

// withServices lets the single-plugin harness see the services a real supervisor would give.
type withServices struct {
	*Plugin
	svcs map[plugin.ID]any
}

type serviceHost struct {
	plugin.Host
	svcs map[plugin.ID]any
}

func (h serviceHost) Service(id plugin.ID) (any, bool) { v, ok := h.svcs[id]; return v, ok }

func (w withServices) Start(ctx context.Context, h plugin.Host, cfg any) error {
	return w.Plugin.Start(ctx, serviceHost{h, w.svcs}, cfg)
}

// Visibility: rule (a). Both topics, hosts and machines, reach an open stream after the row that
// shows them, whichever came first.
func TestVisibilityRuleAForBothTopics(t *testing.T) {
	q := newFakeQuerier()
	x := defaultExprs()
	q.fillHost(x, node, "")
	q.set(x.machineUp("nas-1"), sample(1))
	ep := newFakeEndpoints(1)
	ep.set("pi SSH", true)
	svcs := map[plugin.ID]any{"prometheus": q, "proxmox": &fakeNode{name: node, ok: true}, "gatus": ep}
	factory := func() plugin.Plugin { return withServices{newPlugin(fastTiming()), svcs} }

	before := pluginconf.DefaultHardware() // nothing set: disabled
	after := pluginconf.DefaultHardware()
	after.Hosts = []pluginconf.HardwareHost{{Model: "Dell R730"}}
	after.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}, {Name: "pi", GatusEndpoint: "pi SSH"}}
	plugintest.AssertTopicsReachOpenStream(t, factory, plugin.InstanceConfig{Config: &before}, plugin.InstanceConfig{Config: &after})
}

// TestHardwarePublishesAroundTheFlip: a running row that is unavailable and then visible, flipped
// in the two ways a running plugin can be. By prometheus starting (a reload): the supervisor
// shows the row at once and the plugin's first publish follows it. By the Proxmox node being
// found, read by a slow status loop: the plugin publishes first and the row follows. On every run
// each topic reaches the open stream after the event that shows the row, and no topic arrives
// more than twice after it (the publish and at most one resend).
func TestHardwarePublishesAroundTheFlip(t *testing.T) {
	publishedFirst := 0
	defer func() {
		t.Logf("the first publish preceded the visible row in %d of 12 runs", publishedFirst)
		if publishedFirst < 3 || publishedFirst > 9 {
			t.Errorf("the runs did not exercise both orders: %d of 12 published first", publishedFirst)
		}
	}()
	for i := range 12 {
		byNode := i%2 == 1
		o := rigOpts{cfg: homelabShaped}
		if byNode {
			o.nodeUnknown, o.statusEvery = true, 150*time.Millisecond
		} else {
			o.noProm = true
			o.cfg = func(c *pluginconf.Hardware) {
				homelabShaped(c)
				c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}}
			}
		}
		r := newRig(t, o)
		r.q.fillHost(r.ex, node, scrubPrefix)
		r.q.set(r.ex.machineUp("nas-1"), sample(1))
		r.waitState(model.PluginUnavailable)

		_, ch, cancel := r.hub.Subscribe("", nil)
		time.Sleep(time.Duration(i) * time.Millisecond) // a little jitter against the status tick
		if byNode {
			r.node.set(node, true)
		} else {
			r.switchOn("prometheus")
		}

		visible := false
		after := map[string]int{}
		deadline := time.After(3 * time.Second)
		settle := time.NewTimer(time.Hour)
	loop:
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					t.Fatalf("run %d: the subscriber was dropped", i)
				}
				switch {
				case ev.Topic == string(model.TopicPlugins):
					if !visible && rowShows(t, ev, ID) {
						visible = true
						if _, stored := r.hub.Latest("", "hardware.hosts"); stored {
							publishedFirst++
						}
						settle.Reset(400 * time.Millisecond)
					}
				case visible:
					after[ev.Topic]++
				}
			case <-settle.C:
				break loop
			case <-deadline:
				cancel()
				t.Fatalf("run %d: the row never turned visible", i)
			}
		}
		cancel()
		for _, topic := range []string{"hardware.hosts", "hardware.machines"} {
			if _, stored := r.hub.Latest("", model.Topic(topic)); !stored {
				t.Errorf("run %d: %s was never published", i, topic)
			}
			if n := after[topic]; n < 1 || n > 2 {
				t.Errorf("run %d: %s arrived %d times after the row turned visible, want 1 or 2", i, topic, n)
			}
		}
	}
}

func rowShows(t *testing.T, ev hub.Event, id plugin.ID) bool {
	t.Helper()
	var env model.Envelope
	var data model.PluginsData
	if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &data) != nil {
		t.Fatalf("core.plugins event: %v", err)
	}
	for _, pi := range data.Plugins {
		if pi.ID == string(id) {
			return plugin.Visible(pi.State)
		}
	}
	return false
}

// Stop ends every goroutine the plugin started, even with a query in flight: nothing is asked
// afterwards and Stop returns inside its deadline.
func TestStopEndsEveryGoroutine(t *testing.T) {
	p, rt, h := stubRuntime(t, func(c *pluginconf.Hardware) {
		homelabShaped(c)
		c.Hosts[0].Nodename = node
		c.Machines = []pluginconf.HardwareMachine{{Name: "nas", NodeExporterNodename: "nas-1"}, {Name: "pi", GatusEndpoint: "pi SSH"}}
	})
	q := newFakeQuerier()
	q.fillHost(rt.q, node, scrubPrefix)
	q.set(rt.q.machineUp("nas-1"), sample(1))
	ep := newFakeEndpoints(1)
	ep.set("pi SSH", true)
	h.setService("prometheus", q)
	h.setService("gatus", ep)

	if err := p.Start(context.Background(), h, &rt.cfg); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both topics", func() bool { return len(h.published("hosts")) > 0 && len(h.published("machines")) > 0 })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Stop took %v", took)
	}
	n, pubs := q.n.Load(), len(h.published("hosts"))+len(h.published("machines"))
	time.Sleep(100 * time.Millisecond) // many ticks of every loop
	if q.n.Load() != n || len(h.published("hosts"))+len(h.published("machines")) != pubs {
		t.Errorf("the plugin kept working after Stop: %d -> %d queries", n, q.n.Load())
	}
}

// A query still running when the plugin stops is cancelled by Stop rather than waited out.
func TestStopCancelsAQueryInFlight(t *testing.T) {
	p, rt, h := stubRuntime(t, withHost(pluginconf.HardwareHost{Nodename: node}))
	h.setService("prometheus", blockingQuerier{})
	if err := p.Start(context.Background(), h, &rt.cfg); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // the first poll is now blocked
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Stop waited %v for the query", took)
	}
	if n := len(h.published("hosts")); n != 0 {
		t.Errorf("a cancelled poll published %d times", n)
	}
}

// Stop before Start is a no-op.
func TestStopBeforeStart(t *testing.T) {
	if err := newPlugin(fastTiming()).Stop(context.Background()); err != nil {
		t.Error(err)
	}
}
