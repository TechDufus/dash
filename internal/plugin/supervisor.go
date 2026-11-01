package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// Options configure a Supervisor. Only Publisher and Settings are needed in practice; the zero
// value of the rest is production-ready.
type Options struct {
	Publisher     Publisher
	Settings      Settings
	ReadyRequires []ID          // server.readyRequires
	ReadyTimeout  time.Duration // server.readyTimeout; default 60s
	StopTimeout   time.Duration // default 10s (design B.6)
	StatusEvery   time.Duration // default 1s
	// Transport wraps the RoundTripper of every client Host.HTTPClient builds, after the dial
	// guard and the credentials, so what it records (the upstream metrics) is the real request.
	// nil leaves the transport as it is.
	Transport func(id ID, rt http.RoundTripper) http.RoundTripper
	// ScrubLog is Host.ScrubLog: it removes every secret the core knows from a log line. nil
	// returns the line unchanged.
	ScrubLog func(string) string
	// Demo makes the supervisor run the demo twin (the Demo interface) of every plugin that has
	// one, with its actions enabled, and leaves the rest disabled (design 1.9).
	Demo bool
	Now  func() time.Time
	Log  *slog.Logger
}

const (
	defaultReadyTimeout = 60 * time.Second
	defaultStopTimeout  = 10 * time.Second
	defaultStatusEvery  = time.Second

	msgDisabled      = "disabled"
	msgNotConfigured = "not configured"
	msgInvalidConfig = "invalid configuration"
	msgTypeMismatch  = "configuration type mismatch"
	msgStartFailed   = "failed to start (see the dash log)"
	msgInvalidStatus = "invalid status"
)

// instance is one registered plugin. Everything above mu is fixed at Add; everything below is
// guarded by mu. The lifecycle operations (start, stop, reconcile) are serialised by
// Supervisor.lifeMu, so mu only orders them against the readers and against Host.Publish, which
// holds the read lock for the whole call to the Publisher: marking an instance not live (the
// write lock) therefore waits for a Publish in flight, and nothing is published after it.
type instance struct {
	id          ID
	index       int
	factory     Factory
	man         Manifest
	deps        []ID // Requires ∪ Uses, in declaration order, without duplicates
	cfgType     reflect.Type
	restartOnly bool
	hasTwin     bool       // the plugin implements Demo
	cfgZero     func() any // Manifest.Config, nil when the plugin has no configuration

	mu          sync.RWMutex
	probe       Plugin // the plugin Add built: it answers Configured and Validate, and is the first one started
	probeUsed   bool   // the probe was started; later starts call the factory
	p           Plugin // what is running (or was started last)
	live        bool   // Publish and Go are accepted: from just before Start until just before Stop
	running     bool   // Start returned nil and the instance was not stopped since
	gen         uint64 // bumped on every start; a Host made by Start only acts for its own generation
	cancel      context.CancelFunc
	cfg         InstanceConfig // the config the instance was last evaluated with
	state       State          // the state of an instance that is not running
	msg         string
	panicMsg    string // "panic in <name>" after a recovered panic of the running plugin, until the next start
	failedStart bool   // Start failed with cfg; it is tried again only when cfg or a dependency changes
	evaluated   bool   // a reconcile has decided this instance once
	routes      *http.ServeMux
	loggedState string

	statusPanicked atomic.Bool
}

func (in *instance) isLive() bool {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return in.live
}

func (in *instance) isRunning() bool {
	in.mu.RLock()
	defer in.mu.RUnlock()
	return in.running
}

// Supervisor registers plugins and runs their lifecycle (design A.4–A.6).
type Supervisor struct {
	opt Options

	mu        sync.RWMutex // guards the registry below
	instances []*instance  // display order
	order     []*instance  // start order: dependencies first
	byID      map[ID]*instance
	ran       bool

	lifeMu   sync.Mutex // serialises start, stop, reconcile and shutdown
	baseCtx  context.Context
	down     bool
	unknown  map[ID]bool // settings entries without a factory, logged once
	kickCh   chan struct{}
	runStart time.Time

	started   atomic.Bool // Run's first reconcile returned: every Start has
	readyWarn sync.Once
	avail     map[ID]bool // availability() after the last reconcile; only reconcile and loop touch it

	pubMu         sync.Mutex
	lastPublished []model.PluginInfo
	published     bool
	shutdown      atomic.Bool
	prevVisible   map[ID]bool // Visible(state) of each row in the last core.plugins event; guarded by pubMu
}

