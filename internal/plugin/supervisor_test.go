package plugin_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

type env struct {
	sup *plugin.Supervisor
	hub *plugintest.Hub
	set *plugintest.Settings
	ctx context.Context
}

var quiet = slog.New(slog.DiscardHandler)

// newEnv registers the factories and returns before Run, so tests can look at Add's errors.
func newEnv(t *testing.T, mod func(*plugin.Options), fs ...plugin.Factory) *env {
	t.Helper()
	e := &env{hub: &plugintest.Hub{}, set: &plugintest.Settings{}}
	o := plugin.Options{Publisher: e.hub, Settings: e.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second, Log: quiet}
	if mod != nil {
		mod(&o)
	}
	e.sup = plugin.NewSupervisor(o)
	if err := e.sup.Add(fs...); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return e
}

// run starts the supervisor; it is shut down with the test.
func (e *env) run(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.ctx = ctx
	if err := e.sup.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); e.sup.Shutdown(context.Background()) })
	return e
}

func start(t *testing.T, fs ...plugin.Factory) *env { return newEnv(t, nil, fs...).run(t) }

func (e *env) info(t *testing.T, id string) model.PluginInfo {
	t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == id {
			return pi
		}
	}
	t.Fatalf("no plugin %q in %+v", id, e.sup.Infos())
	return model.PluginInfo{}
}

func (e *env) state(t *testing.T, id string) (model.PluginState, string) {
	pi := e.info(t, id)
	return pi.State, pi.Message
}

// data counts what plugins published, not the core.plugins status events the supervisor adds
// as states change.
func (e *env) data() int {
	n := 0
	for _, ev := range e.hub.Published() {
		if ev.Topic != plugin.TopicPlugins {
			n++
		}
	}
	return n
}

func (e *env) reloadUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	e.sup.Reload()
	eventually(t, what, cond)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// recorder collects lifecycle events across fakes.
type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, s)
}

func (r *recorder) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

// traced is a fake that records its starts and stops.
func (r *recorder) traced(id plugin.ID, opts ...func(*plugintest.Fake)) *plugintest.Fake {
	f := plugintest.New(id, opts...)
	f.StartFn = func(context.Context, plugin.Host, any) error { r.add("start:" + string(id)); return nil }
	f.StopFn = func(context.Context) error { r.add("stop:" + string(id)); return nil }
	return f
}

type cfg struct{ URL string }

func cfgOf() any { return &cfg{} }

func TestStartOrderFollowsRequiresAndUses(t *testing.T) {
	var r recorder
	a := r.traced("aa", plugintest.Requires("cc"))
	b := r.traced("bb", plugintest.Uses("cc"))
	c := r.traced("cc")
	start(t, a.Factory(), b.Factory(), c.Factory())
	if got, want := r.events(), []string{"start:cc", "start:aa", "start:bb"}; !slices.Equal(got, want) {
		t.Fatalf("start order = %v, want %v", got, want)
	}
}

func TestStartOrderTiesFollowAddOrder(t *testing.T) {
	var r recorder
	start(t, r.traced("bb").Factory(), r.traced("aa").Factory(), r.traced("cc").Factory())
	if got, want := r.events(), []string{"start:bb", "start:aa", "start:cc"}; !slices.Equal(got, want) {
		t.Fatalf("start order = %v, want %v", got, want)
	}
}

