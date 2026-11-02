package cnpg

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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// canary is a secret that must never reach a viewer, a status or a log line.
const canary = "canary-s3cr3t-9f2c41"

// fastTiming makes the plugin look and poll in milliseconds.
func fastTiming() timing {
	return timing{poll: 20 * time.Millisecond, watch: 5 * time.Millisecond, timeout: 2 * time.Second}
}

// defaultQuery is the backup-age query the old storage poll sent.
const defaultQuery = `time() - max by (namespace,cluster)(barman_cloud_cloudnative_pg_io_last_available_backup_timestamp{role="primary"})`

// sample builds a sample from alternating label names and values.
func sample(v float64, kv ...string) svc.Sample {
	l := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		l[kv[i]] = kv[i+1]
	}
	return svc.Sample{Labels: l, Value: v}
}

// fakeQuerier is the prometheus plugin's service: it answers an expression with the samples set
// for exactly that text and records what was asked. A closed gate holds every query until it is
// opened or its context ends.
type fakeQuerier struct {
	mu   sync.Mutex
	vals map[string][]svc.Sample
	errs map[string]error
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

// asked lists every expression answered, in order.
func (f *fakeQuerier) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// fakeKube is the kubernetes plugin: it exports the cluster a test sets (none while nil, as the
// real one does in unavailable and error) and reports the state a test sets.
type fakeKube struct {
	cl    atomic.Pointer[plugintest.KubeCluster]
	state atomic.Value // plugin.State
}

func newFakeKube(cl *plugintest.KubeCluster, st plugin.State) *fakeKube {
	f := &fakeKube{}
	f.cl.Store(cl)
	f.state.Store(st)
	return f
}

func (f *fakeKube) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: "kubernetes", Name: "Kubernetes", Version: "1.0.0"}
}
func (f *fakeKube) Start(context.Context, plugin.Host, any) error { return nil }
func (f *fakeKube) Stop(context.Context) error                    { return nil }
func (f *fakeKube) Status() plugin.Status {
	return plugin.Status{State: f.state.Load().(plugin.State)}
}

// Service is an untyped nil while there is no cluster: the supervisor reads that as no service.
func (f *fakeKube) Service() any {
	if c := f.cl.Load(); c != nil {
		return c
	}
	return nil
}

// setCluster replaces the exported cluster; nil withdraws it.
func (f *fakeKube) setCluster(c *plugintest.KubeCluster) { f.cl.Store(c) }

// cnpgCluster is a CloudNativePG cluster object; the status and spec numbers and the phase are
// set under their paths.
func cnpgCluster(ns, name string, status map[string]any, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{}}
	u.SetAPIVersion(gvr.GroupVersion().String())
	u.SetKind("Cluster")
	u.SetNamespace(ns)
	u.SetName(name)
	for k, v := range status {
		_ = unstructured.SetNestedField(u.Object, v, "status", k)
	}
	for k, v := range spec {
		_ = unstructured.SetNestedField(u.Object, v, "spec", k)
	}
	return u
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

// rig is the plugin on a real supervisor and hub, next to fake kubernetes and prometheus plugins
// that export the fake cluster and the fake querier.
type rig struct {
	t    *testing.T
	hub  *hub.Hub
	sup  *plugin.Supervisor
	set  *plugintest.Settings
	q    *fakeQuerier
	kc   *plugintest.KubeCluster
	kube *fakeKube
	logs *syncBuf
	cfg  pluginconf.CNPG
	p    *Plugin // the instance the supervisor last made
}

type rigOpts struct {
	cfg       func(*pluginconf.CNPG)                // adjusts the default configuration
	noKube    bool                                  // the kubernetes plugin is switched off at the start
	noProm    bool                                  // the prometheus plugin is switched off at the start
	noService bool                                  // kubernetes runs but exports no service
	kubeState plugin.State                          // the kubernetes row's state; healthy when empty
	demo      bool                                  // run the twins
	scrub     func(string) string                   // Options.ScrubLog
	gated     bool                                  // hold every query until the test opens the gate
	prefill   func(*rig)                            // fills the fakes before the plugin starts
	link      func(plugin.LinkRequest) []model.Link // a link provider
}

// newRig starts the plugin with the default configuration plus o.cfg. A dependency switched off
// at the start can be switched on later with switchOn.
func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, q: newFakeQuerier(), kc: plugintest.NewKubeCluster(), logs: &syncBuf{}}
	r.cfg = pluginconf.DefaultCNPG()
	if o.cfg != nil {
		o.cfg(&r.cfg)
	}
	if o.gated {
		r.q.gate = make(chan struct{})
	}
	st := o.kubeState
	if st == "" {
		st = plugin.StateHealthy
	}
	var exported *plugintest.KubeCluster
	if !o.noService {
		exported = r.kc
	}
	r.kube = newFakeKube(exported, st)
	if o.prefill != nil {
		o.prefill(r) // before the plugin's first look: a poll never sees half of it
	}
	r.hub = hub.New()
	r.set = &plugintest.Settings{}
	r.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &r.cfg})
	off := false
	for _, d := range []struct {
		id  plugin.ID
		off bool
	}{{"kubernetes", o.noKube}, {"prometheus", o.noProm}} {
		ic := plugin.InstanceConfig{Name: d.id, Type: d.id}
		if d.off {
			ic.Enabled = &off
		}
		r.set.Set(ic)
	}

	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: r.hub, Settings: r.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Demo: o.demo,
		ScrubLog: o.scrub,
	})
	prom := plugintest.New("prometheus", func(f *plugintest.Fake) { f.ServiceVal = r.q })
	promFactory := prom.Factory()
	kubeFactory := func() plugin.Plugin { return r.kube }
	if o.demo {
		promFactory = plugintest.WithDemo(prom, prom)
		kubeFactory = func() plugin.Plugin { return withDemo{r.kube} }
	}
	for _, f := range []plugin.Factory{kubeFactory, promFactory} {
		if err := sup.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if o.link != nil {
		hl := plugintest.New("headlamp", func(f *plugintest.Fake) { f.LinksFn = o.link })
		hf := hl.Factory()
		if o.demo {
			hf = plugintest.WithDemo(hl, hl)
		}
		if err := sup.Add(hf); err != nil {
			t.Fatal(err)
		}
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

// withDemo makes the fake kubernetes plugin its own twin.
type withDemo struct{ *fakeKube }

func (w withDemo) Demo() plugin.Plugin { return w.fakeKube }

// switchOn starts a dependency that was off, as a configuration reload does.
func (r *rig) switchOn(id plugin.ID) {
	r.t.Helper()
	r.set.Set(plugin.InstanceConfig{Name: id, Type: id})
	r.sup.Reload()
}

// switchOff stops a dependency, which stops the plugin too.
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

func anyClusters(model.CNPGClustersData) bool { return true }

// clusters waits for the first published payload.
func (r *rig) clusters() (model.CNPGClustersData, []model.SourceStatus) {
	r.t.Helper()
	return waitTopic(r.t, r.hub, "cnpg.clusters", anyClusters)
}

func (r *rig) published() bool {
	_, ok := r.hub.Latest("", "cnpg.clusters")
	return ok
}

// hasSource reports whether srcs holds a source with that name and outcome.
func hasSource(srcs []model.SourceStatus, name string, ok bool, errCat string) bool {
	return slices.ContainsFunc(srcs, func(s model.SourceStatus) bool { return s.Name == name && s.OK == ok && s.Error == errCat })
}