// NewSupervisor makes a Supervisor. Register plugins with Add, then call Run.
func NewSupervisor(o Options) *Supervisor {
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = defaultReadyTimeout
	}
	if o.StopTimeout <= 0 {
		o.StopTimeout = defaultStopTimeout
	}
	if o.StatusEvery <= 0 {
		o.StatusEvery = defaultStatusEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Supervisor{opt: o, byID: map[ID]*instance{}, unknown: map[ID]bool{}, kickCh: make(chan struct{}, 1)}
}

func (s *Supervisor) log() *slog.Logger {
	if s.opt.Log != nil {
		return s.opt.Log
	}
	return slog.Default()
}

// Add registers factories in display order. It calls Manifest() once on a fresh instance and
// rejects: an invalid or reserved ID, a duplicate type ("second instance of type X: A and B"),
// invalid or duplicate topic names, an action without a Name or with a Name that is not
// [a-z][a-z0-9_.]*, a Requires/Uses cycle, a Manifest.Config that returns a non-pointer. It must
// be called before Run, and registers nothing when it fails. Unknown Requires/Uses ids are not
// an error (that is "blocked").
func (s *Supervisor) Add(f ...Factory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ran {
		return errors.New("plugin: Add after Run")
	}
	next := slices.Clone(s.instances)
	byID := make(map[ID]*instance, len(s.byID)+len(f))
	for id, in := range s.byID {
		byID[id] = in
	}
	legacy, series := map[string]ID{}, map[string]ID{}
	for _, in := range next {
		for _, t := range in.man.Topics {
			if t.Legacy != "" {
				legacy[t.Legacy] = in.id
			}
		}
		for _, sp := range in.man.Series {
			series[sp.Name] = in.id
		}
	}
	for _, fn := range f {
		if fn == nil {
			return errors.New("plugin: nil factory")
		}
		p := fn()
		if p == nil {
			return errors.New("plugin: factory returned nil")
		}
		in, err := newInstance(fn, p, len(next))
		if err != nil {
			return err
		}
		if prev, dup := byID[in.id]; dup {
			return fmt.Errorf("plugin: second instance of type %q: factory %d (%q) and factory %d (%q); dash runs one instance per plugin type", in.id, prev.index+1, prev.man.Name, in.index+1, in.man.Name)
		}
		for _, t := range in.man.Topics {
			if t.Legacy == "" {
				continue
			}
			if other, dup := legacy[t.Legacy]; dup {
				return fmt.Errorf("plugin %s: legacy topic name %q is already used by %s", in.id, t.Legacy, other)
			}
			legacy[t.Legacy] = in.id
		}
		for _, sp := range in.man.Series {
			if other, dup := series[sp.Name]; dup {
				return fmt.Errorf("plugin %s: series %q is already provided by %s", in.id, sp.Name, other)
			}
			series[sp.Name] = in.id
		}
		byID[in.id] = in
		next = append(next, in)
	}
	order, err := startOrder(next, byID)
	if err != nil {
		return err
	}
	s.instances, s.byID, s.order = next, byID, order
	return nil
}

