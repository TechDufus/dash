package flux

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

// topic is the wire name of the plugin's topic.
const topic = model.Topic("flux.objects")

// t0 is the fixed moment of the tests that need a time, and tAt is its text in a condition.
var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const tAt = "2026-01-02T03:04:05Z"

func fastTiming() timing { return timing{tick: 5 * time.Millisecond} }

// cfgPtr is the default section, as the supervisor hands it to Start.
func cfgPtr() *pluginconf.Flux {
	c := pluginconf.DefaultFlux()
	return &c
}

// eventually polls cond until it holds; there is no fixed sleep to tune.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// kubeFake is the kubernetes plugin as flux sees it: a Provider whose service a test switches
// (nil is "no service", which is what kubernetes exports while its row is in error) and whose
// row a test sets.
type kubeFake struct {
	*plugintest.Fake
	mu    sync.Mutex
	c     svc.Cluster
	state plugin.State
	// calls counts the lookups of the service: one per round of the plugin, and one per status.
	calls atomic.Int64
}

func newKubeFake(c svc.Cluster) *kubeFake {
	k := &kubeFake{Fake: plugintest.New("kubernetes"), c: c, state: plugin.StateHealthy}
	k.Fake.StatusFn = func() plugin.Status {
		k.mu.Lock()
		defer k.mu.Unlock()
		return plugin.Status{State: k.state}
	}
	return k
}

// setState sets the row the fake reports.
func (k *kubeFake) setState(s plugin.State) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.state = s
}

func (k *kubeFake) Service() any {
	k.calls.Add(1)
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.c == nil {
		return nil
	}
	return k.c
}

func (k *kubeFake) setService(c svc.Cluster) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.c = c
}

func (k *kubeFake) factory() plugin.Factory { return func() plugin.Plugin { return k } }

// Demo makes the fake its own twin, so the supervisor in demo mode runs it (flux requires it).
func (k *kubeFake) Demo() plugin.Plugin { return k }

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

// clock is the supervisor's time: it moves only when a test says so.
type clock struct{ ns atomic.Int64 }

func newClock() *clock {
	c := &clock{}
	c.ns.Store(t0.UnixNano())
	return c
}

func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

// rig is the plugin on a real supervisor and hub, next to a fake kubernetes plugin that exports
// a plugintest.KubeCluster.
type rig struct {
	t    *testing.T
	hub  *hub.Hub
	sup  *plugin.Supervisor
	set  *plugintest.Settings
	kc   *plugintest.KubeCluster
	kube *kubeFake
	clk  *clock
	logs *syncBuf
	cfg  pluginconf.Flux
}

type rigOpts struct {
	cfg       func(*pluginconf.Flux)                // adjusts the default configuration
	actions   bool                                  // plugins.flux.actions.enabled
	deny      []string                              // plugins.flux.actions.denyNamespaces
	demo      bool                                  // run the twin
	noKube    bool                                  // the kubernetes plugin is switched off at the start
	kubeState plugin.State                          // the row of the fake kubernetes plugin; healthy by default
	noService bool                                  // the fake kubernetes plugin exports no service at the start
	scrub     func(string) string                   // Options.ScrubLog
	prefill   func(*rig)                            // fills the cluster before the plugin starts
	links     func(plugin.LinkRequest) []model.Link // a link provider that runs next to flux
}

// newRig starts the plugin with the default configuration plus o.cfg, over a cluster that is
// synced and serves and has listed everything, and holds no object.
func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, kc: plugintest.NewKubeCluster(), clk: newClock(), logs: &syncBuf{}, hub: hub.New(), set: &plugintest.Settings{}}
	r.kube = newKubeFake(r.kc)
	if o.noService {
		r.kube.setService(nil)
	}
	if o.kubeState != "" {
		r.kube.setState(o.kubeState)
	}
	r.cfg = pluginconf.DefaultFlux()
	if o.cfg != nil {
		o.cfg(&r.cfg)
	}
	if o.prefill != nil {
		o.prefill(r) // before the plugin's first round: a round never sees half of it
	}

	off := false
	kic := plugin.InstanceConfig{Name: "kubernetes", Type: "kubernetes"}
	if o.noKube {
		kic.Enabled = &off
	}
	r.set.Set(kic, plugin.InstanceConfig{
		Name: ID, Type: ID, Config: &r.cfg, ActionsEnabled: o.actions, DenyNamespaces: o.deny,
	})
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: r.hub, Settings: r.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log:      slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		ScrubLog: o.scrub, Now: r.clk.now, Demo: o.demo,
	})
	kf := r.kube.factory()
	if o.demo {
		kf = plugintest.WithDemo(r.kube.Fake, r.kube)
	}
	if err := sup.Add(kf); err != nil {
		t.Fatal(err)
	}
	if o.links != nil {
		lp := plugintest.New("headlamp", func(f *plugintest.Fake) { f.LinksFn = o.links })
		if err := sup.Add(lp.Factory()); err != nil {
			t.Fatal(err)
		}
	}
	if err := sup.Add(func() plugin.Plugin { return &Plugin{t: fastTiming()} }); err != nil {
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

// reload applies the settings again, as a configuration reload does.
func (r *rig) reload() { r.sup.Reload() }

// row is flux's row of core.plugins.
func (r *rig) row() model.PluginInfo {
	r.t.Helper()
	for _, pi := range r.sup.Infos() {
		if pi.ID == string(ID) {
			return pi
		}
	}
	r.t.Fatal("flux is not registered")
	return model.PluginInfo{}
}

func (r *rig) waitState(state model.PluginState) model.PluginInfo {
	r.t.Helper()
	var last model.PluginInfo
	eventually(r.t, "flux to be "+string(state), func() bool { last = r.row(); return last.State == state })
	return last
}

// published is the latest flux.objects event: its payload and sources.
func (r *rig) published() (model.FluxData, []model.SourceStatus, bool) {
	r.t.Helper()
	ev, ok := r.hub.Latest("", topic)
	if !ok {
		return model.FluxData{}, nil, false
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		r.t.Fatal(err)
	}
	var d model.FluxData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		r.t.Fatal(err)
	}
	return d, env.Sources, true
}

