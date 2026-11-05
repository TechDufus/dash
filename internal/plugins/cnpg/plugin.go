// Package cnpg is the cnpg plugin (design F.0, P3.12): the CloudNativePG Postgres clusters of the
// cluster and the age of their last backup. It publishes cnpg.clusters.
//
// It reads the clusters through the kubernetes plugin's svc.Cluster, which it requires, and the
// backup age from the prometheus plugin's svc.Querier, which it only uses: a dash without
// Prometheus still lists the clusters and says that the age is not configured. Without the
// CloudNativePG CRD the row is unavailable (hidden) and nothing is published; the plugin switches
// itself on, so a cluster that installs the CRD later gets its panel.
//
// Like every plugin it imports neither internal/config nor internal/api, and it cannot import
// another plugin, so the rules it took over from the kubernetes plugin's storage topic are
// copies.
package cnpg

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// ID is the plugin's id and its instance name: plugins.cnpg, topic cnpg.clusters.
const ID plugin.ID = "cnpg"

const (
	// topicClusters is the manifest name of the topic.
	topicClusters = "clusters"

	// Source names, on the envelope and in the status. sourceCNPG is the note a CRD that cannot
	// be read adds, as it had on the kubernetes plugin's storage topic.
	sourceKubernetes = "kubernetes"
	sourcePrometheus = "prometheus"
	sourceCNPG       = "cnpg"

	// pollEvery is how often the backup age is read: the cadence the old storage poll had.
	pollEvery = 60 * time.Second
	// watchEvery is how often the plugin looks at the kubernetes service and the cluster caches:
	// a CRD that appears, a refused list or a changed cluster reaches the topic within it.
	watchEvery = time.Second
	// pollTimeout caps one poll.
	pollTimeout = 25 * time.Second
)

// timing is the plugin's cadence; tests shorten it.
type timing struct{ poll, watch, timeout time.Duration }

func defaultTiming() timing { return timing{poll: pollEvery, watch: watchEvery, timeout: pollTimeout} }

// manifest is the plugin's declaration: it requires the kubernetes plugin and uses Prometheus.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "CloudNativePG",
		Description: "CloudNativePG Postgres clusters and the age of their last backup.",
		Version:     "1.0.0",
		Requires:    []plugin.ID{"kubernetes"},
		Uses:        []plugin.ID{"prometheus"},
		Config:      func() any { return &pluginconf.CNPG{} },
		Topics: []plugin.TopicSpec{
			{Name: topicClusters, Type: model.CNPGClustersData{}, Doc: "The CloudNativePG clusters with their instances, phase and last backup age."},
		},
	}
}

// New is the plugin's factory.
func New() plugin.Plugin { return newPlugin(defaultTiming()) }

func newPlugin(tm timing) *Plugin { return &Plugin{tm: tm} }

// Plugin is the CNPG integration. The supervisor makes a fresh one for every start of the
// instance.
type Plugin struct {
	plugin.ObjectStore
	tm timing
	rt atomic.Pointer[runtime] // set by Start
	// live counts the goroutines Start and the watch loop launched that have not ended.
	live atomic.Int32
	// polling is set while a backup-age poll is in flight.
	polling atomic.Bool

	mu     sync.Mutex // guards everything below
	cancel context.CancelFunc
	wg     sync.WaitGroup // the goroutines the plugin launched

	view view // what the watch loop last decided the row is

	ages   map[string]float64 // the last good backup ages, "<ns>/<name>"; replaced as a whole
	polled bool               // an age poll has ended, whatever its outcome
	cat    string             // the error category of the last poll; "" when it answered
	at     time.Time          // when the last poll ended
	gen    uint64             // counts the polls that ended
	// published is set when the topic holds a payload of this start, so that a row that turns
	// hidden empties it.
	published bool
}

// view is the row the watch loop decided.
type view struct {
	state   plugin.State
	message string
	sources []model.SourceStatus
}

// runtime is what one Start knows: the host, the age query and the state only the watch loop
// touches.
type runtime struct {
	h     plugin.Host
	ctx   context.Context
	query string // the backup-age expression

	seenCl   any       // the kubernetes service the GVR is watched on
	last     sig       // the inputs of the last evaluation that published or decided
	lastPoll time.Time // when the last poll was started
	warned   string    // what the log last said about the CRD, so that it says it once
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

// cluster is the kubernetes plugin's service, nil while it exports none: not running, or in the
// states in which it withdraws it (unavailable, error).
func (rt *runtime) cluster() svc.Cluster {
	if v, ok := rt.h.Service("kubernetes"); ok {
		if c, ok := v.(svc.Cluster); ok {
			return c
		}
	}
	return nil
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.CNPG, error) {
	switch c := cfg.(type) {
	case *pluginconf.CNPG:
		if c != nil {
			return *c, nil
		}
	case pluginconf.CNPG:
		return c, nil
	}
	return pluginconf.CNPG{}, fmt.Errorf("cnpg: unexpected configuration type %T", cfg)
}

// ConfigHint implements plugin.ConfigHinter. The plugin is configured whatever the section
// holds, so the hint is shown only for a plugin switched off.
func (p *Plugin) ConfigHint() string { return "plugins.cnpg.enabled" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Start launches the watch loop; it does not wait for the cluster or a poll, and it does not
// fail without a kubernetes service (a kubernetes plugin in error is not this plugin's error):
// the loop looks for the service on every round.
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{h: h, ctx: ctx, query: ageQuery(c.BackupMetricName())}

	p.mu.Lock()
	p.cancel = cancel
	p.view = view{state: plugin.StateStarting}
	p.mu.Unlock()
	p.rt.Store(rt)

	p.spawn(h, "cnpg watch", func() { p.watchLoop(rt) })
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

// Stop ends the loops and waits for them, as long as ctx allows.
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

// Status is what the watch loop last decided (see evaluate).
func (p *Plugin) Status() plugin.Status {
	if p.rt.Load() == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	p.mu.Lock()
	v := p.view
	p.mu.Unlock()
	return plugin.Status{State: v.state, Message: v.message, Sources: append([]model.SourceStatus(nil), v.sources...)}
}

// gvr is the resource the plugin reads.
var gvr = kube.GVRCNPGClusters

var (
	_ plugin.Plugin       = (*Plugin)(nil)
	_ plugin.ConfigHinter = (*Plugin)(nil)
	_ plugin.Validator    = (*Plugin)(nil)
	_ plugin.Demo         = (*Plugin)(nil)
)
