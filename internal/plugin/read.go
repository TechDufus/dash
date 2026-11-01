package plugin

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/model"
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
		Configurable: in.cfgZero != nil,
	}
	for _, t := range m.Topics {
		pi.Topics = append(pi.Topics, string(topicName(in.id, t)))
	}
	if running && enabled {
		if _, ok := p.(Actor); ok {
			for _, a := range m.Actions {
				pi.Actions = append(pi.Actions, model.ActionInfo{Name: a.Name, Title: a.Title, Destructive: a.Destructive, Idempotent: a.Idempotent, Confirm: a.Confirm})
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
			out = append(out, topicName(in.id, t))
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
	return ActionRef{
		Instance:       instance,
		Spec:           in.man.Actions[i],
		DenyNamespaces: slices.Clone(ic.DenyNamespaces),
		Run:            s.actionRun(in, actor, name),
	}, true
}

// Router returns the GET routes of a running Router plugin. The handler takes the full request
// path, /api/v1/plugins/<instance>/…, strips that prefix, and answers 404 for anything the
// plugin did not register, including every method but GET and HEAD. It follows a
// reconfiguration: the routes it serves are always the current instance's.
func (s *Supervisor) Router(instance ID) (http.Handler, bool) {
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
		if rest, cut := strings.CutPrefix(r.URL.Path, prefix); mux == nil || !cut || !strings.HasPrefix(rest, "/") {
			notFound(w, r)
			return
		}
		http.StripPrefix(prefix, mux).ServeHTTP(w, r)
	}), true
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

// Series finds a named series of a running instance.
func (s *Supervisor) Series(name string) (SeriesSpec, ID, bool) {
	for _, in := range s.registry() {
		if !in.isRunning() {
			continue
		}
		if i := slices.IndexFunc(in.man.Series, func(sp SeriesSpec) bool { return sp.Name == name }); i >= 0 {
			return in.man.Series[i], in.id, true
		}
	}
	return SeriesSpec{}, "", false
}

// Ready reports the plugin half of /readyz: every instance's Start has returned, and every
// instance in Options.ReadyRequires has left "starting" (a disabled or unknown one counts as
// settled). A required plugin that is still starting when ReadyTimeout has passed since Run
// began does not hold readiness back; one WARN names it.
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
	if s.opt.Now().Sub(s.runStart) < s.opt.ReadyTimeout {
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
