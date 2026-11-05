package hardware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// canary is a secret that must never reach a viewer, a status or a log line.
const canary = "canary-s3cr3t-9f2c41"

// node is the nodename of the fake host, and fastTiming makes the plugin poll in milliseconds.
const node = "pve-1"

func fastTiming() timing {
	return timing{host: 20 * time.Millisecond, machines: 20 * time.Millisecond, watch: 5 * time.Millisecond, timeout: 2 * time.Second}
}

// sample builds a sample from alternating label names and values.
func sample(v float64, kv ...string) svc.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return svc.Sample{Labels: l, Value: v}
}

// fakeQuerier is the prometheus plugin's service: it answers an expression with the samples set
// for exactly that text and records what was asked.
type fakeQuerier struct {
	mu   sync.Mutex
	vals map[string][]svc.Sample
	errs map[string]error
	fail error // every query fails with this, when set
	seen []string
	n    atomic.Int64
}

func newFakeQuerier() *fakeQuerier {
	return &fakeQuerier{vals: map[string][]svc.Sample{}, errs: map[string]error{}}
}

func (f *fakeQuerier) Instant(_ context.Context, expr string) ([]svc.Sample, error) {
	f.n.Add(1)
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

// asked lists every expression received, in order.
func (f *fakeQuerier) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func (f *fakeQuerier) askedContaining(sub string) bool {
	return slices.ContainsFunc(f.asked(), func(s string) bool { return strings.Contains(s, sub) })
}

// fillHost sets a healthy host for nodename n: two packages at 62 and 58 °C, 214 W, two online
// pools, two disks (the one in bay 0 has no serial).
func (f *fakeQuerier) fillHost(x exprs, n, scrubPrefix string) {
	f.set(x.cpu(n), sample(0.25))
	f.set(x.memTotal(n), sample(64<<30))
	f.set(x.memAvailable(n), sample(48<<30))
	f.set(x.packageTemps(n), sample(62, "sensor", "temp1"), sample(58, "sensor", "temp1"))
	f.set(x.sensors(n), sample(22, "name", "Inlet Temp"), sample(35, "name", "Exhaust Temp"))
	f.set(x.fans(n), sample(6000, "name", "Fan2"), sample(5900, "name", "Fan1"))
	f.set(x.power(n), sample(120), sample(94))
	f.set(x.psus(n), sample(0, "name", "PS1 Status"), sample(0, "name", "PS2 Status"))
	f.set(x.selFree(n), sample(16384))
	f.set(x.pools(n), sample(1, "zpool", "tank", "state", "ONLINE"), sample(1, "zpool", "rpool", "state", "ONLINE"))
	f.set(x.smartHealthy(n), sample(1, "bay", "1", "serial", "SER1", "disk", "sda"), sample(1, "bay", "0", "serial", "", "disk", "sdb"))
	f.set(x.smartTemp(n), sample(34, "bay", "0"), sample(36, "bay", "1"))
	f.set(x.smartLifeRemain(n), sample(97, "bay", "1"))
	if scrubPrefix != "" {
		f.set(x.scrubEnd(scrubPrefix, n), sample(1.7e9, "zpool", "tank"), sample(1.7e9, "zpool", "rpool"))
		f.set(x.scrubErrors(scrubPrefix, n), sample(0, "zpool", "tank"), sample(0, "zpool", "rpool"))
		f.set(x.scrubRunning(scrubPrefix, n), sample(0, "zpool", "tank"), sample(0, "zpool", "rpool"))
	}
}

// fakeNode is the proxmox plugin's service.
type fakeNode struct {
	mu   sync.Mutex
	name string
	ok   bool
}

func (f *fakeNode) NodeName() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.name, f.ok
}

func (f *fakeNode) set(name string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.name, f.ok = name, ok
}

// fakeEndpoints is the gatus plugin's service.
type fakeEndpoints struct {
	mu   sync.Mutex
	up   map[string]bool // by lowercased check name
	rev  uint64
	srcs []model.SourceStatus
}

