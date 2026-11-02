package plugin_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// bare implements Plugin and nothing else.
type bare struct{ id plugin.ID }

func (b bare) Manifest() plugin.Manifest {
	return plugin.Manifest{ID: b.id, Name: string(b.id), Version: "1.0.0"}
}
func (bare) Start(context.Context, plugin.Host, any) error { return nil }
func (bare) Stop(context.Context) error                    { return nil }
func (bare) Status() plugin.Status                         { return plugin.Status{State: plugin.StateHealthy} }

// attentive adds an overview source to bare.
type attentive struct{ bare }

func (attentive) Attention(time.Time) []model.AttentionItem { return nil }

func TestContributorsAreRunningPluginsWithASource(t *testing.T) {
	off := false
	fake := plugintest.New("fk")
	e := newEnv(t, nil,
		func() plugin.Plugin { return bare{"bb"} },
		func() plugin.Plugin { return attentive{bare{"at"}} },
		fake.Factory(),
		plugintest.New("of").Factory(),
	)
	e.set.Set(plugin.InstanceConfig{Name: "of", Enabled: &off})
	e.run(t)

	got := e.sup.Contributors()
	var ids []plugin.ID
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if len(ids) != 2 || ids[0] != "at" || ids[1] != "fk" {
		t.Fatalf("contributors = %v, want [at fk] in registry order (not bb: no source; not of: disabled)", ids)
	}
	if got[0].Attention == nil || got[0].KPIs != nil || got[0].Topology != nil {
		t.Errorf("at implements only AttentionSource: %+v", got[0])
	}
	if got[1].Attention == nil || got[1].KPIs == nil || got[1].Topology == nil {
		t.Errorf("the fake implements all three: %+v", got[1])
	}
}

// attentionList is the attention list of a fake, which the test sets: nil is "no opinion yet".
type attentionList struct {
	mu    sync.Mutex
	items []model.AttentionItem
}

func (l *attentionList) set(items []model.AttentionItem) {
	l.mu.Lock()
	l.items = items
	l.mu.Unlock()
}

func (l *attentionList) get(time.Time) []model.AttentionItem {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.items
}

// stampedFake is a plugin "aa" with a configuration, whose attention items have no time of their own.
func stampedFake(opts ...func(*plugintest.Fake)) (*plugintest.Fake, *attentionList) {
	l := &attentionList{}
	l.set([]model.AttentionItem{{ID: "vm:1"}})
	f := plugintest.New("aa", append([]func(*plugintest.Fake){plugintest.Config(cfgOf)}, opts...)...)
	f.AttentionFn = l.get
	return f, l
}

// attentionVia reads a plugin's attention the way the overview does, through the supervisor.
func attentionVia(t *testing.T, e *env, id plugin.ID, now time.Time) []model.AttentionItem {
	t.Helper()
	for _, c := range e.sup.Contributors() {
		if c.ID == id && c.Attention != nil {
			return c.Attention.Attention(now)
		}
	}
	t.Fatalf("%s is not an attention contributor", id)
	return nil
}

func sinceVia(t *testing.T, e *env, id plugin.ID, now time.Time) time.Time {
	t.Helper()
	items := attentionVia(t, e, id, now)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one", items)
	}
	return items[0].Since
}

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestSupervisorStampsItemsWithoutATimeAndKeepsNaturalOnes(t *testing.T) {
	natural := t0.Add(-time.Hour)
	f, l := stampedFake()
	l.set([]model.AttentionItem{{ID: "a"}, {ID: "b", Since: natural}})
	e := start(t, f.Factory())

	first := attentionVia(t, e, "aa", t0)
	later := attentionVia(t, e, "aa", t0.Add(time.Hour))
	if len(first) != 2 || len(later) != 2 {
		t.Fatalf("items: %+v then %+v", first, later)
	}
	for _, items := range [][]model.AttentionItem{first, later} {
		if !items[0].Since.Equal(t0) || !items[1].Since.Equal(natural) {
			t.Errorf("since = %v and %v, want %v (first sighting) and %v (its own)", items[0].Since, items[1].Since, t0, natural)
		}
	}
}

// A plugin that restarts (here a configuration change; kubernetes, proxmox and hardware also
// restart with a dependency) keeps its clocks: the tracker belongs to the instance.
func TestAttentionSinceSurvivesARestart(t *testing.T) {
	f, _ := stampedFake()
	e := start(t, f.Factory())
	if got := sinceVia(t, e, "aa", t0); !got.Equal(t0) {
		t.Fatalf("first since = %v", got)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "changed"}})
	e.reloadUntil(t, "a to restart", func() bool { return f.Starts() == 2 && e.sup.Running("aa") })
	if got := sinceVia(t, e, "aa", t0.Add(time.Hour)); !got.Equal(t0) {
		t.Errorf("since after the restart = %v, want %v", got, t0)
	}
}

