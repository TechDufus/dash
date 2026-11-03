package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"
)

// ErrNotCheckable is what Supervisor.Check answers for a plugin that has no connection check:
// an id that is not registered, a plugin that does not implement Checker, or any plugin in
// demo mode, which contacts no upstream.
var ErrNotCheckable = errors.New("plugin has no connection check")

// Testable reports whether Check can test a configuration of plugin id: it is registered, it
// implements Checker, and dash is not in demo mode.
func (s *Supervisor) Testable(id ID) bool {
	in := s.lookup(id)
	if in == nil || s.opt.Demo {
		return false
	}
	in.mu.RLock()
	probe := in.probe
	in.mu.RUnlock()
	_, ok := probe.(Checker)
	return ok
}

// Check tests candidate, a configuration of plugin id, against its upstream, without touching
// the running instance or the configuration: it runs the Checker of a fresh plugin from the
// factory, which is never started, within CheckTimeout. candidate must be a pointer of the
// type the plugin's Manifest.Config returns and pass its Validate. The clients the check builds
// are those of Host.HTTPClient (the dial guard, Options.Transport), and with fromUI they also
// refuse loopback, as for a URL saved from the UI.
//
// The error is ErrNotCheckable (see there), or a configuration the plugin refuses; a check that
// ran answers its report and a nil error, whatever the upstream said.
func (s *Supervisor) Check(ctx context.Context, id ID, candidate any, fromUI bool) (CheckReport, error) {
	if !s.Testable(id) {
		return CheckReport{}, ErrNotCheckable
	}
	in := s.lookup(id)
	p := in.factory()
	c, ok := p.(Checker)
	if !ok {
		return CheckReport{}, ErrNotCheckable
	}
	if in.cfgType == nil || reflect.TypeOf(candidate) != in.cfgType {
		return CheckReport{}, fmt.Errorf("%s: configuration is %T, want %v", id, candidate, in.cfgType)
	}
	if v, ok := p.(Validator); ok {
		var err error
		if rec, stack := protect(func() { err = v.Validate(candidate) }); rec != nil {
			s.log().Error("panic in Validate", "plugin", id, "panic", fmt.Sprint(rec), "stack", string(stack))
			return CheckReport{}, fmt.Errorf("%s: panic in Validate", id)
		}
		if err != nil {
			return CheckReport{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	var rep CheckReport
	if rec, stack := protect(func() { rep = c.Check(ctx, candidate, checkEnv{s: s, id: id, fromUI: fromUI}) }); rec != nil {
		s.log().Error("panic in Check", "plugin", id, "panic", fmt.Sprint(rec), "stack", string(stack))
		return CheckReport{}, fmt.Errorf("%s: panic in Check", id)
	}
	return rep, nil
}

// checkEnv is the CheckEnv of Supervisor.Check.
type checkEnv struct {
	s      *Supervisor
	id     ID
	fromUI bool
}

// HTTPClient builds the client as Host.HTTPClient does, with FromUI forced on for a candidate
// from the UI. It logs nothing: a check reports, it does not warn.
func (e checkEnv) HTTPClient(c HTTPConfig) (*http.Client, error) {
	c.FromUI = c.FromUI || e.fromUI
	cl, err := newHTTPClient(c)
	if err != nil {
		return nil, err
	}
	if t := e.s.opt.Transport; t != nil {
		cl.Transport = t(e.id, cl.Transport)
	}
	return cl, nil
}

func (e checkEnv) Now() time.Time { return e.s.opt.Now() }
