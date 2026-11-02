// Package plugintest has the fakes plugin, API and topic tests share: a configurable Fake
// plugin, a Settings that tests change, a Hub that records what is published, and a
// HostProvider with every instance running.
package plugintest

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Fake is a plugin whose behaviour tests set field by field. It implements every optional
// interface, each neutral while its field is nil:
// Configured is true, Validate is nil, Service is nil (no service), Act fails, Routes registers
// nothing, and the sources contribute nothing. The zero Status is healthy.
//
// A Fake is its own factory (Factory), so a restarted instance is the same Fake: Starts and
// Stops count across restarts.
type Fake struct {
	M        plugin.Manifest
	StartFn  func(ctx context.Context, h plugin.Host, cfg any) error
	StopFn   func(ctx context.Context) error
	StatusFn func() plugin.Status

	ActionFn     func(ctx context.Context, action string, p auth.Principal, input any) (any, error)
	RoutesFn     func(r plugin.Routes)
	AttentionFn  func(now time.Time) []model.AttentionItem
	KPIFn        func() []model.KPI
	TopologyFn   func() model.Topology
	ConfiguredFn func(cfg any) bool
	ValidateFn   func(cfg any) error
	ServiceVal   any
	LinksFn      func(req plugin.LinkRequest) []model.Link
	// Hint is what ConfigHint answers: empty (no hint) unless a test sets it.
	Hint string

	starts, stops atomic.Int32
	mu            sync.Mutex
	host          plugin.Host
	cfg           any
}

// New makes a Fake named id, version "1.0.0", with no topics or config.
func New(id plugin.ID, opts ...func(*Fake)) *Fake {
	f := &Fake{M: plugin.Manifest{ID: id, Name: string(id), Version: "1.0.0"}}
	for _, o := range opts {
		o(f)
	}
	return f
}

func (f *Fake) Manifest() plugin.Manifest { return f.M }

func (f *Fake) Start(ctx context.Context, h plugin.Host, cfg any) error {
	f.starts.Add(1)
	f.mu.Lock()
	f.host, f.cfg = h, cfg
	f.mu.Unlock()
	if f.StartFn != nil {
		return f.StartFn(ctx, h, cfg)
	}
	return nil
}

func (f *Fake) Stop(ctx context.Context) error {
	f.stops.Add(1)
	if f.StopFn != nil {
		return f.StopFn(ctx)
	}
	return nil
}

func (f *Fake) Status() plugin.Status {
	if f.StatusFn != nil {
		return f.StatusFn()
	}
	return plugin.Status{State: plugin.StateHealthy}
}

func (f *Fake) Act(ctx context.Context, action string, p auth.Principal, input any) (any, error) {
	if f.ActionFn == nil {
		return nil, &plugin.ActionError{Status: http.StatusNotImplemented, Code: "not_found", Message: "no ActionFn"}
	}
	return f.ActionFn(ctx, action, p, input)
}

func (f *Fake) Routes(r plugin.Routes) {
	if f.RoutesFn != nil {
		f.RoutesFn(r)
	}
}

func (f *Fake) Attention(now time.Time) []model.AttentionItem {
	if f.AttentionFn == nil {
		return nil
	}
	return f.AttentionFn(now)
}

func (f *Fake) KPIs() []model.KPI {
	if f.KPIFn == nil {
		return nil
	}
	return f.KPIFn()
}

func (f *Fake) Topology() model.Topology {
	if f.TopologyFn == nil {
		return model.Topology{}
	}
	return f.TopologyFn()
}

func (f *Fake) Configured(cfg any) bool { return f.ConfiguredFn == nil || f.ConfiguredFn(cfg) }

func (f *Fake) ConfigHint() string { return f.Hint }

func (f *Fake) Validate(cfg any) error {
	if f.ValidateFn == nil {
		return nil
	}
	return f.ValidateFn(cfg)
}

func (f *Fake) Service() any { return f.ServiceVal }

// Starts and Stops count the calls so far.
func (f *Fake) Starts() int { return int(f.starts.Load()) }
func (f *Fake) Stops() int  { return int(f.stops.Load()) }

// Host is the Host the last Start received, nil before the first.
func (f *Fake) Host() plugin.Host {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host
}

// Config is the config the last Start received.
func (f *Fake) Config() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

// Factory is f as a plugin.Factory: every call returns f.
func (f *Fake) Factory() plugin.Factory { return func() plugin.Plugin { return f } }

// Options for New.

// Requires sets Manifest.Requires.
func Requires(ids ...plugin.ID) func(*Fake) { return func(f *Fake) { f.M.Requires = ids } }

// Uses sets Manifest.Uses.
func Uses(ids ...plugin.ID) func(*Fake) { return func(f *Fake) { f.M.Uses = ids } }

// Topics declares topics of the given names, with no payload type.
func Topics(names ...string) func(*Fake) {
	return func(f *Fake) {
		for _, n := range names {
			f.M.Topics = append(f.M.Topics, plugin.TopicSpec{Name: n})
		}
	}
}

// Typed declares a topic whose payload is the type of zero.
func Typed(name string, zero any) func(*Fake) {
	return func(f *Fake) { f.M.Topics = append(f.M.Topics, plugin.TopicSpec{Name: name, Type: zero}) }
}

// Config sets Manifest.Config to return a fresh pointer to a copy of zero's type each call: zero
// must be a pointer to a struct.
func Config(zero func() any) func(*Fake) { return func(f *Fake) { f.M.Config = zero } }

