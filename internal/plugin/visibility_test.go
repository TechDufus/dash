package plugin_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

type visCfg struct {
	URL string `json:"url"`
}

// statusFlag is the state a visibility fake reports; tests flip it without restarting the plugin.
type statusFlag struct{ v atomic.Value }

func newFlag(s plugin.State) *statusFlag {
	f := &statusFlag{}
	f.v.Store(s)
	return f
}
func (f *statusFlag) set(s plugin.State) { f.v.Store(s) }
func (f *statusFlag) get() plugin.State  { return f.v.Load().(plugin.State) }

// publishing is a plugin that publishes a constant payload on topic "t" in Start, the shape that
// the hub deduplicates: after the first publish nothing it does reaches a subscriber again.
func publishing(id plugin.ID, flag *statusFlag, opts ...func(*plugintest.Fake)) *plugintest.Fake {
	all := append([]func(*plugintest.Fake){plugintest.Topics("t")}, opts...)
	f := plugintest.New(id, all...)
	f.StartFn = func(_ context.Context, h plugin.Host, _ any) error {
		h.Publish("t", map[string]int{"n": 1}, nil)
		return nil
	}
	if flag != nil {
		f.StatusFn = func() plugin.Status { return plugin.Status{State: flag.get()} }
	}
	return f
}