func TestStopOrderIsReverse(t *testing.T) {
	var r recorder
	a := r.traced("aa", plugintest.Requires("cc"))
	b := r.traced("bb", plugintest.Uses("cc"))
	c := r.traced("cc")
	e := newEnv(t, nil, a.Factory(), b.Factory(), c.Factory())
	if err := e.sup.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.sup.Shutdown(context.Background())
	want := []string{"start:cc", "start:aa", "start:bb", "stop:bb", "stop:aa", "stop:cc"}
	if got := r.events(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if e.sup.Running("aa") {
		t.Fatal("a still running after Shutdown")
	}
}

func TestMissingHardDependencyIsBlocked(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	e := start(t, a.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginBlocked || msg != "requires xx (not installed)" {
		t.Fatalf("a = %s %q", st, msg)
	}
	if a.Starts() != 0 {
		t.Fatal("a was started")
	}
}

func TestMissingHardDependencyDependencyDisabled(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx")
	e := newEnv(t, nil, a.Factory(), x.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "xx", Enabled: new(false)})
	e.run(t)
	if st, msg := e.state(t, "aa"); st != model.PluginBlocked || msg != "requires xx (disabled)" {
		t.Fatalf("a = %s %q", st, msg)
	}
	if a.Starts() != 0 || x.Starts() != 0 {
		t.Fatal("something was started")
	}
}

func TestMissingHardDependencyDependencyUnavailable(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx", plugintest.Status(plugin.StateUnavailable, "set plugins.xx.url"))
	e := start(t, a.Factory(), x.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginBlocked || msg != "requires xx (unavailable: set plugins.xx.url)" {
		t.Fatalf("a = %s %q", st, msg)
	}
	if st, _ := e.state(t, "xx"); st != model.PluginUnavailable {
		t.Fatalf("xx = %s", st)
	}
	if a.Starts() != 0 {
		t.Fatal("a was started")
	}
}

func TestBlockedDependentStartsWhenDependencyDoes(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx")
	e := newEnv(t, nil, a.Factory(), x.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "xx", Enabled: new(false)})
	e.run(t)
	if st, _ := e.state(t, "aa"); st != model.PluginBlocked {
		t.Fatalf("a = %s, want blocked", st)
	}
	e.set.Set(plugin.InstanceConfig{Name: "xx", Enabled: new(true)})
	e.reloadUntil(t, "a to start", func() bool { return e.sup.Running("aa") })
	if st, _ := e.state(t, "aa"); st != model.PluginHealthy {
		t.Fatalf("a = %s, want healthy", st)
	}
}

func TestBlockedDependentStartsWhenDependencyBecomesAvailable(t *testing.T) {
	var mu sync.Mutex
	st := plugin.StateUnavailable
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx")
	x.StatusFn = func() plugin.Status { mu.Lock(); defer mu.Unlock(); return plugin.Status{State: st, Message: "later"} }
	e := start(t, a.Factory(), x.Factory())
	if s, _ := e.state(t, "aa"); s != model.PluginBlocked {
		t.Fatalf("a = %s, want blocked", s)
	}
	mu.Lock()
	st = plugin.StateHealthy
	mu.Unlock()
	eventually(t, "a to start once xx is available", func() bool { return e.sup.Running("aa") })
	mu.Lock()
	st = plugin.StateUnavailable
	mu.Unlock()
	eventually(t, "a to stop when xx is unavailable again", func() bool { return !e.sup.Running("aa") })
	if s, msg := e.state(t, "aa"); s != model.PluginBlocked || !strings.HasPrefix(msg, "requires xx (unavailable") {
		t.Fatalf("a = %s %q", s, msg)
	}
}

func TestDependencyInErrorDoesNotBlock(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx", plugintest.Status(plugin.StateError, "upstream down"))
	e := start(t, a.Factory(), x.Factory())
	if st, _ := e.state(t, "aa"); st != model.PluginHealthy {
		t.Fatalf("a = %s, want healthy", st)
	}
	if st, msg := e.state(t, "xx"); st != model.PluginError || msg != "upstream down" {
		t.Fatalf("xx = %s %q", st, msg)
	}
}

func TestDependencyThatFailedToStartBlocks(t *testing.T) {
	a := plugintest.New("aa", plugintest.Requires("xx"))
	x := plugintest.New("xx")
	x.StartFn = func(context.Context, plugin.Host, any) error { return errors.New("no") }
	e := start(t, a.Factory(), x.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginBlocked || msg != "requires xx (error)" {
		t.Fatalf("a = %s %q", st, msg)
	}
}

func TestPanicInStartIsErrorAndProcessSurvives(t *testing.T) {
	bad := plugintest.New("bad", plugintest.Topics("items"))
	bad.StartFn = func(context.Context, plugin.Host, any) error { panic("boom with secret-token") }
	ok := plugintest.New("ok")
	e := newEnv(t, nil, bad.Factory(), ok.Factory())
	if err := e.sup.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { e.sup.Shutdown(context.Background()) })
	if st, msg := e.state(t, "bad"); st != model.PluginError || msg != "failed to start (see the dash log)" {
		t.Fatalf("bad = %s %q", st, msg)
	}
	if strings.Contains(e.info(t, "bad").Message, "secret-token") {
		t.Fatal("panic text reached the viewer-visible message")
	}
	if st, _ := e.state(t, "ok"); st != model.PluginHealthy {
		t.Fatalf("ok = %s", st)
	}
	if e.sup.Running("bad") {
		t.Fatal("a plugin that panicked in Start is running")
	}
}

func TestStartErrorIsErrorAndRetriedOnlyWhenConfigChanges(t *testing.T) {
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	f.StartFn = func(context.Context, plugin.Host, any) error { return errors.New("dial tcp 192.0.2.1: refused") }
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginError || msg != "failed to start (see the dash log)" {
		t.Fatalf("a = %s %q", st, msg)
	}
	e.sup.Reload()
	time.Sleep(50 * time.Millisecond)
	if f.Starts() != 1 {
		t.Fatalf("an unchanged config retried a failed start: %d starts", f.Starts())
	}
	f.StartFn = nil
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "changed"}})
	e.reloadUntil(t, "a to start with the new config", func() bool { return e.sup.Running("aa") })
}

func TestPanicInHostGoIsError(t *testing.T) {
	f := plugintest.New("aa")
	f.StartFn = func(_ context.Context, h plugin.Host, _ any) error {
		h.Go("poller", func() { panic("boom") })
		return nil
	}
	e := start(t, f.Factory())
	eventually(t, "a to report the panic", func() bool { st, _ := e.state(t, "aa"); return st == model.PluginError })
	if _, msg := e.state(t, "aa"); msg != "panic in poller" {
		t.Fatalf("message = %q", msg)
	}
	// A restart (here: a config change) clears it.
	f.StartFn = nil
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true)})
	e.reloadUntil(t, "a to recover after a restart", func() bool { st, _ := e.state(t, "aa"); return st == model.PluginHealthy })
}

func TestPanicInStatusIsError(t *testing.T) {
	f := plugintest.New("aa")
	f.StatusFn = func() plugin.Status { panic("boom") }
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginError || msg != "panic in Status" {
		t.Fatalf("a = %s %q", st, msg)
	}
}

func TestInvalidStatusStateIsError(t *testing.T) {
	f := plugintest.New("aa", plugintest.Status(plugin.StateDisabled, "xx"))
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginError || msg != "invalid status" {
		t.Fatalf("a = %s %q", st, msg)
	}
}

func TestReconfigureStopsOldWithin10s(t *testing.T) {
	t.Run("stop gets the default deadline", func(t *testing.T) {
		var deadline time.Time
		f := plugintest.New("aa", plugintest.Config(cfgOf))
		f.StopFn = func(ctx context.Context) error { deadline, _ = ctx.Deadline(); return nil }
		e := newEnv(t, func(o *plugin.Options) { o.StopTimeout = 0 }, f.Factory()).run(t)
		e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "xx"}})
		e.reloadUntil(t, "restart", func() bool { return f.Starts() == 2 })
		if d := time.Until(deadline); d > 10*time.Second || d < 8*time.Second {
			t.Fatalf("Stop deadline in %v, want about 10s", d)
		}
	})
	t.Run("a hanging stop is abandoned", func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		f := plugintest.New("aa", plugintest.Config(cfgOf))
		f.StopFn = func(context.Context) error { <-release; return nil }
		e := newEnv(t, func(o *plugin.Options) { o.StopTimeout = 50 * time.Millisecond }, f.Factory()).run(t)
		begin := time.Now()
		e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "xx"}})
		e.reloadUntil(t, "the new instance to start despite the hanging Stop", func() bool { return f.Starts() == 2 })
		if time.Since(begin) > 2*time.Second {
			t.Fatalf("restart took %v", time.Since(begin))
		}
	})
}

func TestChangedInstanceAndDependentsRestart(t *testing.T) {
	var r recorder
	a := r.traced("aa", plugintest.Config(cfgOf))
	b := r.traced("bb", plugintest.Uses("aa"))
	c := r.traced("cc")
	e := start(t, a.Factory(), b.Factory(), c.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "new"}})
	e.reloadUntil(t, "restart", func() bool { return a.Starts() == 2 && b.Starts() == 2 })
	want := []string{"start:aa", "start:bb", "start:cc", "stop:bb", "stop:aa", "start:aa", "start:bb"}
	if got := r.events(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if got := a.Config().(*cfg).URL; got != "new" {
		t.Fatalf("a restarted with config %q", got)
	}
	// Reloading what is already applied touches nothing.
	e.sup.Reload()
	time.Sleep(50 * time.Millisecond)
	if a.Starts() != 2 || c.Starts() != 1 {
		t.Fatalf("a starts %d, c starts %d", a.Starts(), c.Starts())
	}
}

