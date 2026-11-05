package cnpg

import (
	"context"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// demoEvery is how often the twin's backup age moves on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// twinLinks is the world's link source: the links the running link providers give an object.
type twinLinks struct{ h plugin.Host }

func (l twinLinks) Headlamp(kind, namespace, name string) []model.Link {
	return linksOf(l.h.Links(), namespace, name)
}

// twin is the plugin's demo twin (design 1.9): the same manifest and the cluster of the fake
// world, decorated with its ref as the plugin does, and no network. It is enabled and configured
// whatever the settings say.
type twin struct {
	plugin.ObjectStore
	mu     sync.Mutex
	h      plugin.Host
	w      *world.World
	cancel context.CancelFunc
	done   chan struct{}
}

func (t *twin) Manifest() plugin.Manifest { return manifest() }

// Start publishes the fake world's clusters now and then every demoEvery.
func (t *twin) Start(_ context.Context, h plugin.Host, _ any) error {
	h = t.Wrap(h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.mu.Lock()
	t.h, t.w, t.cancel, t.done = h, world.New(h.Now, h.Now(), twinLinks{h}), cancel, done
	t.mu.Unlock()

	t.publish()
	h.Go("cnpg demo", func() {
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

func (t *twin) publish() {
	t.mu.Lock()
	h, w := t.h, t.w
	t.mu.Unlock()
	d := w.CNPG()
	for i, c := range d.Clusters {
		d.Clusters[i].Ref = plugin.Ref(ID, "Cluster", c.Namespace, c.Name)
	}
	h.Publish(topicClusters, d, demoSources(h.Now()))
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
		{Name: sourceKubernetes, OK: true, CheckedAt: now.UTC()},
		{Name: sourcePrometheus, OK: true, CheckedAt: now.UTC()},
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

var _ plugin.Plugin = (*twin)(nil)