// R7-S1: a dependent that goes blocked while its dependency is unavailable (an API-server
// outage of 10 s or more) and starts again keeps its clocks.
func TestAttentionSinceSurvivesBlocked(t *testing.T) {
	var mu sync.Mutex
	st := plugin.StateHealthy
	f, _ := stampedFake(plugintest.Requires("xx"))
	x := plugintest.New("xx")
	x.StatusFn = func() plugin.Status { mu.Lock(); defer mu.Unlock(); return plugin.Status{State: st, Message: "later"} }
	set := func(s plugin.State) { mu.Lock(); st = s; mu.Unlock() }
	e := start(t, f.Factory(), x.Factory())
	if got := sinceVia(t, e, "aa", t0); !got.Equal(t0) {
		t.Fatalf("first since = %v", got)
	}

	set(plugin.StateUnavailable)
	eventually(t, "aa to be blocked", func() bool { s, _ := e.state(t, "aa"); return s == model.PluginBlocked })
	set(plugin.StateHealthy)
	eventually(t, "aa to run again", func() bool { return e.sup.Running("aa") })
	if f.Starts() != 2 {
		t.Fatalf("starts = %d, want the blocked period to have stopped and restarted aa", f.Starts())
	}
	if got := sinceVia(t, e, "aa", t0.Add(time.Hour)); !got.Equal(t0) {
		t.Errorf("since after the blocked period = %v, want %v", got, t0)
	}
}

// The tracker starts over when the instance is disabled or unconfigured, or its Start fails.
func TestAttentionSinceStartsOverWhenTheInstanceIsDisabledUnconfiguredOrFailsToStart(t *testing.T) {
	var mu sync.Mutex
	configured := true
	f, _ := stampedFake(func(f *plugintest.Fake) {
		f.ConfiguredFn = func(any) bool { mu.Lock(); defer mu.Unlock(); return configured }
	})
	e := start(t, f.Factory())
	if got := sinceVia(t, e, "aa", t0); !got.Equal(t0) {
		t.Fatalf("first since = %v", got)
	}

	// Disabled, then enabled again.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "aa to be disabled", func() bool { s, _ := e.state(t, "aa"); return s == model.PluginDisabled })
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true)})
	e.reloadUntil(t, "aa to run", func() bool { return e.sup.Running("aa") })
	t1 := t0.Add(time.Hour)
	if got := sinceVia(t, e, "aa", t1); !got.Equal(t1) {
		t.Errorf("since after disable and enable = %v, want %v", got, t1)
	}

	// Unconfigured, then configured again.
	mu.Lock()
	configured = false
	mu.Unlock()
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &cfg{URL: "a"}})
	e.reloadUntil(t, "aa to be unconfigured", func() bool { s, _ := e.state(t, "aa"); return s == model.PluginUnconfigured })
	mu.Lock()
	configured = true
	mu.Unlock()
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &cfg{URL: "b"}})
	e.reloadUntil(t, "aa to run", func() bool { return e.sup.Running("aa") })
	t2 := t1.Add(time.Hour)
	if got := sinceVia(t, e, "aa", t2); !got.Equal(t2) {
		t.Errorf("since after unconfigured and configured = %v, want %v", got, t2)
	}

	// A restart whose Start fails, then one that works.
	f.StartFn = func(context.Context, plugin.Host, any) error { return errors.New("refused") }
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &cfg{URL: "c"}})
	e.reloadUntil(t, "aa to fail to start", func() bool { s, _ := e.state(t, "aa"); return s == model.PluginError })
	f.StartFn = nil
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true), Config: &cfg{URL: "d"}})
	e.reloadUntil(t, "aa to run", func() bool { return e.sup.Running("aa") })
	t3 := t2.Add(time.Hour)
	if got := sinceVia(t, e, "aa", t3); !got.Equal(t3) {
		t.Errorf("since after a failed start = %v, want %v", got, t3)
	}
}