func TestRestartOnlyInstanceIsNotTouched(t *testing.T) {
	r := plugintest.New("rr", plugintest.Config(cfgOf))
	a := plugintest.New("aa", plugintest.Config(cfgOf))
	e := start(t, plugintest.RestartOnly(r), a.Factory())
	if e.sup.Live("plugins.rr") {
		t.Fatal("a RestartOnly section is live")
	}
	if !e.sup.Live("plugins.aa") || e.sup.Live("plugins.nope") || e.sup.Live("server") || e.sup.Live("plugins.") {
		t.Fatal("Live reports the wrong sections")
	}
	e.set.Set(plugin.InstanceConfig{Name: "rr", Config: &cfg{URL: "xx"}}, plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "xx"}})
	e.reloadUntil(t, "a to restart", func() bool { return a.Starts() == 2 })
	e.set.Set(plugin.InstanceConfig{Name: "rr", Enabled: new(false)})
	e.sup.Reload()
	time.Sleep(50 * time.Millisecond)
	if r.Starts() != 1 || r.Stops() != 0 || !e.sup.Running("rr") {
		t.Fatalf("r starts %d stops %d running %v", r.Starts(), r.Stops(), e.sup.Running("rr"))
	}
}

func TestSecondInstanceOfOneTypeIsRejected(t *testing.T) {
	first := plugintest.New("xx", func(f *plugintest.Fake) { f.M.Name = "First" })
	second := plugintest.New("xx", func(f *plugintest.Fake) { f.M.Name = "Second" })
	err := plugin.NewSupervisor(plugin.Options{}).Add(first.Factory(), second.Factory())
	if err == nil || !strings.Contains(err.Error(), "First") || !strings.Contains(err.Error(), "Second") || !strings.Contains(err.Error(), `"xx"`) {
		t.Fatalf("err = %v", err)
	}
	// A later Add of the same type is the same error.
	s := plugin.NewSupervisor(plugin.Options{})
	if err := s.Add(first.Factory()); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(second.Factory()); err == nil {
		t.Fatal("second Add of the same type succeeded")
	}
	if got := len(s.Manifests()); got != 1 {
		t.Fatalf("%d manifests after a rejected Add", got)
	}
}

func TestInvalidAndReservedIDsRejected(t *testing.T) {
	for _, id := range []plugin.ID{"", "a", "A1", "core", "a-b", "a_b", "1ab", "ab.c", plugin.ID(strings.Repeat("a", 33))} {
		if err := plugin.NewSupervisor(plugin.Options{}).Add(plugintest.New(id).Factory()); err == nil {
			t.Errorf("id %q accepted", id)
		}
		if id.Valid() {
			t.Errorf("%q.Valid()", id)
		}
	}
	for _, id := range []plugin.ID{"ab", "proxmox", "k8s2", plugin.ID(strings.Repeat("a", 32))} {
		if err := plugin.NewSupervisor(plugin.Options{}).Add(plugintest.New(id).Factory()); err != nil {
			t.Errorf("id %q rejected: %v", id, err)
		}
	}
}

func TestDuplicateTopicAndActionNamesRejected(t *testing.T) {
	act := func(names ...string) func(*plugintest.Fake) {
		return func(f *plugintest.Fake) {
			for _, n := range names {
				f.M.Actions = append(f.M.Actions, plugin.ActionSpec{Name: n})
			}
		}
	}
	cases := map[string]*plugintest.Fake{
		"duplicate topic":        plugintest.New("aa", plugintest.Topics("items", "items")),
		"topic with a dot":       plugintest.New("aa", plugintest.Topics("a.b")),
		"empty topic":            plugintest.New("aa", plugintest.Topics("")),
		"uppercase topic":        plugintest.New("aa", plugintest.Topics("Items")),
		"too long topic":         plugintest.New("aa", plugintest.Topics(strings.Repeat("a", 33))),
		"duplicate action":       plugintest.New("aa", act("restart", "restart")),
		"empty action":           plugintest.New("aa", act("")),
		"action with a dash":     plugintest.New("aa", act("re-start")),
		"action starting with .": plugintest.New("aa", act(".x")),
		"unknown role": plugintest.New("aa", func(f *plugintest.Fake) {
			f.M.Actions = []plugin.ActionSpec{{Name: "xx", MinRole: "root"}}
		}),
		"config that is not a pointer": plugintest.New("aa", plugintest.Config(func() any { return cfg{} })),
		"config that is nil":           plugintest.New("aa", plugintest.Config(func() any { return (*cfg)(nil) })),
	}
	for name, f := range cases {
		if err := plugin.NewSupervisor(plugin.Options{}).Add(f.Factory()); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := plugin.NewSupervisor(plugin.Options{}).Add(plugintest.New("aa", act("silence.create", "restart_all")).Factory()); err != nil {
		t.Errorf("valid action names rejected: %v", err)
	}
}

func TestActionDefaultsAreApplied(t *testing.T) {
	f := plugintest.New("aa", func(f *plugintest.Fake) {
		f.M.Actions = []plugin.ActionSpec{{Name: "xx"}, {Name: "y", MinRole: auth.RoleViewer, Confirm: plugin.ConfirmDialog}}
	})
	s := plugin.NewSupervisor(plugin.Options{})
	if err := s.Add(f.Factory()); err != nil {
		t.Fatal(err)
	}
	acts := s.Manifests()[0].Actions
	if acts[0].MinRole != auth.RoleAdmin || acts[0].Confirm != plugin.ConfirmNone || acts[1].MinRole != auth.RoleViewer || acts[1].Confirm != plugin.ConfirmDialog {
		t.Fatalf("actions = %+v", acts)
	}
	if f.M.Actions[0].MinRole != "" {
		t.Fatal("Add changed the plugin's own manifest")
	}
}

func TestCycleRejected(t *testing.T) {
	s := plugin.NewSupervisor(plugin.Options{})
	a := plugintest.New("aa", plugintest.Requires("bb"))
	b := plugintest.New("bb", plugintest.Uses("cc"))
	c := plugintest.New("cc", plugintest.Uses("aa"))
	err := s.Add(a.Factory(), b.Factory(), c.Factory())
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "aa -> bb -> cc -> aa") {
		t.Fatalf("err = %v", err)
	}
	if len(s.Manifests()) != 0 {
		t.Fatal("a failed Add registered something")
	}
	if err := plugin.NewSupervisor(plugin.Options{}).Add(plugintest.New("aa", plugintest.Uses("aa")).Factory()); err == nil {
		t.Fatal("a plugin that uses itself was accepted")
	}
	// A dependency that is not registered is no cycle and no error.
	if err := plugin.NewSupervisor(plugin.Options{}).Add(plugintest.New("aa", plugintest.Requires("zz")).Factory()); err != nil {
		t.Fatal(err)
	}
}