// waitPublished waits for a payload that satisfies ok.
func (r *rig) waitPublished(what string, ok func(model.FluxData) bool) model.FluxData {
	r.t.Helper()
	var got model.FluxData
	eventually(r.t, what, func() bool {
		d, _, has := r.published()
		got = d
		return has && ok(d)
	})
	return got
}

// rowSources are the sources of flux's row, as the overview reads them (kept while hidden).
func (r *rig) rowSources() []model.SourceStatus {
	c, ok := r.contributor()
	if !ok || c.Sources == nil {
		return nil
	}
	return c.Sources()
}

// contributor is flux's entry of the overview contributors, if it has one.
func (r *rig) contributor() (plugin.Contributor, bool) {
	for _, c := range r.sup.Contributors() {
		if c.ID == ID {
			return c, true
		}
	}
	return plugin.Contributor{}, false
}

// attention is what the overview would take from flux now: nil when it has no opinion.
func (r *rig) attention() []model.AttentionItem {
	c, ok := r.contributor()
	if !ok || c.Attention == nil {
		return nil
	}
	return c.Attention.Attention(r.clk.now())
}

func (r *rig) kpis() []model.KPI {
	c, ok := r.contributor()
	if !ok || c.KPIs == nil {
		return nil
	}
	return c.KPIs.KPIs()
}

// setKind puts the objects of one Flux kind in the fake cluster.
func (r *rig) setKind(kind model.FluxKind, objs ...*unstructured.Unstructured) {
	gvr, _ := gvrOf(kind)
	r.kc.SetObjects(gvr, objs...)
}

// obj is a Flux object of kind with the given changes applied.
func obj(kind model.FluxKind, ns, name string, mods ...func(*unstructured.Unstructured)) *unstructured.Unstructured {
	gvr, _ := gvrOf(kind)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvr.GroupVersion().String(),
		"kind":       string(kind),
		"metadata":   map[string]any{"namespace": ns, "name": name},
	}}
	for _, m := range mods {
		m(u)
	}
	return u
}

// cnd appends a status condition.
func cnd(typ, status, msg, at string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
		c := map[string]any{"type": typ, "status": status, "message": msg}
		if at != "" {
			c["lastTransitionTime"] = at
		}
		_ = unstructured.SetNestedSlice(u.Object, append(conds, c), "status", "conditions")
	}
}

func ready(msg string) func(*unstructured.Unstructured) { return cnd("Ready", "True", msg, tAt) }

func notReady(msg string) func(*unstructured.Unstructured) { return cnd("Ready", "False", msg, tAt) }

func suspended() func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) { _ = unstructured.SetNestedField(u.Object, true, "spec", "suspend") }
}

func annotated(key, value string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		a := u.GetAnnotations()
		if a == nil {
			a = map[string]string{}
		}
		a[key] = value
		u.SetAnnotations(a)
	}
}

// signed gives a source a spec.verify.
func signed() func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, map[string]any{"provider": "cosign"}, "spec", "verify")
	}
}

// verified gives a source a SourceVerified condition of the given status.
func verified(status string) func(*unstructured.Unstructured) {
	return cnd("SourceVerified", status, "verification "+status, tAt)
}

func statusField(value any, path ...string) func(*unstructured.Unstructured) {
	return func(u *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(u.Object, value, append([]string{"status"}, path...)...)
	}
}

// find is the object of the payload with the given ref.
func find(d model.FluxData, ref string) (model.FluxObj, bool) {
	for _, l := range [][]model.FluxObj{d.Sources, d.Kustomizations, d.HelmReleases} {
		for _, o := range l {
			if o.Ref == ref {
				return o, true
			}
		}
	}
	return model.FluxObj{}, false
}

// itemIDs are the ids of attention items, in order.
func itemIDs(items []model.AttentionItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// item is the attention item with the given id.
func item(items []model.AttentionItem, id string) (model.AttentionItem, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return model.AttentionItem{}, false
}

// kpi is the KPI with the given key.
func kpi(ks []model.KPI, key string) (model.KPI, bool) {
	for _, k := range ks {
		if k.Key == key {
			return k, true
		}
	}
	return model.KPI{}, false
}
