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

// The fake host's layout: a 16-bay chassis with 8 disks, fans that top out at 18000 RPM.
const (
	demoBays      = 16
	demoFanMaxRPM = 18000
)

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// noLinks is the link source of a world whose payloads here need none.
type noLinks struct{}

func (noLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }
func (noLinks) VMConsole(int, string) []model.Link           { return []model.Link{} }

// twin is the plugin's demo twin (design 1.9): the same manifest and the fake world's host and
// machines, and no network. It is enabled and configured whatever the settings say, raises the
// same attention items and contributes the same KPIs and topology as the plugin, and answers the
// host.* series for the fake host, so that every page and chart of the demo has data.
type twin struct {
	plugin.ObjectStore
	mu     sync.Mutex
	h      plugin.Host
	cfg    pluginconf.Hardware
	w      *world.World
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *twin) Manifest() plugin.Manifest {
	q := newExprs(pluginconf.DefaultHardware().Selectors)
	return manifest(seriesSpecs(func(name string) (string, error) { return hostSeries(q, name, world.HostName) }))
}

// demoConfig is the settings the twin runs with: the given ones where they exist, and the fake
// host's layout where the section names no host, so the page has a full-size chassis.
func demoConfig(cfg any) pluginconf.Hardware {
	c := pluginconf.DefaultHardware()
	if in, err := settings(cfg); err == nil {
		c = in
	}
	c = c.Normalized()
	if len(c.Hosts) == 0 {
		c.Hosts = []pluginconf.HardwareHost{{Model: world.HostModel, Bays: demoBays, FanMaxRPM: demoFanMaxRPM}}
		c = c.Normalized()
	}
	return c
}

// Start publishes the fake world's host and machines now and then every demoEvery.
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

// host is the fake host as the plugin would publish it, without links.
func (t *twin) host() (model.HostData, plugin.Host, bool) {
	t.mu.Lock()
	h, c, w := t.h, t.cfg, t.w
	t.mu.Unlock()
	if h == nil {
		return model.HostData{}, nil, false
	}
	return buildHostData(w.Host(), c, world.HostName, world.HostName), h, true
}

func (t *twin) publish() {
	d, h, ok := t.host()
	if !ok {
		return
	}
	d.Links = hostLinks(h, d.Node)
	now := h.Now().UTC()
	t.mu.Lock()
	w := t.w
	t.mu.Unlock()
	h.Publish(topicHosts, d, demoSources(now)[:1])
	h.Publish(topicMachines, hwmodel.MachinesData{Machines: w.Machines()}, demoSources(now))
}

func (t *twin) Attention(now time.Time) []model.AttentionItem {
	d, h, ok := t.host()
	if !ok {
		return nil
	}
	return attentionOf(d, shellLinks(h, d.Node))
}

func (t *twin) KPIs() []model.KPI {
	d, _, ok := t.host()
	if !ok {
		return nil
	}
	return kpisOf(d)
}

func (t *twin) Topology() model.Topology {
	d, _, ok := t.host()
	if !ok {
		return model.Topology{}
	}
	return topologyOf(d, attentionOf(d, nil))
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
	_ plugin.KPISource       = (*twin)(nil)
	_ plugin.TopologySource  = (*twin)(nil)
)
