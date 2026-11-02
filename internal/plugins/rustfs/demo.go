package rustfs

import (
	"context"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// noLinks is the link source of a world whose payloads here need none.
type noLinks struct{}

func (noLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }

// twin is the plugin's demo twin (design 1.9): the same manifest and the buckets of the fake
// world, and no network. It is enabled and configured whatever the settings say, and raises the
// same attention items as the plugin does for the same buckets.
type twin struct {
	mu   sync.Mutex
	data model.RustFSUsageData
	done bool // Start has published
}

func (t *twin) Manifest() plugin.Manifest { return manifest() }

// Start publishes the fake world's buckets. They do not move, so there is nothing to tick.
func (t *twin) Start(_ context.Context, h plugin.Host, _ any) error {
	d := world.New(h.Now, h.Now(), noLinks{}).RustFS()
	for i, b := range d.Buckets {
		d.Buckets[i].Ref = plugin.Ref(ID, "Bucket", b.Name)
	}
	t.mu.Lock()
	t.data, t.done = d, true
	t.mu.Unlock()
	h.Publish(topicUsage, d, demoSources(h.Now()))
	return nil
}

func (t *twin) Stop(context.Context) error { return nil }

// demoSources is the twin's one source, healthy.
func demoSources(now time.Time) []model.SourceStatus {
	return []model.SourceStatus{{Name: sourcePrometheus, OK: true, CheckedAt: now.UTC()}}
}

func (t *twin) Status() plugin.Status {
	t.mu.Lock()
	done := t.done
	t.mu.Unlock()
	if !done {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: demoSources(time.Time{})}
}

// Attention applies the plugin's own rule to the fake buckets.
func (t *twin) Attention(time.Time) []model.AttentionItem {
	t.mu.Lock()
	d, done := t.data, t.done
	t.mu.Unlock()
	if !done {
		return nil
	}
	return attentionOf(d.Buckets)
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
)
