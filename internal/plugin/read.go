package plugin

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
)

func (s *Supervisor) lookup(id ID) *instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

func (s *Supervisor) registry() []*instance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.instances)
}

// Infos is one PluginInfo per registered instance, in registry order, with the state as it is
// now (a running plugin's Status is read here, so the answer is never a poll interval old).
func (s *Supervisor) Infos() []model.PluginInfo {
	insts := s.registry()
	out := make([]model.PluginInfo, 0, len(insts))
	for _, in := range insts {
		out = append(out, s.info(in))
	}
	return out
}

func (s *Supervisor) info(in *instance) model.PluginInfo {
	st, msg := s.liveState(in)
	in.mu.RLock()
	running, p, enabled := in.running, in.p, in.cfg.ActionsEnabled
	in.mu.RUnlock()
	m := in.man
	pi := model.PluginInfo{
		ID: string(in.id), Type: string(in.id), Name: m.Name, Version: m.Version,
		State: st, Message: msg,
		Topics:       make([]string, 0, len(m.Topics)),
		Actions:      []model.ActionInfo{},
		Nav:          append([]model.NavItem{}, m.UI.Nav...),
		Generic:      make([]model.GenericView, 0, len(m.UI.Generic)),
		Configurable: in.cfgZero != nil,
	}
	for _, t := range m.Topics {
		pi.Topics = append(pi.Topics, string(TopicName(in.id, t)))
	}
	for _, v := range m.UI.Generic {
		for _, t := range m.Topics {
			if t.Name == v.Topic {
				v.Topic = string(TopicName(in.id, t))
			}
		}
		v.Columns = append([]model.GenericColumn{}, v.Columns...)
		pi.Generic = append(pi.Generic, v)
	}
	if len(pi.Generic) > 0 && len(pi.Nav) == 0 {
		icon := m.UI.Icon
		if icon == "" {
			icon = "Puzzle"
		}
		pi.Nav = append(pi.Nav, model.NavItem{Path: "/x/" + string(in.id), Label: m.Name, Title: m.Name, Icon: icon})
	}
	_, pi.Objects = p.(Objects)
	pi.Objects = pi.Objects && running // as objectsOf: only a running instance answers
	if running && enabled {
		if _, ok := p.(Actor); ok {
			for _, a := range m.Actions {
				pi.Actions = append(pi.Actions, model.ActionInfo{Name: a.Name, Title: a.Title, Destructive: a.Destructive, Idempotent: a.Idempotent, Confirm: a.Confirm, MinRole: string(a.MinRole)})
			}
		}
	}
	return pi
}

// Manifests is every registered manifest, in registry order, with the defaults Add applied
// (an unset MinRole is admin, an unset Confirm is none).
func (s *Supervisor) Manifests() []Manifest {
	insts := s.registry()
	out := make([]Manifest, len(insts))
	for i, in := range insts {
		out[i] = in.man
	}
	return out
}

// TopicOrder is the order the hub lists topics in: core.plugins, the overview, then every
// instance's topics under the names they publish.
func (s *Supervisor) TopicOrder() []model.Topic {
	out := []model.Topic{model.TopicPlugins, model.TopicOverview}
	for _, in := range s.registry() {
		for _, t := range in.man.Topics {
			out = append(out, TopicName(in.id, t))
		}
	}
	return out
}

// HostFor returns the Host of instance id, which is a no-op until the instance runs and again
// after it stops. HostFor(Core) is always live. It never returns nil.
func (s *Supervisor) HostFor(id ID) Host {
	if id == Core {
		return coreHost{s}
	}
	return &host{s: s, in: s.lookup(id)}
}

// Running reports whether instance id has started and not stopped.
func (s *Supervisor) Running(id ID) bool {
	in := s.lookup(id)
	return in != nil && in.isRunning()
}