func newInstance(f Factory, p Plugin, index int) (*instance, error) {
	m := p.Manifest()
	if !m.ID.Valid() {
		if m.ID == Core {
			return nil, fmt.Errorf("plugin: id %q is reserved for the core topics", m.ID)
		}
		return nil, fmt.Errorf("plugin: invalid id %q: want [a-z][a-z0-9]{1,31}", m.ID)
	}
	in := &instance{id: m.ID, index: index, factory: f, probe: p, state: StateStarting}
	if _, ok := p.(RestartOnly); ok {
		in.restartOnly = true
	}
	if _, ok := p.(Demo); ok {
		in.hasTwin = true
	}
	if m.Config != nil {
		c := m.Config()
		if v := reflect.ValueOf(c); v.Kind() != reflect.Pointer || v.IsNil() {
			return nil, fmt.Errorf("plugin %s: Manifest.Config must return a non-nil pointer to a config struct, got %T", m.ID, c)
		}
		in.cfgType, in.cfgZero = reflect.TypeOf(c), m.Config
	}
	topics := map[string]bool{}
	for _, t := range m.Topics {
		if !topicRE.MatchString(t.Name) {
			return nil, fmt.Errorf("plugin %s: invalid topic name %q: want [a-z][a-z0-9_]{0,31}", m.ID, t.Name)
		}
		if topics[t.Name] {
			return nil, fmt.Errorf("plugin %s: duplicate topic %q", m.ID, t.Name)
		}
		topics[t.Name] = true
		if t.Legacy != "" && !topicRE.MatchString(t.Legacy) {
			return nil, fmt.Errorf("plugin %s: invalid legacy topic name %q: want [a-z][a-z0-9_]{0,31}", m.ID, t.Legacy)
		}
	}
	m.Topics = slices.Clone(m.Topics)
	legacy := map[string]bool{}
	for _, t := range m.Topics {
		if t.Legacy != "" {
			if legacy[t.Legacy] {
				return nil, fmt.Errorf("plugin %s: duplicate legacy topic name %q", m.ID, t.Legacy)
			}
			legacy[t.Legacy] = true
		}
	}
	actions := map[string]bool{}
	m.Actions = slices.Clone(m.Actions)
	for i := range m.Actions {
		a := &m.Actions[i]
		if a.Name == "" || !actionRE.MatchString(a.Name) {
			return nil, fmt.Errorf("plugin %s: invalid action name %q: want [a-z][a-z0-9_.]*", m.ID, a.Name)
		}
		if actions[a.Name] {
			return nil, fmt.Errorf("plugin %s: duplicate action %q", m.ID, a.Name)
		}
		actions[a.Name] = true
		switch a.MinRole {
		case "":
			a.MinRole = auth.RoleAdmin
		case auth.RoleAdmin, auth.RoleViewer:
		default:
			return nil, fmt.Errorf("plugin %s: action %s has unknown role %q", m.ID, a.Name, a.MinRole)
		}
		if a.Confirm == "" {
			a.Confirm = ConfirmNone
		}
	}
	series := map[string]bool{}
	for _, sp := range m.Series {
		if sp.Name == "" || sp.Expr == nil {
			return nil, fmt.Errorf("plugin %s: a series needs a name and an Expr", m.ID)
		}
		if series[sp.Name] {
			return nil, fmt.Errorf("plugin %s: duplicate series %q", m.ID, sp.Name)
		}
		series[sp.Name] = true
	}
	m.Series = slices.Clone(m.Series)
	m.UI.Nav = slices.Clone(m.UI.Nav)
	var err error
	if m.UI.Generic, err = cloneGeneric(m.ID, m.UI.Generic, topics); err != nil {
		return nil, err
	}
	for _, d := range slices.Concat(m.Requires, m.Uses) {
		if !slices.Contains(in.deps, d) {
			in.deps = append(in.deps, d)
		}
	}
	in.man = m
	return in, nil
}

// genericFormats are the cell and stat formats the SPA knows.
var genericFormats = map[string]bool{"text": true, "number": true, "bytes": true, "percent": true, "time": true, "health": true}

// cloneGeneric checks the generic views of plugin id against the topics it declares (by
// TopicSpec.Name) and returns a copy the plugin can no longer change.
func cloneGeneric(id ID, views []model.GenericView, topics map[string]bool) ([]model.GenericView, error) {
	out := make([]model.GenericView, len(views))
	ids := map[string]bool{}
	for i, v := range views {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("plugin %s: generic view %q: %s", id, v.ID, fmt.Sprintf(format, a...))
		}
		if v.ID == "" {
			return nil, fmt.Errorf("plugin %s: generic view %d has no id", id, i+1)
		}
		if ids[v.ID] {
			return nil, bad("duplicate id")
		}
		ids[v.ID] = true
		switch v.Kind {
		case model.GenericTable, model.GenericList:
			if len(v.Columns) == 0 {
				return nil, bad("a %s needs columns", v.Kind)
			}
		case model.GenericStat:
		default:
			return nil, bad("unknown kind %q: want table, list or stat", v.Kind)
		}
		if !topics[v.Topic] {
			return nil, bad("topic %q is not declared by the plugin", v.Topic)
		}
		if !validPointer(v.Path) {
			return nil, bad("path %q is not an RFC 6901 JSON pointer", v.Path)
		}
		if v.Format != "" && !genericFormats[v.Format] {
			return nil, bad("unknown format %q", v.Format)
		}
		keys := map[string]bool{}
		for _, c := range v.Columns {
			if c.Key == "" {
				return nil, bad("a column has no key")
			}
			if keys[c.Key] {
				return nil, bad("duplicate column key %q", c.Key)
			}
			keys[c.Key] = true
			if c.Format != "" && !genericFormats[c.Format] {
				return nil, bad("column %q has unknown format %q", c.Key, c.Format)
			}
		}
		// Never nil, so a stat view's columns reach the wire as [], not null.
		v.Columns = append([]model.GenericColumn{}, v.Columns...)
		out[i] = v
	}
	return out, nil
}