func newFakeEndpoints(rev uint64) *fakeEndpoints {
	return &fakeEndpoints{up: map[string]bool{}, rev: rev, srcs: []model.SourceStatus{{Name: "gatus", OK: true}}}
}

func (f *fakeEndpoints) ByHost(string) (model.Health, bool) { return model.HealthUnknown, false }

func (f *fakeEndpoints) ByName(name string) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	up, known := f.up[strings.ToLower(name)]
	return up, known
}

func (f *fakeEndpoints) Revision() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rev
}

func (f *fakeEndpoints) SourceStatuses() []model.SourceStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.srcs)
}

// set records a check's result and bumps the revision, as the plugin does on a change.
func (f *fakeEndpoints) set(name string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up[strings.ToLower(name)] = up
	f.rev++
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

// rig is the plugin on a real supervisor and hub, next to fake prometheus, proxmox and gatus
// plugins that export the fake services.
type rig struct {
	t    *testing.T
	hub  *hub.Hub
	sup  *plugin.Supervisor
	set  *plugintest.Settings
	q    *fakeQuerier
	node *fakeNode
	ep   *fakeEndpoints
	logs *syncBuf
	cfg  pluginconf.Hardware
	ex   exprs
}

type rigOpts struct {
	cfg         func(*pluginconf.Hardware) // adjusts the default configuration
	demo        bool                       // run the twin
	noProm      bool                       // the prometheus plugin is switched off at the start
	noNode      bool                       // the proxmox plugin is switched off, and so no node is known
	noGat       bool                       // the gatus plugin is switched off
	scrub       func(string) string        // Options.ScrubLog
	qfail       error                      // every query fails with this from the start
	nodeUnknown bool                       // the proxmox plugin runs but has not found its node yet
	statusEvery time.Duration              // how often the supervisor reads the plugin's status; 10ms by default
	prefill     func(*rig)                 // fills the querier before the plugin starts
}

// newRig starts the plugin with the default configuration plus o.cfg. A dependency that is
// switched off at the start can be switched on later with the on helpers.
func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, q: newFakeQuerier(), node: &fakeNode{name: node, ok: !o.nodeUnknown}, ep: newFakeEndpoints(1), logs: &syncBuf{}}
	r.q.fail = o.qfail
	r.cfg = pluginconf.DefaultHardware()
	if o.cfg != nil {
		o.cfg(&r.cfg)
	}
	r.ex = newExprs(r.cfg.Selectors)
	if o.prefill != nil {
		o.prefill(r) // before the plugin's first poll: a poll never sees half of it
	}
	r.hub = hub.New()
	r.set = &plugintest.Settings{}

	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &r.cfg})
	off := false
	for _, d := range []struct {
		id  plugin.ID
		svc any
		off bool
	}{
		{"prometheus", r.q, o.noProm},
		{"proxmox", r.node, o.noNode},
		{"gatus", r.ep, o.noGat},
	} {
		ic := plugin.InstanceConfig{Name: d.id, Type: d.id}
		if d.off {
			ic.Enabled = &off
		}
		r.set.Set(ic)
	}

	statusEvery := 10 * time.Millisecond
	if o.statusEvery > 0 {
		statusEvery = o.statusEvery
	}
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: r.hub, Settings: r.set, StatusEvery: statusEvery, StopTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Demo: o.demo,
		ScrubLog: o.scrub,
	})
	for _, d := range []struct {
		id  plugin.ID
		svc any
	}{{"prometheus", r.q}, {"proxmox", r.node}, {"gatus", r.ep}} {
		f := plugintest.New(d.id, func(f *plugintest.Fake) { f.ServiceVal = d.svc })
		if err := sup.Add(f.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if err := sup.Add(func() plugin.Plugin { return newPlugin(fastTiming()) }); err != nil {
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

// switchOn starts a dependency that was off, as a configuration reload does.
func (r *rig) switchOn(id plugin.ID) {
	r.t.Helper()
	r.set.Set(plugin.InstanceConfig{Name: id, Type: id})
	r.sup.Reload()
}

// switchOff stops a dependency, which stops the plugin too (it uses it).
func (r *rig) switchOff(id plugin.ID) {
	r.t.Helper()
	off := false
	r.set.Set(plugin.InstanceConfig{Name: id, Type: id, Enabled: &off})
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

func anyHost(model.HostData) bool { return true }

// hostOf waits for the first published host.
func (r *rig) hostOf() (model.HostData, []model.SourceStatus) {
	r.t.Helper()
	return waitTopic(r.t, r.hub, "hardware.hosts", anyHost)
}

// withHost configures the one host, with the given entry.
func withHost(h pluginconf.HardwareHost) func(*pluginconf.Hardware) {
	return func(c *pluginconf.Hardware) { c.Hosts = []pluginconf.HardwareHost{h} }
}

// hasSource reports whether srcs holds a source with that name and outcome.
func hasSource(srcs []model.SourceStatus, name string, ok bool, errCat string) bool {
	return slices.ContainsFunc(srcs, func(s model.SourceStatus) bool { return s.Name == name && s.OK == ok && s.Error == errCat })
}

// stubHost is a plugin.Host whose services a test swaps at will, and that records every publish.
// It drives the plugin without a supervisor, so a test can place a dependency change between two
// ticks.
type stubHost struct {
	plugin.Host // the methods the plugin does not use

	mu   sync.Mutex
	svcs map[plugin.ID]any
	pubs []stubPub
	now  time.Time
}

type stubPub struct {
	topic string
	data  any
	srcs  []model.SourceStatus
}

func newStubHost() *stubHost {
	return &stubHost{svcs: map[plugin.ID]any{}, now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
}

func (h *stubHost) setService(id plugin.ID, v any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v == nil {
		delete(h.svcs, id)
		return
	}
	h.svcs[id] = v
}

func (h *stubHost) Service(id plugin.ID) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.svcs[id]
	return v, ok
}

func (h *stubHost) Publish(topic string, data any, srcs []model.SourceStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pubs = append(h.pubs, stubPub{topic, data, srcs})
}

func (h *stubHost) published(topic string) []stubPub {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []stubPub
	for _, p := range h.pubs {
		if p.topic == topic {
			out = append(out, p)
		}
	}
	return out
}

func (h *stubHost) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *stubHost) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *stubHost) Logger() *slog.Logger     { return slog.New(slog.DiscardHandler) }
func (h *stubHost) ScrubLog(s string) string { return s }
func (h *stubHost) CutLog(s string, n int) (string, bool) {
	return (*secret.Registry)(nil).CutRunes(s, n)
}
func (h *stubHost) Links() plugin.LinkResolver { return plugin.NoLinks }
func (h *stubHost) IconBaseURL() string        { return "" }
func (h *stubHost) Go(_ string, fn func())     { go fn() }

// stubRuntime is a plugin and its runtime over a stub host, ready for a tick to be called by
// hand.
func stubRuntime(t *testing.T, mod func(*pluginconf.Hardware)) (*Plugin, *runtime, *stubHost) {
	t.Helper()
	cfg := pluginconf.DefaultHardware()
	if mod != nil {
		mod(&cfg)
	}
	cfg = cfg.Normalized()
	h := newStubHost()
	p := newPlugin(fastTiming())
	rt := &runtime{h: h, cfg: cfg, q: newExprs(cfg.Selectors)}
	p.rt.Store(rt)
	return p, rt, h
}

// blockingQuerier answers nothing until its context ends.
type blockingQuerier struct{}

func (blockingQuerier) Instant(ctx context.Context, _ string) ([]svc.Sample, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingQuerier) Range(ctx context.Context, _ string, _, _ time.Time, _ time.Duration) ([]model.SeriesLine, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
