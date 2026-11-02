// Package rustfs is the rustfs plugin (design F.0, P3.12): the buckets and capacity of a RustFS
// object store, read from Prometheus. It publishes rustfs.usage and contributes one attention
// item for every bucket that is close to its quota.
//
// It reads the metrics through the prometheus plugin's svc.Querier, which it requires, and it is
// off until plugins.rustfs.enabled is true: nothing in the settings says that an object store
// exists, so the plugin cannot switch itself on (decision D17).
//
// Like every plugin it imports neither internal/config nor internal/api, and it cannot import
// internal/topics, so the rules it took over from the kubernetes plugin's storage topic (the
// bucket health and the quota rule) are copies.
package rustfs

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// ID is the plugin's id and its instance name: plugins.rustfs, topic rustfs.usage.
const ID plugin.ID = "rustfs"

const (
	// topicUsage is the manifest name of the topic.
	topicUsage = "usage"

	// sourcePrometheus is the plugin's one source, on the envelope, in the status and on the
	// overview.
	sourcePrometheus = "prometheus"

	// pollEvery is how often the metrics are read: the cadence the old storage poll had.
	pollEvery = 60 * time.Second
	// pollTimeout caps one poll, however many queries it holds.
	pollTimeout = 25 * time.Second
)

// timing is the plugin's cadence; tests shorten it.
type timing struct{ poll, timeout time.Duration }

func defaultTiming() timing { return timing{poll: pollEvery, timeout: pollTimeout} }

// manifest is the plugin's declaration.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "RustFS",
		Description: "Buckets and capacity of a RustFS object store.",
		Version:     "1.0.0",
		Requires:    []plugin.ID{"prometheus"},
		Config:      func() any { return &pluginconf.RustFS{} },
		Topics: []plugin.TopicSpec{
			{Name: topicUsage, Type: model.RustFSUsageData{}, Doc: "The object store's buckets with their usage and quota, and its capacity."},
		},
	}
}

// New is the plugin's factory.
func New() plugin.Plugin { return newPlugin(defaultTiming()) }

func newPlugin(tm timing) *Plugin { return &Plugin{tm: tm} }

// Plugin is the RustFS integration. The supervisor makes a fresh one for every start of the
// instance.
type Plugin struct {
	tm timing
	rt atomic.Pointer[runtime] // set by Start
	// live counts the goroutines Start launched that have not ended.
	live atomic.Int32

	mu     sync.Mutex // guards everything below
	cancel context.CancelFunc
	wg     sync.WaitGroup // the goroutines Start launched

	snap   snapshot  // the last good value of every query
	polled bool      // a poll has ended and its payload is published, whatever its outcome
	cat    string    // the error category of the last poll; "" when every query answered
	at     time.Time // when the last poll ended
}

// runtime is what one Start knows: the host and the queries the settings shape.
type runtime struct {
	h plugin.Host
	q exprs
}

// querier is the prometheus plugin's service, nil while that plugin does not run.
func (rt *runtime) querier() svc.Querier {
	if v, ok := rt.h.Service("prometheus"); ok {
		if q, ok := v.(svc.Querier); ok {
			return q
		}
	}
	return nil
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.RustFS, error) {
	switch c := cfg.(type) {
	case *pluginconf.RustFS:
		if c != nil {
			return *c, nil
		}
	case pluginconf.RustFS:
		return c, nil
	}
	return pluginconf.RustFS{}, fmt.Errorf("rustfs: unexpected configuration type %T", cfg)
}

// Configured is true only with enabled: true. The plugin is opt-in: with enabled unset or false
// the supervisor leaves it disabled, and it is never unconfigured.
func (p *Plugin) Configured(cfg any) bool {
	c, err := settings(cfg)
	return err == nil && c.Enabled != nil && *c.Enabled
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.rustfs.enabled (set true)" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Start launches the poller; it does not wait for the first poll.
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	rt := &runtime{h: h, q: newExprs(c.Metrics.Effective())}
	ctx, cancel := context.WithCancel(context.Background())

	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()
	p.rt.Store(rt)

	p.spawn(h, "rustfs poll", func() { p.pollLoop(ctx, rt) })
	return nil
}

// spawn runs fn on Host.Go and counts it, so that Stop can wait for it.
func (p *Plugin) spawn(h plugin.Host, name string, fn func()) {
	p.wg.Add(1)
	p.live.Add(1)
	h.Go(name, func() {
		defer p.wg.Done()
		defer p.live.Add(-1)
		fn()
	})
}

// Stop ends the poller and waits for it, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status is starting until the first poll has ended, then healthy, or error while a poll fails:
// the plugin has one source, so it is never degraded. The message is the category, never a URL.
func (p *Plugin) Status() plugin.Status {
	if p.rt.Load() == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	p.mu.Lock()
	polled, cat, at := p.polled, p.cat, p.at
	p.mu.Unlock()
	if !polled {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll"}
	}
	src := model.SourceStatus{Name: sourcePrometheus, OK: cat == "", Error: cat, CheckedAt: at.UTC()}
	if cat != "" {
		return plugin.Status{State: plugin.StateError, Message: sourcePrometheus + ": " + cat, Sources: []model.SourceStatus{src}}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: []model.SourceStatus{src}}
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