func TestAddAfterRunFails(t *testing.T) {
	e := start(t)
	if err := e.sup.Add(plugintest.New("aa").Factory()); err == nil {
		t.Fatal("Add after Run succeeded")
	}
	if err := e.sup.Run(context.Background()); err == nil {
		t.Fatal("second Run succeeded")
	}
}

func TestEnabledFalseDisablesAndRemovesTopics(t *testing.T) {
	var pub plugin.Host
	f := plugintest.New("aa", plugintest.Topics("items", "other"))
	f.StartFn = func(_ context.Context, h plugin.Host, _ any) error {
		if pub == nil { // the Host of the first start only
			pub = h
		}
		return nil
	}
	e := start(t, f.Factory())
	pub.Publish("items", 1, nil)
	if _, ok := e.hub.Last("aa.items"); !ok {
		t.Fatal("nothing published")
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "a to be disabled", func() bool { st, _ := e.state(t, "aa"); return st == model.PluginDisabled })
	if _, msg := e.state(t, "aa"); msg != "disabled" {
		t.Fatalf("message = %q", msg)
	}
	if got := e.hub.Removed(); !slices.Equal(got, []model.Topic{"aa.items", "aa.other"}) {
		t.Fatalf("removed = %v", got)
	}
	if e.sup.Running("aa") || f.Stops() != 1 {
		t.Fatalf("running %v, stops %d", e.sup.Running("aa"), f.Stops())
	}
	n := e.data()
	pub.Publish("items", 2, nil) // the Host Start was given no longer publishes
	e.sup.HostFor("aa").Publish("items", 3, nil)
	if e.data() != n {
		t.Fatal("a stopped instance published")
	}
	// Enabling it again brings it back, with a fresh Host.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true)})
	e.reloadUntil(t, "a to run again", func() bool { return e.sup.Running("aa") })
	e.sup.HostFor("aa").Publish("items", 4, nil)
	if e.data() != n+1 {
		t.Fatal("a re-enabled instance does not publish")
	}
	pub.Publish("items", 5, nil) // the old Host stays dead
	if e.data() != n+1 {
		t.Fatal("a Host of a previous start published")
	}
}

func TestAutoEnableUsesConfigured(t *testing.T) {
	configured := false
	f := plugintest.New("aa")
	f.ConfiguredFn = func(any) bool { return configured }
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginDisabled || msg != "not configured" {
		t.Fatalf("auto, not configured: %s %q", st, msg)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(true)})
	e.reloadUntil(t, "a to be unconfigured", func() bool { st, _ := e.state(t, "aa"); return st == model.PluginUnconfigured })
	if _, msg := e.state(t, "aa"); msg != "not configured" {
		t.Fatalf("message = %q", msg)
	}
	configured = true
	e.reloadUntil(t, "a to start", func() bool { return e.sup.Running("aa") })
	e.set.Set(plugin.InstanceConfig{Name: "aa"}) // auto again, now configured
	e.sup.Reload()
	time.Sleep(30 * time.Millisecond)
	if st, _ := e.state(t, "aa"); st != model.PluginHealthy {
		t.Fatalf("a = %s", st)
	}
}

func TestValidatorFailureIsUnconfigured(t *testing.T) {
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	f.ValidateFn = func(c any) error {
		if c.(*cfg).URL == "" {
			return errors.New("url is required (http://10.0.0.5)")
		}
		return nil
	}
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginUnconfigured || msg != "invalid configuration" {
		t.Fatalf("a = %s %q", st, msg)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "http://x"}})
	e.reloadUntil(t, "a to start", func() bool { return e.sup.Running("aa") })
}

func TestConfigTypeMismatchIsError(t *testing.T) {
	type other struct{ X int }
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	e := newEnv(t, nil, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &other{}})
	e.run(t)
	if st, msg := e.state(t, "aa"); st != model.PluginError || msg != "configuration type mismatch" {
		t.Fatalf("a = %s %q", st, msg)
	}
	if f.Starts() != 0 {
		t.Fatal("started with a config of the wrong type")
	}
	// Explicitly disabled wins over the mismatch.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false), Config: &other{}})
	e.reloadUntil(t, "a to be disabled", func() bool { st, _ := e.state(t, "aa"); return st == model.PluginDisabled })
}

func TestZeroConfigWhenSettingsHaveNoEntry(t *testing.T) {
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	start(t, f.Factory())
	c, ok := f.Config().(*cfg)
	if !ok || *c != (cfg{}) {
		t.Fatalf("config = %#v", f.Config())
	}
}