// Action finds a mounted action: the instance runs, its actions are enabled, it is an Actor
// and declares the name.
func (s *Supervisor) Action(instance ID, name string) (ActionRef, bool) {
	in := s.lookup(instance)
	if in == nil {
		return ActionRef{}, false
	}
	in.mu.RLock()
	running, p, ic := in.running, in.p, in.cfg
	in.mu.RUnlock()
	if !running || !ic.ActionsEnabled {
		return ActionRef{}, false
	}
	actor, ok := p.(Actor)
	if !ok {
		return ActionRef{}, false
	}
	i := slices.IndexFunc(in.man.Actions, func(a ActionSpec) bool { return a.Name == name })
	if i < 0 {
		return ActionRef{}, false
	}
	spec := in.man.Actions[i]
	spec.ConfirmFor = s.liveConfirmHook(in, p, spec)
	return ActionRef{
		Instance:       instance,
		Spec:           spec,
		DenyNamespaces: slices.Clone(ic.DenyNamespaces),
		Agents:         cmp.Or(ic.ActionsAgents, pluginconf.AgentsApprove),
		Run:            s.actionRun(in, actor, name),
		ConfirmFor:     s.actionConfirm(in, spec),
	}, true
}

// liveConfirmHook is spec's ConfirmFor as the running plugin p declares it: Add read the manifest
// of another instance, and a hook closes over its own. A spec without one has none; a panic in
// Manifest answers ConfirmTypeName, as a panic in the hook does.
func (s *Supervisor) liveConfirmHook(in *instance, p Plugin, spec ActionSpec) func(context.Context, any) Confirm {
	if spec.ConfirmFor == nil {
		return nil
	}
	var live []ActionSpec
	if rec, _ := protect(func() { live = p.Manifest().Actions }); rec != nil {
		s.log().Error("panic in Manifest", "plugin", in.id, "panic", fmt.Sprint(rec))
		return func(context.Context, any) Confirm { return ConfirmTypeName }
	}
	if j := slices.IndexFunc(live, func(a ActionSpec) bool { return a.Name == spec.Name }); j >= 0 && live[j].ConfirmFor != nil {
		return live[j].ConfirmFor
	}
	return spec.ConfirmFor
}

// actionConfirm is ActionRef.ConfirmFor: the spec's hook, "" falling back to the static rule, and
// a panic in the hook answering ConfirmTypeName, the strictest rule.
func (s *Supervisor) actionConfirm(in *instance, spec ActionSpec) func(context.Context, any) Confirm {
	return func(ctx context.Context, input any) (c Confirm) {
		if spec.ConfirmFor == nil {
			return spec.Confirm
		}
		if rec, stack := protect(func() { c = spec.ConfirmFor(ctx, input) }); rec != nil {
			s.log().Error("panic in ConfirmFor", "plugin", in.id, "action", spec.Name, "panic", fmt.Sprint(rec), "stack", string(stack))
			return ConfirmTypeName
		}
		return cmp.Or(c, spec.Confirm)
	}
}

// Router returns the routes of a running Router plugin. The handler takes the full request
// path, /api/v1/plugins/<instance>/…, strips that prefix, and answers 404 for anything the
// plugin did not register, including every method but GET and HEAD. It follows a
// reconfiguration: the routes it serves are always the current instance's. Every route runs
// through g, the core's policy (see RouteGuard); the zero guard serves GET routes as they are.
func (s *Supervisor) Router(instance ID, g RouteGuard) (http.Handler, bool) {
	in := s.lookup(instance)
	if in == nil {
		return nil, false
	}
	in.mu.RLock()
	ok := in.running && in.routes != nil
	in.mu.RUnlock()
	if !ok {
		return nil, false
	}
	prefix := "/api/v1/plugins/" + string(instance)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		in.mu.RLock()
		mux := in.routes
		in.mu.RUnlock()
		rest, cut := strings.CutPrefix(r.URL.Path, prefix)
		if mux == nil || !cut || !strings.HasPrefix(rest, "/") {
			notFound(w, r)
			return
		}
		// Not http.StripPrefix: it also strips RawPath, and a request that spells the instance
		// with percent-escapes (/plugins/%63c/…) has a RawPath without the literal prefix, which
		// would make it answer with its own plain-text 404.
		// A shallow copy that carries the guard for the route handlers (Fail finds it there).
		r2 := r.WithContext(withGuard(r.Context(), instance, g))
		u := *r.URL
		u.Path = rest
		if u.RawPath != "" {
			u.RawPath = dropSegments(u.RawPath, 5) // "", api, v1, plugins, <instance>
		}
		r2.URL = &u
		mux.ServeHTTP(w, r2)
	}), true
}