// validPointer reports whether p is an RFC 6901 JSON pointer: empty, or '/'-separated tokens in
// which '~' is only ever the start of "~0" or "~1".
func validPointer(p string) bool {
	if p == "" {
		return true
	}
	if p[0] != '/' {
		return false
	}
	for i := range len(p) {
		if p[i] == '~' && (i+1 == len(p) || (p[i+1] != '0' && p[i+1] != '1')) {
			return false
		}
	}
	return true
}

// startOrder sorts instances so that every registered dependency comes first, ties by
// registration order, and reports a dependency cycle.
func startOrder(all []*instance, byID map[ID]*instance) ([]*instance, error) {
	const (
		unseen = iota
		visiting
		done
	)
	mark := make(map[*instance]int, len(all))
	var stack []ID
	var visit func(in *instance) error
	visit = func(in *instance) error {
		switch mark[in] {
		case done:
			return nil
		case visiting:
			cycle := append(slices.Clone(stack[slices.Index(stack, in.id):]), in.id)
			names := make([]string, len(cycle))
			for i, id := range cycle {
				names[i] = string(id)
			}
			return fmt.Errorf("plugin: dependency cycle: %s", joinArrow(names))
		}
		mark[in] = visiting
		stack = append(stack, in.id)
		for _, d := range in.deps {
			if dep := byID[d]; dep != nil {
				if err := visit(dep); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		mark[in] = done
		return nil
	}
	for _, in := range all {
		if err := visit(in); err != nil {
			return nil, err
		}
	}
	placed := make(map[*instance]bool, len(all))
	out := make([]*instance, 0, len(all))
	for len(out) < len(all) {
		for _, in := range all {
			if placed[in] {
				continue
			}
			ready := true
			for _, d := range in.deps {
				if dep := byID[d]; dep != nil && !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				placed[in] = true
				out = append(out, in)
				break
			}
		}
	}
	return out, nil
}

func joinArrow(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += " -> "
		}
		out += n
	}
	return out
}

// Run evaluates every instance and starts the runnable ones in dependency order, returning once
// every Start has returned. The status loop and the reconfigure loop it launches are bound to ctx.
func (s *Supervisor) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.ran {
		s.mu.Unlock()
		return errors.New("plugin: Run called twice")
	}
	s.ran = true
	s.mu.Unlock()

	s.lifeMu.Lock()
	if s.down {
		s.lifeMu.Unlock()
		return errors.New("plugin: Run after Shutdown")
	}
	s.baseCtx, s.runStart = ctx, s.opt.Now()
	s.reconcile()
	s.started.Store(true)
	s.lifeMu.Unlock()

	s.publishInfos(true)
	go s.loop(ctx)
	return nil
}

// Reload re-reads Settings and applies the difference. It never blocks (it signals a
// coalescing channel); main calls it from the config reloader's subscriber.
func (s *Supervisor) Reload() {
	select {
	case s.kickCh <- struct{}{}:
	default:
	}
}

// loop polls every instance's status and applies queued reloads until ctx is done.
func (s *Supervisor) loop(ctx context.Context) {
	t := time.NewTicker(s.opt.StatusEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kickCh:
			s.lifeMu.Lock()
			s.reconcile()
			s.lifeMu.Unlock()
			s.publishInfos(false)
		case <-t.C:
			s.publishInfos(false)
			if !s.shutdown.Load() && !maps.Equal(s.avail, s.availability()) {
				s.Reload()
			}
		}
	}
}

// availability says, for every plugin that others require, whether it runs and reports
// "unavailable". The dependents of such a plugin are blocked, so a change of this map between
// two reconciles is a reason for another one.
func (s *Supervisor) availability() map[ID]bool {
	insts := s.registry()
	required := map[ID]bool{}
	for _, in := range insts {
		for _, r := range in.man.Requires {
			required[r] = true
		}
	}
	out := map[ID]bool{}
	for _, in := range insts {
		if !required[in.id] {
			continue
		}
		st, _ := s.liveState(in)
		out[in.id] = in.isRunning() && st == StateUnavailable
	}
	return out
}

// plan is what the configuration says about one instance, before its dependencies are looked at.
type plan struct {
	ic       InstanceConfig
	state    State // when not runnable
	msg      string
	runnable bool
}

