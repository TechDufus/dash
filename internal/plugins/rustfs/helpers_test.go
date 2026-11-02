package rustfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// canary is a secret that must never reach a viewer, a status or a log line.
const canary = "canary-s3cr3t-9f2c41"

// fastTiming makes the plugin poll in milliseconds.
func fastTiming() timing { return timing{poll: 20 * time.Millisecond, timeout: 2 * time.Second} }

// sample builds a sample from alternating label names and values.
func sample(v float64, kv ...string) svc.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return svc.Sample{Labels: l, Value: v}
}

// The default expressions of the four queries, as the old storage poll sent them.
const (
	exprBucketUsed     = `max by (bucket)(rustfs_bucket_usage_usage_bytes)`
	exprBucketQuota    = `max by (bucket)(rustfs_bucket_usage_quota_bytes)`
	exprCapacityUsed   = `max(rustfs_cluster_capacity_used_bytes)`
	exprCapacityUsable = `max(rustfs_cluster_capacity_usable_total_bytes)`
)

// fakeQuerier is the prometheus plugin's service: it answers an expression with the samples set
// for exactly that text and records what was asked. A closed gate holds every query until it is
// opened or its context ends.
type fakeQuerier struct {
	mu   sync.Mutex
	vals map[string][]svc.Sample
	errs map[string]error
	fail error // every query fails with this, when set
	seen []string
	n    atomic.Int64
	gate chan struct{}
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{vals: map[string][]svc.Sample{}, errs: map[string]error{}}
}

func (f *fakeQuerier) Instant(ctx context.Context, expr string) ([]svc.Sample, error) {
	f.n.Add(1)
	f.mu.Lock()
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, expr)
	if f.fail != nil {
		return nil, f.fail
	}
	if err := f.errs[expr]; err != nil {
		return nil, err
	}
	return slices.Clone(f.vals[expr]), nil
}

func (f *fakeQuerier) Range(context.Context, string, time.Time, time.Time, time.Duration) ([]model.SeriesLine, error) {
	return nil, errors.New("not used")
}

func (f *fakeQuerier) set(expr string, s ...svc.Sample) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vals[expr] = s
	delete(f.errs, expr)
}

func (f *fakeQuerier) setErr(expr string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[expr] = err
}

func (f *fakeQuerier) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

// asked lists every expression answered, in order.
func (f *fakeQuerier) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// fill sets a store with three buckets (one over 90% of its quota, one without a quota) and
// 3 of 8 TB used.
func (f *fakeQuerier) fill() {
	f.set(exprBucketUsed, sample(95, "bucket", "backups"), sample(40, "bucket", "media"), sample(7, "bucket", "scratch"))
	f.set(exprBucketQuota, sample(100, "bucket", "backups"), sample(100, "bucket", "media"))
	f.set(exprCapacityUsed, sample(3e12))
	f.set(exprCapacityUsable, sample(8e12))
}

// syncBuf is a log sink safe for concurrent writers.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// rig is the plugin on a real supervisor and hub, next to a fake prometheus plugin that exports
// the fake querier.
type rig struct {
	t    *testing.T
	hub  *hub.Hub
	sup  *plugin.Supervisor
	set  *plugintest.Settings
	q    *fakeQuerier
	logs *syncBuf
	p    *Plugin // the instance the supervisor last made
	cfg  pluginconf.RustFS
}

type rigOpts struct {
	cfg     func(*pluginconf.RustFS) // adjusts the default configuration
	enabled *bool                    // plugins.rustfs.enabled; true when nil
	unset   bool                     // leave enabled unset (nil) instead
	demo    bool                     // run the twins
	noProm  bool                     // the prometheus plugin is switched off at the start
	scrub   func(string) string      // Options.ScrubLog
	prefill func(*fakeQuerier)       // fills the querier before the plugin starts
	gated   bool                     // hold every query until the test opens the gate
}

