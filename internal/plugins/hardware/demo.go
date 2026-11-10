package hardware

import (
	"context"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
)

// demoEvery is how often the twin's values move on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// The fake hosts' layouts: a 16-bay chassis with 8 disks and fans that top out at 18000 RPM, and
// a 4-bay box with 2 disks and fans that top out at 12000 RPM.
const (
	demoBays       = 16
	demoFanMaxRPM  = 18000
	demoBays2      = 4
	demoFanMaxRPM2 = 12000
)

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// noLinks is the link source of a world whose payloads here need none.
type noLinks struct{}

func (noLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }
func (noLinks) VMConsole(int, string) []model.Link           { return []model.Link{} }

// twin is the plugin's demo twin (design 1.9): the same manifest and the fake world's hosts and
// machines, and no network. It is enabled and configured whatever the settings say, raises the
// same attention items and contributes the same KPIs and topology as the plugin, and answers the
// host.* series for the fake hosts, so that every page and chart of the demo has data.
type twin struct {
	plugin.ObjectStore
	mu     sync.Mutex
	h      plugin.Host
	cfg    pluginconf.Hardware
	w      *world.World
	cancel context.CancelFunc
	done   chan struct{}
}

// demoHostIDs are the fake hosts as the plugin would resolve them: each is its Proxmox node, and
// its nodename and name are the node's name.
func demoHostIDs() []hostID {
	names := []string{world.HostName, world.SecondNodeName}
	out := make([]hostID, len(names))
	for i, n := range names {
		out[i] = hostID{i: i, nodename: n, node: n, name: n, dupOf: -1}
	}
	return out
}

func (t *twin) Manifest() plugin.Manifest {
	q := newExprs(pluginconf.DefaultHardware().Selectors)
	ids := demoHostIDs()
	return manifest(seriesSpecs(func(name string, a plugin.SeriesArgs) (string, error) {
		return seriesOf(q, ids, len(ids), name, a)
	}))
}

// demoConfig is the settings the twin runs with: the given ones where they exist, and the fake
// hosts' layouts for the hosts the section does not name, so each page has a full-size chassis.
// The twin always shows the world's two hosts; a configured entry only lends its layout.
func demoConfig(cfg any) pluginconf.Hardware {
	c := pluginconf.DefaultHardware()
	if in, err := settings(cfg); err == nil {
		c = in
	}
	c = c.Normalized()
	layouts := []pluginconf.HardwareHost{
		{Model: world.HostModel, Bays: demoBays, FanMaxRPM: demoFanMaxRPM},
		{Model: world.SecondHostModel, Bays: demoBays2, FanMaxRPM: demoFanMaxRPM2},
	}
	if len(c.Hosts) < len(layouts) {
		c.Hosts = append(c.Hosts, layouts[len(c.Hosts):]...)
		c = c.Normalized()
	}
	return c
}

// Start publishes the fake world's hosts and machines now and then every demoEvery.
func (t *twin) Start(_ context.Context, h plugin.Host, cfg any) error {
	h = t.Wrap(h)
	c := demoConfig(cfg)
	w := world.New(h.Now, h.Now(), noLinks{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.mu.Lock()
	t.h, t.cfg, t.w, t.cancel, t.done = h, c, w, cancel, done
	t.mu.Unlock()

	t.publish()
	h.Go("hardware demo", func() {
		defer close(done)
		tick := time.NewTicker(demoEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				t.publish()
			}
		}
	})
	return nil
}

func (t *twin) Stop(ctx context.Context) error {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// demoSources are the twin's sources: both healthy.
func demoSources(now time.Time) []model.SourceStatus {
	return []model.SourceStatus{
		{Name: sourcePrometheus, OK: true, CheckedAt: now},
		{Name: sourceGatus, OK: true, CheckedAt: now},
	}
}

func (t *twin) Status() plugin.Status {
	t.mu.Lock()
	h := t.h
	t.mu.Unlock()
	if h == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: demoSources(time.Time{})}
}

// hosts are the fake hosts as the plugin would publish them, without links.
func (t *twin) hosts() ([]model.HostData, plugin.Host) {
	t.mu.Lock()
	h, c, w := t.h, t.cfg, t.w
	t.mu.Unlock()
	if h == nil {
		return nil, nil
	}
	vals := w.Hosts()
	out := make([]model.HostData, len(vals))
	for i, id := range demoHostIDs() {
		out[i] = buildHostData(vals[i], c, c.Hosts[i], id.name, id.node)
	}
	return out, h
}

func (t *twin) publish() {
	hosts, h := t.hosts()
	if h == nil {
		return
	}
	for i := range hosts {
		hosts[i].Links = hostLinks(h, hosts[i].Node)
	}
	now := h.Now().UTC()
	t.mu.Lock()
	w := t.w
	t.mu.Unlock()
	h.Publish(topicHosts, model.HostsData{Hosts: hosts}, demoSources(now)[:1])
	h.Publish(topicMachines, hwmodel.MachinesData{Machines: w.Machines()}, demoSources(now))
}

func (t *twin) Attention(now time.Time) []model.AttentionItem {
	hosts, h := t.hosts()
	if h == nil {
		return nil
	}
	items := []model.AttentionItem{}
	for _, d := range hosts {
		items = append(items, attentionOf(d, shellLinks(h, d.Node))...)
	}
	return items
}

func (t *twin) KPIs() []model.KPI {
	hosts, _ := t.hosts()
	return kpisOf(hosts)
}

func (t *twin) Topology() model.Topology {
	hosts, _ := t.hosts()
	if len(hosts) == 0 {
		return model.Topology{}
	}
	return topologyOf(hosts)
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
	_ plugin.KPISource       = (*twin)(nil)
	_ plugin.TopologySource  = (*twin)(nil)
)