func TestVisibleMatchesTheSPAList(t *testing.T) {
	// web/src/plugins/visibility.ts hides exactly these four states; any other state is visible,
	// including one this build does not know.
	for _, tc := range []struct {
		s    plugin.State
		want bool
	}{
		{plugin.StateDisabled, false},
		{plugin.StateUnavailable, false},
		{plugin.StateUnconfigured, false},
		{plugin.StateBlocked, false},
		{plugin.StateStarting, true},
		{plugin.StateHealthy, true},
		{plugin.StateDegraded, true},
		{plugin.StateError, true},
		{plugin.State("from-the-future"), true},
	} {
		if got := plugin.Visible(tc.s); got != tc.want {
			t.Errorf("Visible(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// realHubEnv runs a supervisor over the real hub with one open all-topics subscriber.
func realHubEnv(t *testing.T, set *plugintest.Settings, fs ...plugin.Factory) (*plugin.Supervisor, *hub.Hub, <-chan hub.Event) {
	t.Helper()
	h := hub.New()
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second, Log: quiet})
	if err := sup.Add(fs...); err != nil {
		t.Fatal(err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	_, ch, cancelSub := h.Subscribe("", nil)
	t.Cleanup(cancelSub)
	return sup, h, ch
}

// rowVisible reports whether the core.plugins event body lists id in a visible state.
func rowVisible(t *testing.T, ev hub.Event, id string) bool {
	t.Helper()
	var env model.Envelope
	var data model.PluginsData
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	for _, pi := range data.Plugins {
		if pi.ID == id {
			return plugin.Visible(pi.State)
		}
	}
	return false
}

// TestUnavailableToHealthyResendsTheIdenticalPayload is the trap of design A.9: a subscriber opened
// while the row was hidden gets the topic after the core.plugins event that made the row visible,
// although the plugin published the same bytes before and the hub deduplicates them.
func TestUnavailableToHealthyResendsTheIdenticalPayload(t *testing.T) {
	flag := newFlag(plugin.StateUnavailable)
	sup, h, ch := realHubEnv(t, &plugintest.Settings{}, publishing("aa", flag).Factory())
	_ = sup
	if _, ok := h.Latest("", "aa.t"); !ok {
		t.Fatal("the plugin did not publish at Start")
	}
	// Nothing about "aa.t" may be pending: the subscriber opened after the publish.
	flag.set(plugin.StateHealthy)

	visibleSeen := false
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-ch:
			switch {
			case ev.Topic == string(model.TopicPlugins) && rowVisible(t, ev, "aa"):
				visibleSeen = true
			case ev.Topic == "aa.t":
				if !visibleSeen {
					t.Fatal("aa.t arrived before the core.plugins event that made the row visible")
				}
				return
			}
		case <-deadline:
			t.Fatalf("aa.t was never resent (visible row seen: %v)", visibleSeen)
		}
	}
}

func TestNoResendWhileVisible(t *testing.T) {
	e := newEnv(t, nil, publishing("aa", nil).Factory(), publishing("bb", newFlag(plugin.StateDegraded)).Factory()).run(t)
	// Healthy → degraded or error never hides a row, and the first publish only seeds.
	eventually(t, "aa and bb to run", func() bool { return e.sup.Running("aa") && e.sup.Running("bb") })
	time.Sleep(60 * time.Millisecond) // several status ticks
	if got := e.hub.Resent(); len(got) != 0 {
		t.Errorf("Resend called %v without a hidden→visible transition", got)
	}
	// A plugin that keeps publishing while visible is not resent either.
	host := e.sup.HostFor("aa")
	for i := range 5 {
		host.Publish("t", map[string]int{"n": i}, nil)
	}
	time.Sleep(30 * time.Millisecond)
	if got := e.hub.Resent(); len(got) != 0 {
		t.Errorf("Resend called %v for a visible plugin", got)
	}
}

func TestBlockedToHealthyResends(t *testing.T) {
	dep := newFlag(plugin.StateUnavailable)
	a := plugintest.New("aa", func(f *plugintest.Fake) {
		f.StatusFn = func() plugin.Status { return plugin.Status{State: dep.get()} }
	})
	b := publishing("bb", nil, plugintest.Requires("aa"))
	e := newEnv(t, nil, a.Factory(), b.Factory()).run(t)
	if st, _ := e.state(t, "bb"); st != plugin.StateBlocked {
		t.Fatalf("bb = %s, want blocked while aa is unavailable", st)
	}
	if got := e.hub.Resent(); len(got) != 0 {
		t.Fatalf("resent %v while hidden", got)
	}
	dep.set(plugin.StateHealthy)
	eventually(t, "bb to start and be resent", func() bool { return slices.Contains(e.hub.Resent(), "bb.t") })
	if st, _ := e.state(t, "bb"); !plugin.Visible(st) {
		t.Errorf("bb = %s after its dependency started", st)
	}
	// Exactly once for the transition.
	time.Sleep(50 * time.Millisecond)
	if n := countOf(e.hub.Resent(), "bb.t"); n != 1 {
		t.Errorf("bb.t resent %d times, want 1", n)
	}
}

func countOf(ts []model.Topic, t model.Topic) int {
	n := 0
	for _, x := range ts {
		if x == t {
			n++
		}
	}
	return n
}

// TestDisabledToEnabledStartPath is the restart path: Start publishes before the row flips, so the
// topic must be delivered again after the visible row. The enabled flag flips with the helper
// plugin authors call.
func TestDisabledToEnabledStartPath(t *testing.T) {
	off, on := false, true
	f := publishing("aa", nil)
	plugintest.AssertTopicsReachOpenStream(t, f.Factory(),
		plugin.InstanceConfig{Enabled: &off}, plugin.InstanceConfig{Enabled: &on})
}

// TestUnconfiguredToConfiguredThroughTheHelper covers a config flip that restarts the instance and
// a plugin that is hidden because Configured says no.
func TestUnconfiguredToConfiguredThroughTheHelper(t *testing.T) {
	on := true
	f := publishing("aa", nil,
		plugintest.Config(func() any { return &visCfg{} }),
		func(f *plugintest.Fake) {
			f.ConfiguredFn = func(cfg any) bool { return cfg.(*visCfg).URL != "" }
		})
	plugintest.AssertTopicsReachOpenStream(t, f.Factory(),
		plugin.InstanceConfig{Enabled: &on, Config: &visCfg{}},
		plugin.InstanceConfig{Enabled: &on, Config: &visCfg{URL: "http://x.example"}})
}

func TestResendFollowsTheRowNotTheOrderOfStart(t *testing.T) {
	// The detector reads prevVisible and the infos under pubMu, so a row that turns hidden and
	// visible again is resent each time it turns visible.
	flag := newFlag(plugin.StateUnavailable)
	e := newEnv(t, nil, publishing("aa", flag).Factory()).run(t)
	for i := 1; i <= 2; i++ {
		flag.set(plugin.StateHealthy)
		want := i
		eventually(t, "a resend", func() bool { return countOf(e.hub.Resent(), "aa.t") == want })
		flag.set(plugin.StateUnavailable)
		eventually(t, "the row to hide", func() bool { st, _ := e.state(t, "aa"); return st == plugin.StateUnavailable })
		time.Sleep(30 * time.Millisecond)
	}
}