// newRig starts the plugin with the default configuration plus o.cfg and enabled: true, unless
// the options say otherwise. A dependency that is switched off at the start can be switched on
// later with switchOn.
func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, q: newFakeQuerier(), logs: &syncBuf{}}
	r.cfg = pluginconf.DefaultRustFS()
	on := true
	switch {
	case o.unset:
	case o.enabled != nil:
		r.cfg.Enabled = o.enabled
	default:
		r.cfg.Enabled = &on
	}
	if o.cfg != nil {
		o.cfg(&r.cfg)
	}
	if o.gated {
		r.q.gate = make(chan struct{})
	}
	if o.prefill != nil {
		o.prefill(r.q) // before the plugin's first poll: a poll never sees half of it
	}
	r.hub = hub.New()
	r.set = &plugintest.Settings{}
	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Enabled: r.cfg.Enabled, Config: &r.cfg})
	promCfg := plugin.InstanceConfig{Name: "prometheus", Type: "prometheus"}
	if o.noProm {
		off := false
		promCfg.Enabled = &off
	}
	r.set.Set(promCfg)

	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: r.hub, Settings: r.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Demo: o.demo,
		ScrubLog: o.scrub,
	})
	prom := plugintest.New("prometheus", func(f *plugintest.Fake) { f.ServiceVal = r.q })
	promFactory := prom.Factory()
	if o.demo {
		promFactory = plugintest.WithDemo(prom, prom)
	}
	if err := sup.Add(promFactory); err != nil {
		t.Fatal(err)
	}
	if err := sup.Add(func() plugin.Plugin {
		p := newPlugin(fastTiming())
		r.p = p
		return p
	}); err != nil {
		t.Fatal(err)
	}
	r.hub.SetOrder(sup.TopicOrder)
	r.sup = sup
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return r
}

// switchOn starts the prometheus plugin, as a configuration reload does.
func (r *rig) switchOn() {
	r.t.Helper()
	r.set.Set(plugin.InstanceConfig{Name: "prometheus", Type: "prometheus"})
	r.sup.Reload()
}

// row is the plugin's row of core.plugins as it is now.
func (r *rig) row() model.PluginInfo {
	r.t.Helper()
	for _, pi := range r.sup.Infos() {
		if pi.ID == string(ID) {
			return pi
		}
	}
	r.t.Fatal("the plugin has no row")
	return model.PluginInfo{}
}

// waitState waits for the row to reach a state and returns it.
func (r *rig) waitState(want ...model.PluginState) model.PluginInfo {
	r.t.Helper()
	var last model.PluginInfo
	eventually(r.t, "the row to be one of "+string(want[0]), func() bool {
		last = r.row()
		return slices.Contains(want, last.State)
	})
	return last
}

// contributor is the plugin's overview contributor as the supervisor hands it out.
func (r *rig) contributor() (plugin.Contributor, bool) {
	for _, c := range r.sup.Contributors() {
		if c.ID == ID {
			return c, true
		}
	}
	return plugin.Contributor{}, false
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stored is the latest envelope of a topic, decoded, and whether there is one.
func stored[T any](t *testing.T, h *hub.Hub, topic string) (T, []model.SourceStatus, bool) {
	t.Helper()
	var zero T
	ev, ok := h.Latest("", model.Topic(topic))
	if !ok {
		return zero, nil, false
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		t.Fatal(err)
	}
	var d T
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d, env.Sources, true
}

// waitTopic waits for a published payload of the topic that satisfies ok.
func waitTopic[T any](t *testing.T, h *hub.Hub, topic string, ok func(T) bool) (T, []model.SourceStatus) {
	t.Helper()
	var last T
	var srcs []model.SourceStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d, s, found := stored[T](t, h, topic); found {
			last, srcs = d, s
			if ok(d) {
				return d, s
			}
		}
		time.Sleep(3 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a payload of %s; last %+v", topic, last)
	return last, srcs
}

func anyUsage(model.RustFSUsageData) bool { return true }

// usage waits for the first published usage payload.
func (r *rig) usage() (model.RustFSUsageData, []model.SourceStatus) {
	r.t.Helper()
	return waitTopic(r.t, r.hub, "rustfs.usage", anyUsage)
}

// hasSource reports whether srcs holds a source with that name and outcome.
func hasSource(srcs []model.SourceStatus, name string, ok bool, errCat string) bool {
	return slices.ContainsFunc(srcs, func(s model.SourceStatus) bool { return s.Name == name && s.OK == ok && s.Error == errCat })
}