func TestUnknownSettingsEntryIsIgnoredAndLoggedOnce(t *testing.T) {
	var buf lockedBuffer
	f := plugintest.New("aa")
	e := newEnv(t, func(o *plugin.Options) { o.Log = slog.New(slog.NewTextHandler(&buf, nil)) }, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "ghost"})
	e.run(t)
	e.sup.Reload()
	e.sup.Reload()
	time.Sleep(50 * time.Millisecond)
	if n := strings.Count(buf.String(), "unknown plugin"); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, buf.String())
	}
	if len(e.sup.Infos()) != 1 {
		t.Fatalf("infos = %+v", e.sup.Infos())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type item struct{ N int }

func TestPublishNamespacesDropsUndeclaredWrongTypeAndAfterStop(t *testing.T) {
	var h plugin.Host
	f := plugintest.New("aa", plugintest.Typed("items", item{}), plugintest.Topics("free"))
	f.StartFn = func(_ context.Context, host plugin.Host, _ any) error { h = host; return nil }
	e := start(t, f.Factory())
	src := []model.SourceStatus{{Name: "s", OK: true}}

	h.Publish("items", item{1}, src)
	h.Publish("items", &item{2}, nil)  // pointers are dereferenced
	h.Publish("free", "anything", nil) // no declared type: anything goes
	h.Publish("undeclared", item{3}, nil)
	h.Publish("items", "wrong type", nil)
	h.Publish("items", nil, nil)
	h.Publish("items", &model.PluginsData{}, nil)
	var got []model.Topic
	for _, ev := range e.hub.Published() {
		if ev.Topic != plugin.TopicPlugins {
			got = append(got, ev.Topic)
		}
	}
	if want := []model.Topic{"aa.items", "aa.items", "aa.free"}; !slices.Equal(got, want) {
		t.Fatalf("published %v, want %v", got, want)
	}
	if ev, _ := e.hub.Last("aa.free"); ev.Data != "anything" {
		t.Fatalf("data = %v", ev.Data)
	}
	first, _ := e.hub.Last("aa.items")
	if p, ok := first.Data.(*item); !ok || p.N != 2 {
		t.Fatalf("data = %#v", first.Data)
	}

	// Core publishes under core.<name>, whenever; an unknown instance's Host drops.
	e.sup.HostFor(plugin.Core).Publish("overview", item{9}, nil)
	if _, ok := e.hub.Last("core.overview"); !ok {
		t.Fatal("core publish missing")
	}
	e.sup.HostFor(plugin.Core).Publish("Bad.Name", item{9}, nil)
	n := e.data()
	e.sup.HostFor("ghost").Publish("items", item{}, nil)
	if e.data() != n {
		t.Fatal("unknown instance published")
	}

	// After Shutdown (the instance stopped) nothing is published.
	e.sup.Shutdown(context.Background())
	h.Publish("items", item{4}, nil)
	if e.data() != n {
		t.Fatal("published after Shutdown")
	}
}

func TestPublishUsesLegacyNameWhenSet(t *testing.T) {
	var h plugin.Host
	f := plugintest.New("hw", func(f *plugintest.Fake) {
		f.M.Topics = []plugin.TopicSpec{{Name: "hosts", Legacy: "host", Type: item{}}, {Name: "disks"}}
	})
	f.StartFn = func(_ context.Context, host plugin.Host, _ any) error { h = host; return nil }
	e := start(t, f.Factory())
	h.Publish("hosts", item{1}, nil)
	h.Publish("disks", item{1}, nil)
	if _, ok := e.hub.Last("host"); !ok {
		t.Fatal("nothing published as the legacy name")
	}
	if _, ok := e.hub.Last("hw.hosts"); ok {
		t.Fatal("published under the namespaced name too")
	}
	if _, ok := e.hub.Last("hw.disks"); !ok {
		t.Fatal("a topic without a legacy name is namespaced")
	}
	if got := e.info(t, "hw").Topics; !slices.Equal(got, []string{"host", "hw.disks"}) {
		t.Fatalf("Topics = %v", got)
	}
	e.set.Set(plugin.InstanceConfig{Name: "hw", Enabled: new(false)})
	e.reloadUntil(t, "removal", func() bool { return len(e.hub.Removed()) == 2 })
	if got := e.hub.Removed(); !slices.Equal(got, []model.Topic{"host", "hw.disks"}) {
		t.Fatalf("removed = %v", got)
	}
}

func TestLegacyNamesAreValidatedAndUnique(t *testing.T) {
	with := func(id plugin.ID, topics ...plugin.TopicSpec) plugin.Factory {
		return plugintest.New(id, func(f *plugintest.Fake) { f.M.Topics = topics }).Factory()
	}
	for name, fs := range map[string][]plugin.Factory{
		"invalid legacy":            {with("aa", plugin.TopicSpec{Name: "xx", Legacy: "Bad"})},
		"legacy with a dot":         {with("aa", plugin.TopicSpec{Name: "xx", Legacy: "a.b"})},
		"duplicate within a plugin": {with("aa", plugin.TopicSpec{Name: "xx", Legacy: "v"}, plugin.TopicSpec{Name: "y", Legacy: "v"})},
		"duplicate across plugins":  {with("aa", plugin.TopicSpec{Name: "xx", Legacy: "v"}), with("bb", plugin.TopicSpec{Name: "y", Legacy: "v"})},
	} {
		if err := plugin.NewSupervisor(plugin.Options{}).Add(fs...); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := plugin.NewSupervisor(plugin.Options{}).Add(with("aa", plugin.TopicSpec{Name: "xx", Legacy: "v"}), with("bb", plugin.TopicSpec{Name: "v"})); err != nil {
		t.Errorf("a legacy name equal to another plugin's local name is fine: %v", err)
	}
}

func TestHostServiceOnlyForDeclaredRunningProviders(t *testing.T) {
	var hmu sync.Mutex
	hosts := map[string]plugin.Host{}
	get := func(id string) plugin.Host { hmu.Lock(); defer hmu.Unlock(); return hosts[id] }
	capture := func(id string) func(context.Context, plugin.Host, any) error {
		return func(_ context.Context, h plugin.Host, _ any) error {
			hmu.Lock()
			hosts[id] = h
			hmu.Unlock()
			return nil
		}
	}
	prov := plugintest.New("prov")
	prov.ServiceVal = "the service"
	prov.StartFn = capture("prov")
	req := plugintest.New("req", plugintest.Requires("prov"))
	req.StartFn = capture("req")
	uses := plugintest.New("uses", plugintest.Uses("prov"))
	uses.StartFn = capture("uses")
	other := plugintest.New("other")
	other.StartFn = capture("other")
	plain := plugintest.New("plain") // not a provider: ServiceVal nil
	plain.StartFn = capture("plain")
	user := plugintest.New("user", plugintest.Uses("plain", "off"))
	user.StartFn = capture("user")
	off := plugintest.New("off")
	off.ServiceVal = "off"
	e := newEnv(t, nil, prov.Factory(), req.Factory(), uses.Factory(), other.Factory(), plain.Factory(), user.Factory(), off.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "off", Enabled: new(false)})
	e.run(t)

	for _, id := range []string{"req", "uses"} {
		if v, ok := get(id).Service("prov"); !ok || v != "the service" {
			t.Errorf("%s: Service(prov) = %v, %v", id, v, ok)
		}
	}
	if _, ok := get("other").Service("prov"); ok {
		t.Error("an undeclared consumer got a service")
	}
	if _, ok := get("user").Service("plain"); ok {
		t.Error("a plugin without a service returned one")
	}
	if _, ok := get("user").Service("off"); ok {
		t.Error("a service of a disabled instance")
	}
	if _, ok := get("req").Service("ghost"); ok {
		t.Error("a service of an unknown id")
	}
	if _, ok := e.sup.HostFor(plugin.Core).Service("prov"); ok {
		t.Error("core got a service")
	}
	// Once the provider stops, so does the service.
	e.set.Set(plugin.InstanceConfig{Name: "prov", Enabled: new(false)})
	e.reloadUntil(t, "prov to stop", func() bool { return !e.sup.Running("prov") })
	if _, ok := get("uses").Service("prov"); ok {
		t.Error("a stopped provider still serves")
	}
}

func TestCorePluginsPublishedOnEveryStateChange(t *testing.T) {
	var mu sync.Mutex
	state := plugin.StateHealthy
	f := plugintest.New("aa")
	f.StatusFn = func() plugin.Status { mu.Lock(); defer mu.Unlock(); return plugin.Status{State: state, Message: "m"} }
	e := start(t, f.Factory())
	count := func() int {
		n := 0
		for _, ev := range e.hub.Published() {
			if ev.Topic == plugin.TopicPlugins {
				n++
			}
		}
		return n
	}
	if count() != 1 {
		t.Fatalf("%d core.plugins events after Run, want 1", count())
	}
	time.Sleep(80 * time.Millisecond) // several polls, no change
	if count() != 1 {
		t.Fatalf("%d events after idle polls, want 1", count())
	}
	mu.Lock()
	state = plugin.StateDegraded
	mu.Unlock()
	eventually(t, "the degraded state to be published", func() bool { return count() == 2 })
	last, _ := e.hub.Last(plugin.TopicPlugins)
	data := last.Data.(model.PluginsData)
	if len(data.Plugins) != 1 || data.Plugins[0].State != model.PluginDegraded || data.Plugins[0].Message != "m" {
		t.Fatalf("last = %+v", data)
	}
	if last.Sources == nil {
		t.Fatal("sources must be an empty list, not null")
	}
	// Configuration changes publish too.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "the disabled state to be published", func() bool { return count() == 3 })
}

func TestInfosOrderAndFields(t *testing.T) {
	nav := []model.NavItem{{Path: "/vms", Label: "VMs", Title: "Virtual machines", Icon: "Server"}}
	a := plugintest.New("aa", plugintest.Config(cfgOf), plugintest.Topics("vms"), func(f *plugintest.Fake) {
		f.M.Name, f.M.Version, f.M.UI.Nav = "Alpha", "2.1.0", nav
		f.M.Actions = []plugin.ActionSpec{
			{Name: "restart", Title: "Restart", Destructive: true, Confirm: plugin.ConfirmTypeName},
			{Name: "ping", Title: "Ping", Idempotent: true, MinRole: auth.RoleViewer},
		}
	})
	b := plugintest.New("bb")
	c := plugintest.New("cc", plugintest.Requires("bb"))
	e := newEnv(t, nil, c.Factory(), a.Factory(), b.Factory())
	if got := e.sup.Infos(); len(got) != 3 || got[0].State != model.PluginStarting {
		t.Fatalf("before Run: %+v", got)
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: true}, plugin.InstanceConfig{Name: "bb", Enabled: new(false)})
	e.run(t)
	infos := e.sup.Infos()
	var ids []string
	for _, pi := range infos {
		ids = append(ids, pi.ID)
	}
	if !slices.Equal(ids, []string{"cc", "aa", "bb"}) {
		t.Fatalf("order = %v (registry order, not start order)", ids)
	}
	aa := e.info(t, "aa")
	if aa.Type != "aa" || aa.Name != "Alpha" || aa.Version != "2.1.0" || aa.State != model.PluginHealthy || !aa.Configurable ||
		!slices.Equal(aa.Topics, []string{"aa.vms"}) || !slices.Equal(aa.Nav, nav) {
		t.Fatalf("aa = %+v", aa)
	}
	want := []model.ActionInfo{
		{Name: "restart", Title: "Restart", Destructive: true, Confirm: model.ConfirmTypeName},
		{Name: "ping", Title: "Ping", Idempotent: true, Confirm: model.ConfirmNone},
	}
	if len(aa.Actions) != 2 || aa.Actions[0].Name != want[0].Name || aa.Actions[0].Destructive != true || aa.Actions[0].Confirm != want[0].Confirm ||
		aa.Actions[1].Idempotent != true || aa.Actions[1].Confirm != want[1].Confirm {
		t.Fatalf("actions = %+v", aa.Actions)
	}
	cc := e.info(t, "cc")
	if cc.State != model.PluginBlocked || cc.Configurable || cc.Topics == nil || cc.Actions == nil || cc.Nav == nil || len(cc.Actions) != 0 {
		t.Fatalf("cc = %+v (lists must be empty, not null)", cc)
	}
	// Actions are listed only while mounted.
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: false})
	e.reloadUntil(t, "actions to be unmounted", func() bool { return len(e.info(t, "aa").Actions) == 0 })
}