// configs is Settings.Instances matched to the registry: one entry per instance, Type and Config
// filled in, Config nil for a plugin that has none. Entries without a factory are logged once.
func (s *Supervisor) configs() map[*instance]InstanceConfig {
	byName := map[ID]InstanceConfig{}
	if s.opt.Settings != nil {
		for _, ic := range s.opt.Settings.Instances() {
			byName[ic.Name] = ic
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for name := range byName {
		if _, ok := s.byID[name]; !ok && !s.unknown[name] {
			s.unknown[name] = true
			s.log().Warn("configuration for an unknown plugin is ignored", "plugin", name)
		}
	}
	out := make(map[*instance]InstanceConfig, len(s.instances))
	for _, in := range s.instances {
		ic, ok := byName[in.id]
		if !ok {
			ic = InstanceConfig{}
		}
		ic.Name = in.id
		if ic.Type == "" {
			ic.Type = in.id
		}
		if in.cfgZero == nil {
			ic.Config = nil
		} else if ic.Config == nil {
			ic.Config = in.cfgZero()
		}
		if len(ic.DenyNamespaces) == 0 {
			ic.DenyNamespaces = nil
		}
		if len(ic.ReadDenyNamespaces) == 0 {
			ic.ReadDenyNamespaces = nil
		}
		if s.demoTwin(in) {
			ic.Enabled, ic.ActionsEnabled = new(true), true
		}
		out[in] = ic
	}
	return out
}

// protect runs fn and reports a panic in it.
func protect(fn func()) (rec any, stack []byte) {
	defer func() {
		if r := recover(); r != nil {
			rec, stack = r, debug.Stack()
		}
	}()
	fn()
	return nil, nil
}

// evaluate applies the first five rows of the state table to an instance's own configuration.
func (s *Supervisor) evaluate(in *instance, ic InstanceConfig) plan {
	pl := plan{ic: ic}
	switch {
	case ic.Enabled != nil && !*ic.Enabled:
		pl.state, pl.msg = StateDisabled, msgDisabled
		return pl
	case ic.Type != in.id || (in.cfgType != nil && reflect.TypeOf(ic.Config) != in.cfgType):
		s.log().Error("plugin configuration has the wrong type", "plugin", in.id, "type", ic.Type)
		pl.state, pl.msg = StateError, msgTypeMismatch
		return pl
	}
	if s.demoTwin(in) { // the twin has no upstream to be configured for
		pl.runnable = true
		return pl
	}
	in.mu.RLock()
	probe := in.probe
	in.mu.RUnlock()
	if c, ok := probe.(Configured); ok {
		var configured bool
		if rec, stack := protect(func() { configured = c.Configured(ic.Config) }); rec != nil {
			s.log().Error("panic in Configured", "plugin", in.id, "panic", fmt.Sprint(rec), "stack", string(stack))
			pl.state, pl.msg = StateError, "panic in Configured"
			return pl
		}
		if !configured {
			if ic.Enabled == nil {
				pl.state, pl.msg = StateDisabled, msgNotConfigured
			} else {
				pl.state, pl.msg = StateUnconfigured, msgNotConfigured
			}
			return pl
		}
	}
	if v, ok := probe.(Validator); ok {
		var err error
		if rec, stack := protect(func() { err = v.Validate(ic.Config) }); rec != nil {
			s.log().Error("panic in Validate", "plugin", in.id, "panic", fmt.Sprint(rec), "stack", string(stack))
			pl.state, pl.msg = StateError, "panic in Validate"
			return pl
		}
		if err != nil {
			s.log().Warn("plugin configuration is invalid", "plugin", in.id, "error", err)
			pl.state, pl.msg = StateUnconfigured, msgInvalidConfig
			return pl
		}
	}
	pl.runnable = true
	return pl
}

// liveState is the state of an instance as it is now: the plugin's own report while it runs.
func (s *Supervisor) liveState(in *instance) (State, string) {
	in.mu.RLock()
	running, p, state, msg, pm := in.running, in.p, in.state, in.msg, in.panicMsg
	in.mu.RUnlock()
	if !running {
		return state, msg
	}
	if pm != "" {
		return StateError, pm
	}
	var st Status
	if rec, stack := protect(func() { st = p.Status() }); rec != nil {
		if in.statusPanicked.CompareAndSwap(false, true) {
			s.log().Error("panic in Status", "plugin", in.id, "panic", fmt.Sprint(rec), "stack", string(stack))
		}
		return StateError, "panic in Status"
	}
	in.statusPanicked.Store(false)
	switch st.State {
	case StateStarting, StateHealthy, StateDegraded, StateError, StateUnavailable:
		return st.State, st.Message
	}
	return StateError, msgInvalidStatus
}

// blocker says why a runnable instance cannot start now, or "" (state table row 6).
func (s *Supervisor) blocker(in *instance) string {
	for _, r := range in.man.Requires {
		dep := s.lookup(r)
		if dep == nil {
			return fmt.Sprintf("requires %s (not installed)", r)
		}
		st, msg := s.liveState(dep)
		switch {
		case !dep.isRunning() && st == StateDisabled:
			return fmt.Sprintf("requires %s (disabled)", r)
		case !dep.isRunning():
			return fmt.Sprintf("requires %s (%s)", r, st)
		case st == StateUnavailable:
			return fmt.Sprintf("requires %s (unavailable: %s)", r, msg)
		}
	}
	return ""
}

// reconcile brings the running instances in line with Settings. The caller holds lifeMu.
//
// It first stops, dependents before dependencies, every running instance whose configuration
// changed, that is no longer runnable (disabled, unconfigured, blocked), or that depends on
// an instance being stopped. Then it starts, dependencies first, every instance that is
// runnable and whose requirements are met. A RestartOnly instance is decided once, at Run, and
// never touched again.
func (s *Supervisor) reconcile() {
	if s.down {
		return
	}
	cfgs := s.configs()
	s.mu.RLock()
	order := slices.Clone(s.order)
	byID := s.byID
	s.mu.RUnlock()

	plans := make(map[*instance]plan, len(order))
	for _, in := range order {
		plans[in] = s.evaluate(in, cfgs[in])
	}

	stopping := map[*instance]string{}
	for _, in := range order {
		if !in.isRunning() || in.restartOnly {
			continue
		}
		pl := plans[in]
		in.mu.RLock()
		applied := in.cfg
		in.mu.RUnlock()
		switch {
		case !pl.runnable:
			stopping[in] = pl.msg
		case !reflect.DeepEqual(applied, pl.ic):
			stopping[in] = "configuration changed"
		default:
			for _, d := range in.deps {
				if dep := byID[d]; dep != nil {
					if _, ok := stopping[dep]; ok {
						stopping[in] = "dependency " + string(d) + " is stopping"
						break
					}
				}
			}
			if _, ok := stopping[in]; !ok {
				if b := s.blocker(in); b != "" {
					stopping[in] = b
				}
			}
		}
	}
	for i := len(order) - 1; i >= 0; i-- {
		if reason, ok := stopping[order[i]]; ok {
			s.stop(order[i], context.Background(), reason, true)
		}
	}

	started := map[*instance]bool{}
	for _, in := range order {
		in.mu.RLock()
		running, frozen, failed, applied := in.running, in.restartOnly && in.evaluated, in.failedStart, in.cfg
		in.mu.RUnlock()
		if frozen {
			continue
		}
		if running {
			in.setEvaluated()
			continue
		}
		pl := plans[in]
		if !pl.runnable {
			s.setNotRunning(in, pl.ic, pl.state, pl.msg)
			continue
		}
		if b := s.blocker(in); b != "" {
			s.setNotRunning(in, pl.ic, StateBlocked, b)
			continue
		}
		if failed && reflect.DeepEqual(applied, pl.ic) {
			retry := false
			for _, d := range in.deps {
				retry = retry || (byID[d] != nil && started[byID[d]])
			}
			if !retry {
				in.setEvaluated()
				continue
			}
		}
		if s.start(in, pl.ic) {
			started[in] = true
		}
	}
	s.avail = s.availability()
}

func (in *instance) setEvaluated() {
	in.mu.Lock()
	in.evaluated = true
	in.mu.Unlock()
}

// setNotRunning records the state of an instance that is not (and does not become) running.
func (s *Supervisor) setNotRunning(in *instance, ic InstanceConfig, st State, msg string) {
	in.mu.Lock()
	in.state, in.msg, in.cfg, in.evaluated, in.failedStart = st, msg, ic, true, false
	changed := in.loggedState != string(st)+"/"+msg
	in.loggedState = string(st) + "/" + msg
	in.mu.Unlock()
	if changed {
		s.log().Info("plugin not running", "plugin", in.id, "state", st, "message", msg)
	}
}

// start runs the plugin's Start. It reports whether the instance is running afterwards.
func (s *Supervisor) start(in *instance, ic InstanceConfig) bool {
	in.mu.Lock()
	p := in.probe
	if in.probeUsed {
		p = in.factory()
	}
	in.probeUsed = true
	p = s.twinOf(p)
	in.gen++
	gen := in.gen
	ctx, cancel := context.WithCancel(s.baseCtx)
	in.p, in.cancel = p, cancel
	in.cfg, in.evaluated = ic, true
	in.state, in.msg, in.panicMsg, in.failedStart = StateStarting, "", "", false
	in.live = true // a plugin publishes from Start or from the goroutines it launches there
	in.mu.Unlock()

	// The plugin gets its own copy of the struct, so that nothing it does to it can make the
	// next comparison with Settings see a change.
	var cfg any
	if ic.Config != nil {
		c := reflect.New(reflect.TypeOf(ic.Config).Elem())
		c.Elem().Set(reflect.ValueOf(ic.Config).Elem())
		cfg = c.Interface()
	}
	var err error
	rec, stack := protect(func() { err = p.Start(ctx, &host{s: s, in: in, gen: gen}, cfg) })
	if rec == nil && err == nil {
		var mux *http.ServeMux
		if rt, ok := p.(Router); ok {
			mux, err = buildMux(in, rt)
		}
		if err == nil {
			in.mu.Lock()
			in.running, in.routes = true, mux
			in.state, in.msg, in.loggedState = StateStarting, "", ""
			in.mu.Unlock()
			s.log().Info("plugin started", "plugin", in.id, "version", in.man.Version)
			return true
		}
		// Started but unusable: stop what Start launched.
		s.callStop(in.id, p, context.Background())
	}
	if rec != nil {
		s.log().Error("panic in Start", "plugin", in.id, "panic", fmt.Sprint(rec), "stack", string(stack))
	} else {
		s.log().Error("plugin failed to start", "plugin", in.id, "error", err)
	}
	cancel()
	in.mu.Lock()
	in.live, in.running = false, false
	in.state, in.msg, in.failedStart = StateError, msgStartFailed, true
	in.loggedState = string(StateError) + "/" + msgStartFailed
	in.mu.Unlock()
	s.removeTopics(in)
	return false
}

// buildMux registers a Router's routes, each behind the instance's guard (see RouteGuard). A
// pattern ServeMux rejects is a start failure.
func buildMux(in *instance, r Router) (mux *http.ServeMux, err error) {
	mux = http.NewServeMux()
	mux.HandleFunc("/", notFound)
	defer func() {
		if rec := recover(); rec != nil {
			mux, err = nil, fmt.Errorf("invalid route: %v", rec)
		}
	}()
	for _, ri := range CollectRoutes(r) {
		mux.HandleFunc("GET "+ri.Pattern, routeHandler(in, ri))
	}
	return mux, nil
}

// routeHandler serves one route: through the guard of the request when it has one, which applies
// the core's policy and runs the handler; without one a GET route runs as registered and a
// stream route, which the core serves, is refused.
func routeHandler(in *instance, ri RouteInfo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g := guardFrom(r.Context()).guard
		if g.Serve == nil {
			if ri.Stream != nil {
				WriteError(w, http.StatusInternalServerError, model.ErrCodeInternal, "stream routes are not served here")
				return
			}
			ri.Handler(w, r)
			return
		}
		in.mu.RLock()
		deny := slices.Clone(in.cfg.ReadDenyNamespaces)
		in.mu.RUnlock()
		g.Serve(w, r, MountedRoute{Instance: in.id, Info: ri, ReadDenyNamespaces: deny}, ri.Handler)
	}
}

// twinOf is the plugin an instance runs: in demo mode the twin of a plugin that has one, else p.
func (s *Supervisor) twinOf(p Plugin) Plugin {
	if s.opt.Demo {
		if d, ok := p.(Demo); ok {
			return d.Demo()
		}
	}
	return p
}

// demoTwin reports whether the instance runs its twin: demo mode, and the plugin has one. Such an
// instance is enabled and configured whatever the configuration says, with its actions mounted.
func (s *Supervisor) demoTwin(in *instance) bool { return s.opt.Demo && in.hasTwin }

// stop ends an instance: no more publishes, its context cancelled, Stop called within
// StopTimeout (a Stop that outlives it is abandoned), then its topics removed, so that a viewer
// is not shown the data of a plugin that no longer runs.
func (s *Supervisor) stop(in *instance, parent context.Context, reason string, removeTopics bool) {
	in.mu.Lock()
	p, cancel := in.p, in.cancel
	was := in.running
	in.live, in.running, in.routes = false, false, nil
	in.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if was && p != nil {
		s.callStop(in.id, p, parent)
		s.log().Info("plugin stopped", "plugin", in.id, "reason", reason)
	}
	if removeTopics {
		s.removeTopics(in)
	}
}

func (s *Supervisor) callStop(id ID, p Plugin, parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, s.opt.StopTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var err error
		if rec, stack := protect(func() { err = p.Stop(ctx) }); rec != nil {
			s.log().Error("panic in Stop", "plugin", id, "panic", fmt.Sprint(rec), "stack", string(stack))
		} else if err != nil {
			s.log().Warn("plugin stop failed", "plugin", id, "error", err)
		}
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.log().Error("plugin did not stop in time; abandoning it", "plugin", id, "timeout", s.opt.StopTimeout)
	}
}