// An overview rebuild can hold a contributor across a disable. Its late call must not stamp the
// list into the tracker the disable reset, neither while the instance is disabled nor after it
// runs again: the next run's items get the time they are first seen then.
func TestStaleContributorDoesNotStampAfterTheTrackerReset(t *testing.T) {
	f, _ := stampedFake()
	e := start(t, f.Factory())
	var stale plugin.AttentionSource
	for _, c := range e.sup.Contributors() {
		if c.ID == "aa" {
			stale = c.Attention
		}
	}
	if stale == nil {
		t.Fatal("aa is not an attention contributor")
	}
	if got := sinceVia(t, e, "aa", t0); !got.Equal(t0) {
		t.Fatalf("first since = %v", got)
	}

	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "aa to be disabled", func() bool { s, _ := e.state(t, "aa"); return s == model.PluginDisabled })
	if got := stale.Attention(t0.Add(time.Hour)); got != nil {
		t.Fatalf("a contributor of a stopped instance came back with %+v, want nil", got)
	}

	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true)})
	e.reloadUntil(t, "aa to run", func() bool { return e.sup.Running("aa") })
	if got := stale.Attention(t0.Add(90 * time.Minute)); got != nil {
		t.Fatalf("a contributor of an earlier run came back with %+v, want nil", got)
	}
	t2 := t0.Add(2 * time.Hour)
	if got := sinceVia(t, e, "aa", t2); !got.Equal(t2) {
		t.Errorf("since after the late calls = %v, want %v (the first sighting of the new run)", got, t2)
	}
}

// nil is "no opinion yet": the sightings stay. A non-nil empty list is "nothing to raise": they go.
func TestNilAttentionLeavesTheSightingsAndAnEmptyListPrunesThem(t *testing.T) {
	f, l := stampedFake()
	e := start(t, f.Factory())
	if got := sinceVia(t, e, "aa", t0); !got.Equal(t0) {
		t.Fatalf("first since = %v", got)
	}

	l.set(nil)
	if got := attentionVia(t, e, "aa", t0.Add(time.Minute)); got != nil {
		t.Fatalf("a nil list came back as %+v", got)
	}
	l.set([]model.AttentionItem{{ID: "vm:1"}})
	if got := sinceVia(t, e, "aa", t0.Add(time.Hour)); !got.Equal(t0) {
		t.Errorf("since after a nil list = %v, want %v (no opinion keeps the sighting)", got, t0)
	}

	l.set([]model.AttentionItem{})
	if got := attentionVia(t, e, "aa", t0.Add(2*time.Hour)); got == nil || len(got) != 0 {
		t.Fatalf("an empty list came back as %#v", got)
	}
	l.set([]model.AttentionItem{{ID: "vm:1"}})
	t1 := t0.Add(3 * time.Hour)
	if got := sinceVia(t, e, "aa", t1); !got.Equal(t1) {
		t.Errorf("since after an empty list = %v, want %v (it pruned the sighting)", got, t1)
	}
}

// A running instance whose row is hidden (unavailable) contributes no data, only its sources;
// it contributes again, with its data, when the row is visible.
func TestHiddenContributorKeepsItsSourcesAndDropsItsData(t *testing.T) {
	var mu sync.Mutex
	st := plugin.StateUnavailable
	f := plugintest.New("aa")
	f.StatusFn = func() plugin.Status {
		mu.Lock()
		defer mu.Unlock()
		return plugin.Status{State: st, Sources: []model.SourceStatus{{Name: "up", OK: false, Error: "unreachable"}}}
	}
	f.AttentionFn = func(time.Time) []model.AttentionItem { return []model.AttentionItem{{ID: "x"}} }
	f.KPIFn = func() []model.KPI { return []model.KPI{{Key: "k"}} }
	f.TopologyFn = func() model.Topology { return model.Topology{VMs: []model.TopologyVM{{}}} }
	e := start(t, f.Factory())

	find := func() plugin.Contributor {
		for _, c := range e.sup.Contributors() {
			if c.ID == "aa" {
				return c
			}
		}
		t.Fatal("aa is not a contributor")
		return plugin.Contributor{}
	}
	c := find()
	if c.Attention != nil || c.KPIs != nil || c.Topology != nil {
		t.Errorf("a hidden contributor kept its data: %+v", c)
	}
	if c.Sources == nil || len(c.Sources()) != 1 || c.Sources()[0].Error != "unreachable" {
		t.Errorf("a hidden contributor lost its sources")
	}

	mu.Lock()
	st = plugin.StateHealthy
	mu.Unlock()
	c = find()
	if c.Attention == nil || c.KPIs == nil || c.Topology == nil || c.Sources == nil {
		t.Errorf("a visible contributor lost something: %+v", c)
	}
}