func TestTopicOrder(t *testing.T) {
	a := plugintest.New("aa", plugintest.Topics("one", "two"))
	b := plugintest.New("bb", func(f *plugintest.Fake) { f.M.Topics = []plugin.TopicSpec{{Name: "hosts", Legacy: "host"}} })
	e := newEnv(t, nil, a.Factory(), b.Factory())
	want := []model.Topic{plugin.TopicPlugins, model.TopicOverview, "aa.one", "aa.two", "host"}
	if got := e.sup.TopicOrder(); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func TestActionMountingAndRun(t *testing.T) {
	var gotAction string
	var gotPrincipal auth.Principal
	f := plugintest.New("aa", func(f *plugintest.Fake) {
		f.M.Actions = []plugin.ActionSpec{{Name: "restart"}, {Name: "boom"}}
	})
	f.ActionFn = func(_ context.Context, action string, p auth.Principal, input any) (any, error) {
		if action == "boom" {
			panic("kaboom")
		}
		gotAction, gotPrincipal = action, p
		return input, nil
	}
	e := newEnv(t, nil, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, DenyNamespaces: []string{"kube-system"}})
	e.run(t)
	ref, ok := e.sup.Action("aa", "restart")
	if !ok || ref.Instance != "aa" || ref.Spec.Name != "restart" || ref.Spec.MinRole != auth.RoleAdmin || !slices.Equal(ref.DenyNamespaces, []string{"kube-system"}) {
		t.Fatalf("ref = %+v, %v", ref, ok)
	}
	ref.DenyNamespaces[0] = "changed"
	if again, _ := e.sup.Action("aa", "restart"); again.DenyNamespaces[0] != "kube-system" {
		t.Fatal("DenyNamespaces aliases the supervisor's copy")
	}
	p := auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}
	if out, err := ref.Run(context.Background(), p, "in"); err != nil || out != "in" || gotAction != "restart" || gotPrincipal != p {
		t.Fatalf("Run = %v, %v (%q %+v)", out, err, gotAction, gotPrincipal)
	}
	boom, _ := e.sup.Action("aa", "boom")
	_, err := boom.Run(context.Background(), p, nil)
	var ae *plugin.ActionError
	if !errors.As(err, &ae) || ae.Status != 500 || ae.Code != "internal" || strings.Contains(ae.Message, "kaboom") {
		t.Fatalf("panic became %v", err)
	}
	for _, c := range []struct{ id, name string }{{"aa", "nope"}, {"zz", "restart"}} {
		if _, ok := e.sup.Action(plugin.ID(c.id), c.name); ok {
			t.Errorf("Action(%s, %s) found", c.id, c.name)
		}
	}
	// Not mounted with actions.enabled false, or when stopped.
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: false})
	e.reloadUntil(t, "unmount", func() bool { _, ok := e.sup.Action("aa", "restart"); return !ok })
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, Enabled: new(false)})
	e.reloadUntil(t, "stop", func() bool { return !e.sup.Running("aa") })
	if _, ok := e.sup.Action("aa", "restart"); ok {
		t.Fatal("an action of a stopped instance is mounted")
	}
}