func (s *Supervisor) removeTopics(in *instance) {
	if s.opt.Publisher == nil {
		return
	}
	for _, t := range in.man.Topics {
		s.opt.Publisher.Remove(topicName(in.id, t))
	}
}

// topicName is the name a topic is published, listed and removed under.
func topicName(id ID, t TopicSpec) model.Topic {
	if t.Legacy != "" {
		return model.Topic(t.Legacy)
	}
	return model.Topic(string(id) + "." + t.Name)
}

// Shutdown stops every running instance in reverse order, each within StopTimeout.
func (s *Supervisor) Shutdown(ctx context.Context) {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.down {
		return
	}
	s.down = true
	s.shutdown.Store(true)
	s.mu.RLock()
	order := slices.Clone(s.order)
	s.mu.RUnlock()
	for i := len(order) - 1; i >= 0; i-- {
		in := order[i]
		s.stop(in, ctx, "shutting down", false)
		in.mu.Lock()
		in.state, in.msg = StateUnavailable, "stopped"
		in.mu.Unlock()
	}
}

// Live reports whether a change to "plugins.<id>" is applied in-process (an instance exists
// and is not RestartOnly). It is config.ReloadOptions.Live.
func (s *Supervisor) Live(section string) bool {
	const prefix = "plugins."
	if len(section) <= len(prefix) || section[:len(prefix)] != prefix {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	in := s.byID[ID(section[len(prefix):])]
	return in != nil && !in.restartOnly
}

// publishInfos publishes core.plugins when any PluginInfo changed (or force), then re-publishes
// the topics of every running instance whose row just turned visible (design A.9, rule (a)).
func (s *Supervisor) publishInfos(force bool) {
	if s.opt.Publisher == nil || s.shutdown.Load() {
		return
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	infos := s.Infos()
	if !force && s.published && reflect.DeepEqual(s.lastPublished, infos) {
		return
	}
	s.lastPublished, s.published = infos, true
	if _, err := s.opt.Publisher.Publish(model.TopicPlugins, model.PluginsData{Plugins: infos}, []model.SourceStatus{}); err != nil {
		s.log().Error("publish failed", "topic", model.TopicPlugins, "error", err)
	}
	s.resendNewlyVisible(infos)
}

// resendNewlyVisible is the transition detector of rule (a). The SPA fetches a plugin's topics
// only when it starts listening, and stops listening while the row is hidden, and the hub
// deduplicates identical publishes: a plugin that turns visible without changing its payload
// would leave its pages loading. So when a running instance's row goes from a hidden to a
// visible state, the latest event of each of its topics is sent again, after the core.plugins
// event that made the row visible. It reads and writes prevVisible under pubMu, from the infos
// that were just published; the first call (Run's) only records.
func (s *Supervisor) resendNewlyVisible(infos []model.PluginInfo) {
	next := make(map[ID]bool, len(infos))
	for _, pi := range infos {
		id := ID(pi.ID)
		vis := Visible(pi.State)
		next[id] = vis
		if was, seen := s.prevVisible[id]; !seen || was || !vis || !s.Running(id) {
			continue
		}
		for _, t := range pi.Topics {
			s.opt.Publisher.Resend(model.Topic(t))
		}
	}
	s.prevVisible = next
}

// actionRun wraps Act so that a panic in a plugin is an ActionError, not a crashed request.
func (s *Supervisor) actionRun(in *instance, a Actor, name string) func(context.Context, auth.Principal, any) (any, error) {
	return func(ctx context.Context, p auth.Principal, input any) (out any, err error) {
		defer func() {
			if r := recover(); r != nil {
				s.log().Error("panic in Act", "plugin", in.id, "action", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				out, err = nil, &ActionError{Status: http.StatusInternalServerError, Code: "internal", Message: "internal error"}
			}
		}()
		return a.Act(ctx, name, p, input)
	}
}