// dropSegments removes the first n '/'-separated elements of an escaped path that starts with
// '/' (the empty one before it counts), leaving the path that starts at the next '/'.
func dropSegments(raw string, n int) string {
	for range n - 1 {
		i := strings.IndexByte(raw[1:], '/')
		if i < 0 {
			return ""
		}
		raw = raw[1+i:]
	}
	return raw
}

// RouteInfos lists the routes a running Router plugin registered (for the API route table).
func (s *Supervisor) RouteInfos(instance ID) []RouteInfo {
	in := s.lookup(instance)
	if in == nil {
		return nil
	}
	in.mu.RLock()
	running, p := in.running, in.p
	in.mu.RUnlock()
	if rt, ok := p.(Router); ok && running {
		return CollectRoutes(rt)
	}
	return nil
}

// Series finds a named series of a running instance. The spec comes from the Manifest of the
// plugin that is running, not from the one captured when the instance was added: a series whose
// Expr closes over its plugin's own state (its settings, set in Start) must read the instance
// that was started, which after a reload is a new one, and which in demo mode is the twin. The
// plugin is called outside every supervisor lock.
func (s *Supervisor) Series(name string) (SeriesSpec, ID, bool) {
	var found SeriesSpec
	var owner ID
	s.eachSeries(func(id ID, sp SeriesSpec) bool {
		if sp.Name != name {
			return true
		}
		found, owner = sp, id
		return false
	})
	return found, owner, owner != ""
}

// SeriesNames are the names of the series the running instances define, in registry order: what
// Series finds.
func (s *Supervisor) SeriesNames() []string {
	out := []string{}
	s.eachSeries(func(_ ID, sp SeriesSpec) bool { out = append(out, sp.Name); return true })
	return out
}

// eachSeries calls fn with every series of every running instance, from the Manifest of the
// plugin that runs, until fn returns false.
func (s *Supervisor) eachSeries(fn func(ID, SeriesSpec) bool) {
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p := in.running, in.p
		in.mu.RUnlock()
		if !running || p == nil {
			continue
		}
		var series []SeriesSpec
		if rec, _ := protect(func() { series = p.Manifest().Series }); rec != nil {
			s.log().Error("panic in Manifest", "plugin", in.id, "panic", fmt.Sprint(rec))
			continue
		}
		for _, sp := range series {
			if !fn(in.id, sp) {
				return
			}
		}
	}
}

// Ready reports the plugin half of /readyz: every instance's Start has returned, and every
// instance in Options.ReadyRequires has left "starting" (a disabled or unknown one counts as
// settled). A required plugin that is still starting when ReadyTimeout has passed since Run
// began does not hold readiness back; one WARN names it. The timeout runs on Options.Wall, so a
// fixed demo clock does not hold it back.
func (s *Supervisor) Ready() bool {
	if !s.started.Load() {
		return false
	}
	var pending []string
	for _, id := range s.opt.ReadyRequires {
		in := s.lookup(id)
		if in == nil {
			continue
		}
		if st, _ := s.liveState(in); st == StateStarting {
			pending = append(pending, string(id))
		}
	}
	if len(pending) == 0 {
		return true
	}
	if s.opt.Wall().Sub(s.runStart) < s.opt.ReadyTimeout {
		return false
	}
	s.readyWarn.Do(func() {
		s.log().Warn("ready without waiting any longer for plugins that are still starting", "plugins", pending, "timeout", s.opt.ReadyTimeout)
	})
	return true
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found", "code": "not_found", "requestId": w.Header().Get("X-Request-Id")})
}

var _ HostProvider = (*Supervisor)(nil)
