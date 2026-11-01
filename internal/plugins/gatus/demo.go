package gatus

import (
	"context"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
)

// demoEvery is how often the twin's check history moves on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// demoCritical is what the twin treats as critical: the fake world has an infrastructure group
// for the Status page to list first.
var demoCritical = []string{"infrastructure"}

// noLinks is the world's link source for a twin that shows no object links.
type noLinks struct{}

func (noLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }
func (noLinks) VMConsole(int, string) []model.Link           { return []model.Link{} }

// twin is the plugin's demo twin (design 1.9): the same manifest, the checks of the fake world,
// and no network. It exports the same service and raises the same attention items, so what
// consumes them behaves as it does against a real Gatus.
type twin struct {
	state *service
	w     *world.World

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newTwin() *twin { return &twin{} }

func (t *twin) Manifest() plugin.Manifest { return manifest() }

// Start publishes the world's checks now and then every demoEvery.
func (t *twin) Start(_ context.Context, h plugin.Host, _ any) error {
	s := newService(append([]string(nil), demoCritical...))
	w := world.New(h.Now, h.Now(), noLinks{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.mu.Lock()
	t.state, t.w, t.cancel, t.done = s, w, cancel, done
	t.mu.Unlock()

	publish := func() {
		data, srcs := s.record(w.Status().Endpoints, nil, h.Now())
		h.Publish("status", data, srcs)
	}
	publish()
	h.Go("gatus demo", func() {
		defer close(done)
		tick := time.NewTicker(demoEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				publish()
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

func (t *twin) service() *service {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

func (t *twin) Status() plugin.Status {
	if s := t.service(); s != nil {
		return s.status()
	}
	return plugin.Status{State: plugin.StateStarting}
}

func (t *twin) Service() any {
	if s := t.service(); s != nil {
		return svc.EndpointHealth(s)
	}
	return nil
}

func (t *twin) Attention(now time.Time) []model.AttentionItem {
	if s := t.service(); s != nil {
		return attention(s, now)
	}
	return nil
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.Provider        = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
)