func TestActionNeedsAnActor(t *testing.T) {
	// A manifest with actions whose plugin is no Actor has none mounted.
	type noActor struct{ plugin.Plugin }
	base := plugintest.New("aa", func(f *plugintest.Fake) { f.M.Actions = []plugin.ActionSpec{{Name: "xx"}} })
	e := newEnv(t, nil, func() plugin.Plugin { return noActor{base} })
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: true})
	e.run(t)
	if _, ok := e.sup.Action("aa", "xx"); ok {
		t.Fatal("mounted without an Actor")
	}
	if len(e.info(t, "aa").Actions) != 0 {
		t.Fatal("listed without an Actor")
	}
}

func TestRouterServesOnlyGetsAndFollowsReconfiguration(t *testing.T) {
	version := "v1"
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	f.RoutesFn = func(r plugin.Routes) {
		v := version
		r.Get("/vms/{vmid}", func(w http.ResponseWriter, req *http.Request) {
			_, _ = w.Write([]byte(v + ":" + req.PathValue("vmid")))
		}, plugin.RouteSpec{Summary: "one vm", Scope: "read"})
		r.Get("/{$}", func(w http.ResponseWriter, req *http.Request) { _, _ = w.Write([]byte(v + ":root")) }, plugin.RouteSpec{})
	}
	e := start(t, f.Factory())
	h, ok := e.sup.Router("aa")
	if !ok {
		t.Fatal("no router")
	}
	get := func(method, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code, rec.Body.String()
	}
	for _, c := range []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/api/v1/plugins/aa/vms/101", 200, "v1:101"},
		{"HEAD", "/api/v1/plugins/aa/vms/101", 200, "v1:101"}, // the recorder does not drop a HEAD body; net/http does
		{"GET", "/api/v1/plugins/aa/", 200, "v1:root"},
		{"POST", "/api/v1/plugins/aa/vms/101", 404, ""},
		{"DELETE", "/api/v1/plugins/aa/vms/101", 404, ""},
		{"GET", "/api/v1/plugins/aa/nope", 404, ""},
		{"GET", "/api/v1/plugins/aa", 404, ""},
		{"GET", "/api/v1/plugins/aab/vms/1", 404, ""},
		{"GET", "/elsewhere", 404, ""},
	} {
		code, body := get(c.method, c.path)
		if code != c.code || (c.code == 200 && body != c.body) {
			t.Errorf("%s %s = %d %q, want %d %q", c.method, c.path, code, body, c.code, c.body)
		}
		if c.code == 404 && !strings.Contains(body, `"code":"not_found"`) && c.method != "HEAD" {
			t.Errorf("%s %s: 404 body %q is not the error envelope", c.method, c.path, body)
		}
	}
	infos := e.sup.RouteInfos("aa")
	if len(infos) != 2 || infos[0].Pattern != "/vms/{vmid}" || infos[0].Spec.Summary != "one vm" {
		t.Fatalf("RouteInfos = %+v", infos)
	}
	// The same handler serves the restarted instance's routes.
	version = "v2"
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "xx"}})
	e.reloadUntil(t, "restart", func() bool { return f.Starts() == 2 })
	eventually(t, "the new routes", func() bool { _, body := get("GET", "/api/v1/plugins/aa/vms/7"); return body == "v2:7" })
	// A stopped instance has no router.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "stop", func() bool { _, ok := e.sup.Router("aa"); return !ok })
	if code, _ := get("GET", "/api/v1/plugins/aa/vms/7"); code != 404 {
		t.Fatalf("a stopped instance answered %d", code)
	}
	if _, ok := e.sup.Router("ghost"); ok {
		t.Fatal("router for an unknown instance")
	}
}

func TestInvalidRoutePatternIsAStartFailure(t *testing.T) {
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) { r.Get("/x/{", func(http.ResponseWriter, *http.Request) {}, plugin.RouteSpec{}) }
	e := start(t, f.Factory())
	if st, msg := e.state(t, "aa"); st != model.PluginError || msg != "failed to start (see the dash log)" {
		t.Fatalf("aa = %s %q", st, msg)
	}
	if f.Stops() != 1 {
		t.Fatalf("what Start launched was not stopped: %d stops", f.Stops())
	}
}