// Status sets what the plugin reports.
func Status(state plugin.State, msg string) func(*Fake) {
	return func(f *Fake) { f.StatusFn = func() plugin.Status { return plugin.Status{State: state, Message: msg} } }
}

// Settings is a plugin.Settings that tests change at will.
type Settings struct {
	mu  sync.Mutex
	ics []plugin.InstanceConfig
}

// Set replaces the entry of each ic's Name, or adds it.
func (s *Settings) Set(ics ...plugin.InstanceConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ic := range ics {
		replaced := false
		for i := range s.ics {
			if s.ics[i].Name == ic.Name {
				s.ics[i], replaced = ic, true
			}
		}
		if !replaced {
			s.ics = append(s.ics, ic)
		}
	}
}

// Remove deletes the entries of the given names.
func (s *Settings) Remove(names ...plugin.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.ics[:0:0]
	for _, ic := range s.ics {
		keep := true
		for _, n := range names {
			keep = keep && ic.Name != n
		}
		if keep {
			out = append(out, ic)
		}
	}
	s.ics = out
}

func (s *Settings) Instances() []plugin.InstanceConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]plugin.InstanceConfig(nil), s.ics...)
}

// Event is one Publish.
type Event struct {
	Topic   model.Topic
	Data    any
	Sources []model.SourceStatus
}

// Hub is a plugin.Publisher that records what it is given.
type Hub struct {
	mu      sync.Mutex
	events  []Event
	removed []model.Topic
	resent  []model.Topic
}

func (h *Hub) Publish(topic model.Topic, data any, sources []model.SourceStatus) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, Event{topic, data, sources})
	return true, nil
}

func (h *Hub) Remove(topic model.Topic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removed = append(h.removed, topic)
}

// Resend records the call and reports whether the topic has been published.
func (h *Hub) Resend(topic model.Topic) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resent = append(h.resent, topic)
	for _, e := range h.events {
		if e.Topic == topic {
			return true
		}
	}
	return false
}

// Resent is every Resend so far, in order.
func (h *Hub) Resent() []model.Topic {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.Topic(nil), h.resent...)
}

// Published is every Publish so far, in order.
func (h *Hub) Published() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.events...)
}

// Removed is every Remove so far, in order.
func (h *Hub) Removed() []model.Topic {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]model.Topic(nil), h.removed...)
}

// Last is the latest Publish of topic.
func (h *Hub) Last(topic model.Topic) (Event, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := len(h.events) - 1; i >= 0; i-- {
		if h.events[i].Topic == topic {
			return h.events[i], true
		}
	}
	return Event{}, false
}

// AllRunning is a HostProvider in which every instance runs. Publish prefixes the topic with
// "<instance>." (core: "core.") and forwards it to p. It checks nothing else.
func AllRunning(p plugin.Publisher) plugin.HostProvider { return allRunning{p} }

type allRunning struct{ p plugin.Publisher }

func (a allRunning) HostFor(id plugin.ID) plugin.Host { return runningHost{a, id} }
func (a allRunning) Running(plugin.ID) bool           { return true }

type runningHost struct {
	a  allRunning
	id plugin.ID
}

func (h runningHost) Publish(topic string, data any, sources []model.SourceStatus) {
	_, _ = h.a.p.Publish(model.Topic(string(h.id)+"."+topic), data, sources)
}

func (h runningHost) Logger() *slog.Logger { return slog.Default().With("plugin", string(h.id)) }
func (h runningHost) HTTPClient(c plugin.HTTPConfig) (*http.Client, error) {
	return &http.Client{Timeout: c.Timeout}, nil
}
func (h runningHost) Service(plugin.ID) (any, bool) { return nil, false }
func (h runningHost) ScrubLog(s string) string      { return s }
func (h runningHost) Links() plugin.LinkResolver    { return plugin.NoLinks }
func (h runningHost) Now() time.Time                { return time.Now() }
func (h runningHost) Go(_ string, fn func()) {
	go func() {
		defer func() { _ = recover() }()
		fn()
	}()
}

// Without wraps p so the listed instances do not run, and counts the Publish calls each instance
// receives, running or not. A non-running instance's Host drops them, as the supervisor's does.
func Without(p plugin.HostProvider, off ...plugin.ID) *Partial {
	pt := &Partial{p: p, off: map[plugin.ID]bool{}, calls: map[plugin.ID]int{}}
	for _, id := range off {
		pt.off[id] = true
	}
	return pt
}

// Partial is the HostProvider Without returns.
type Partial struct {
	p     plugin.HostProvider
	off   map[plugin.ID]bool
	mu    sync.Mutex
	calls map[plugin.ID]int
}

func (p *Partial) Running(id plugin.ID) bool { return !p.off[id] && p.p.Running(id) }

func (p *Partial) HostFor(id plugin.ID) plugin.Host {
	return countingHost{Host: p.p.HostFor(id), p: p, id: id}
}

// Publishes is how many times a Publish was attempted on id's Host.
func (p *Partial) Publishes(id plugin.ID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[id]
}

type countingHost struct {
	plugin.Host
	p  *Partial
	id plugin.ID
}

func (h countingHost) Publish(topic string, data any, sources []model.SourceStatus) {
	h.p.mu.Lock()
	h.p.calls[h.id]++
	h.p.mu.Unlock()
	if h.p.off[h.id] {
		return
	}
	h.Host.Publish(topic, data, sources)
}