func TestSeriesOfRunningInstancesOnly(t *testing.T) {
	expr := func(plugin.SeriesArgs) (string, error) { return "up", nil }
	a := plugintest.New("aa", func(f *plugintest.Fake) { f.M.Series = []plugin.SeriesSpec{{Name: "vm.cpu", Expr: expr}} })
	e := start(t, a.Factory())
	sp, id, ok := e.sup.Series("vm.cpu")
	if !ok || id != "aa" {
		t.Fatalf("Series = %v %v", id, ok)
	}
	if q, _ := sp.Expr(plugin.SeriesArgs{}); q != "up" {
		t.Fatalf("expr = %q", q)
	}
	if _, _, ok := e.sup.Series("nope"); ok {
		t.Fatal("found an unknown series")
	}
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.reloadUntil(t, "stop", func() bool { _, _, ok := e.sup.Series("vm.cpu"); return !ok })

	dup := plugintest.New("bb", func(f *plugintest.Fake) { f.M.Series = []plugin.SeriesSpec{{Name: "vm.cpu", Expr: expr}} })
	if err := plugin.NewSupervisor(plugin.Options{}).Add(a.Factory(), dup.Factory()); err == nil {
		t.Fatal("the same series from two plugins was accepted")
	}
	noExpr := plugintest.New("cc", func(f *plugintest.Fake) { f.M.Series = []plugin.SeriesSpec{{Name: "xx"}} })
	if err := plugin.NewSupervisor(plugin.Options{}).Add(noExpr.Factory()); err == nil {
		t.Fatal("a series without an Expr was accepted")
	}
}

func TestReadyWaitsForRequiredPluginsUntilTimeout(t *testing.T) {
	var buf lockedBuffer
	var mu sync.Mutex
	st := plugin.StateStarting
	slow := plugintest.New("slow")
	slow.StatusFn = func() plugin.Status { mu.Lock(); defer mu.Unlock(); return plugin.Status{State: st} }
	off := plugintest.New("off")
	now := time.Unix(1000, 0)
	var nmu sync.Mutex
	clock := func() time.Time { nmu.Lock(); defer nmu.Unlock(); return now }
	e := newEnv(t, func(o *plugin.Options) {
		o.ReadyRequires = []plugin.ID{"slow", "off", "unknown"}
		o.ReadyTimeout = time.Minute
		o.Now = clock
		o.Log = slog.New(slog.NewTextHandler(&buf, nil))
	}, slow.Factory(), off.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "off", Enabled: new(false)})
	if e.sup.Ready() {
		t.Fatal("ready before Run")
	}
	e.run(t)
	if e.sup.Ready() {
		t.Fatal("ready while a required plugin is starting")
	}
	mu.Lock()
	st = plugin.StateHealthy
	mu.Unlock()
	if !e.sup.Ready() {
		t.Fatal("not ready once the required plugin left starting")
	}
	mu.Lock()
	st = plugin.StateStarting
	mu.Unlock()
	nmu.Lock()
	now = now.Add(59 * time.Second)
	nmu.Unlock()
	if e.sup.Ready() {
		t.Fatal("ready before the timeout")
	}
	nmu.Lock()
	now = now.Add(2 * time.Second)
	nmu.Unlock()
	if !e.sup.Ready() || !e.sup.Ready() {
		t.Fatal("not ready after the timeout")
	}
	if !strings.Contains(buf.String(), "plugins=[slow]") || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("the timeout did not warn about the plugin:\n%s", buf.String())
	}
}

func TestReadyWithAnErrorPlugin(t *testing.T) {
	bad := plugintest.New("bad")
	bad.StartFn = func(context.Context, plugin.Host, any) error { return errors.New("no") }
	e := start(t, bad.Factory(), plugintest.New("ok", plugintest.Status(plugin.StateError, "down")).Factory())
	if !e.sup.Ready() {
		t.Fatal("a plugin in error makes dash unready")
	}
}

func TestReloadNeverBlocksAndCoalesces(t *testing.T) {
	s := plugin.NewSupervisor(plugin.Options{})
	done := make(chan struct{})
	go func() {
		for range 1000 {
			s.Reload()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Reload blocked with no loop running")
	}
}

func TestShutdownStopsWithinTheContext(t *testing.T) {
	f := plugintest.New("aa")
	var got time.Time
	f.StopFn = func(ctx context.Context) error { got, _ = ctx.Deadline(); return nil }
	e := newEnv(t, nil, f.Factory())
	if err := e.sup.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	e.sup.Shutdown(ctx)
	dl, _ := ctx.Deadline()
	if got.IsZero() || got.After(dl.Add(time.Millisecond)) {
		t.Fatalf("Stop deadline %v is not within the Shutdown context %v", got, dl)
	}
	e.sup.Reload() // after Shutdown nothing starts again
	if f.Starts() != 1 {
		t.Fatal("restarted after Shutdown")
	}
	if st, _ := e.state(t, "aa"); st != model.PluginUnavailable {
		t.Fatalf("state after Shutdown = %s", st)
	}
}

func TestStartReceivesAnIndependentConfigCopy(t *testing.T) {
	f := plugintest.New("aa", plugintest.Config(cfgOf))
	f.StartFn = func(_ context.Context, _ plugin.Host, c any) error {
		c.(*cfg).URL = "mutated by the plugin"
		return nil
	}
	e := newEnv(t, nil, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", Config: &cfg{URL: "xx"}})
	e.run(t)
	e.sup.Reload()
	time.Sleep(50 * time.Millisecond)
	if f.Starts() != 1 {
		t.Fatalf("a plugin changing its config copy caused %d starts", f.Starts())
	}
}

func TestHostHTTPClientWarnsOnInsecureSkipVerify(t *testing.T) {
	var buf lockedBuffer
	var h plugin.Host
	f := plugintest.New("aa")
	f.StartFn = func(_ context.Context, host plugin.Host, _ any) error { h = host; return nil }
	newEnv(t, func(o *plugin.Options) { o.Log = slog.New(slog.NewTextHandler(&buf, nil)) }, f.Factory()).run(t)
	if _, err := h.HTTPClient(plugin.HTTPConfig{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "verification is disabled") {
		t.Fatal("warned without InsecureSkipVerify")
	}
	if _, err := h.HTTPClient(plugin.HTTPConfig{InsecureSkipVerify: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "verification is disabled") || !strings.Contains(buf.String(), "plugin=aa") {
		t.Fatalf("no WARN naming the plugin:\n%s", buf.String())
	}
}
