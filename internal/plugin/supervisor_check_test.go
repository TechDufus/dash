package plugin_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

type checkCfg struct{ URL string }

// checking is a Fake that implements plugin.Checker with fn.
type checking struct {
	*plugintest.Fake
	fn func(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport
}

func (c *checking) Check(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
	return c.fn(ctx, cfg, env)
}

// checkingFactory makes a fresh checking plugin "ck" on every call and records each one.
type checkingFactory struct {
	fn       func(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport
	validate func(cfg any) error

	mu   sync.Mutex
	made []*checking
}

func (f *checkingFactory) factory() plugin.Plugin {
	p := &checking{
		Fake: plugintest.New("ck", plugintest.Config(func() any { return &checkCfg{} })),
		fn:   f.fn,
	}
	p.ValidateFn = f.validate
	f.mu.Lock()
	f.made = append(f.made, p)
	f.mu.Unlock()
	return p
}

func (f *checkingFactory) all() []*checking {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*checking(nil), f.made...)
}

func TestSupervisorCheckRunsAFreshCheckerWithinCheckTimeout(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var (
		gotCfg      any
		gotDeadline time.Time
		gotNow      time.Time
	)
	f := &checkingFactory{}
	f.fn = func(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
		gotCfg, gotNow = cfg, env.Now()
		gotDeadline, _ = ctx.Deadline()
		return plugin.CheckReport{Checks: []plugin.CheckResult{{Name: "read", OK: true}}}
	}
	e := newEnv(t, func(o *plugin.Options) { o.Now = func() time.Time { return now } }, f.factory).run(t)
	eventually(t, "ck to run", func() bool { return e.sup.Running("ck") })
	running := f.all()

	if !e.sup.Testable("ck") {
		t.Fatal("Testable(ck) = false for a Checker")
	}
	cand := &checkCfg{URL: "https://upstream.invalid"}
	before := time.Now()
	rep, err := e.sup.Check(context.Background(), "ck", cand, false)
	after := time.Now()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(rep.Checks) != 1 || !rep.Checks[0].OK || rep.Checks[0].Name != "read" {
		t.Errorf("report = %+v, want the checker's", rep)
	}
	if gotCfg != cand {
		t.Errorf("the checker got %v, want the candidate itself", gotCfg)
	}
	if !gotNow.Equal(now) {
		t.Errorf("env.Now() = %v, want Options.Now's %v", gotNow, now)
	}
	if gotDeadline.Before(before.Add(plugin.CheckTimeout)) || gotDeadline.After(after.Add(plugin.CheckTimeout)) {
		t.Errorf("the check's deadline %v is not CheckTimeout (%v) after the call", gotDeadline, plugin.CheckTimeout)
	}
	made := f.all()
	if len(made) != len(running)+1 {
		t.Fatalf("Check made %d plugins, want one fresh one", len(made)-len(running))
	}
	fresh := made[len(made)-1]
	for _, p := range running {
		if p == fresh {
			t.Fatal("Check ran on a plugin the supervisor had already built")
		}
	}
	if fresh.Starts() != 0 {
		t.Errorf("the checked plugin was started %d times, want never", fresh.Starts())
	}
}

func TestSupervisorCheckRefusesWhatItCannotCheck(t *testing.T) {
	ran := atomic.Int32{}
	ok := func(context.Context, any, plugin.CheckEnv) plugin.CheckReport {
		ran.Add(1)
		return plugin.CheckReport{}
	}
	invalid := errors.New("url is required")
	f := &checkingFactory{fn: ok, validate: func(cfg any) error {
		if cfg.(*checkCfg).URL == "" {
			return invalid
		}
		return nil
	}}
	plain := plugintest.New("pl")
	e := start(t, f.factory, plain.Factory())

	for _, c := range []struct {
		name string
		id   plugin.ID
		cand any
		want error
	}{
		{"unknown id", "nope", &checkCfg{URL: "u"}, plugin.ErrNotCheckable},
		{"no Checker", "pl", &checkCfg{URL: "u"}, plugin.ErrNotCheckable},
		{"refused by Validate", "ck", &checkCfg{}, invalid},
	} {
		if _, err := e.sup.Check(context.Background(), c.id, c.cand, false); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if e.sup.Testable("pl") || e.sup.Testable("nope") {
		t.Error("Testable is true for a plugin without a check")
	}
	_, err := e.sup.Check(context.Background(), "ck", checkCfg{URL: "u"}, false)
	if err == nil || errors.Is(err, plugin.ErrNotCheckable) {
		t.Errorf("a candidate of the wrong type: err = %v, want a type error", err)
	}
	if ran.Load() != 0 {
		t.Errorf("the checker ran %d times for refused candidates", ran.Load())
	}

	panics := &checkingFactory{fn: func(context.Context, any, plugin.CheckEnv) plugin.CheckReport { panic("boom") }}
	pe := start(t, panics.factory)
	if _, err := pe.sup.Check(context.Background(), "ck", &checkCfg{URL: "u"}, false); err == nil {
		t.Error("a panicking checker answered no error")
	}

	demo := newEnv(t, func(o *plugin.Options) { o.Demo = true }, (&checkingFactory{fn: ok}).factory).run(t)
	if demo.sup.Testable("ck") {
		t.Error("Testable is true in demo mode")
	}
	if _, err := demo.sup.Check(context.Background(), "ck", &checkCfg{URL: "u"}, false); !errors.Is(err, plugin.ErrNotCheckable) {
		t.Errorf("demo mode: err = %v, want ErrNotCheckable", err)
	}
}

// The check's client is the plugin's: the dial guard (with loopback refused for a candidate
// from the UI) and Options.Transport, under the plugin's id.
func TestSupervisorCheckEnvGuardsLikeTheHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()

	var getErr error
	f := &checkingFactory{fn: func(ctx context.Context, cfg any, env plugin.CheckEnv) plugin.CheckReport {
		c, err := env.HTTPClient(plugin.HTTPConfig{})
		if err == nil {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.(*checkCfg).URL, nil)
			var resp *http.Response
			if resp, err = c.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
		getErr = err
		return plugin.CheckReport{}
	}}
	var wrapped atomic.Value
	e := newEnv(t, func(o *plugin.Options) {
		o.Transport = func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
			wrapped.Store(id)
			return rt
		}
	}, f.factory).run(t)

	if _, err := e.sup.Check(context.Background(), "ck", &checkCfg{URL: srv.URL}, false); err != nil || getErr != nil {
		t.Fatalf("a loopback URL from the file or env: Check err = %v, GET err = %v", err, getErr)
	}
	if id, _ := wrapped.Load().(plugin.ID); id != "ck" {
		t.Errorf("Options.Transport wrapped the check's client for %q, want ck", id)
	}
	if _, err := e.sup.Check(context.Background(), "ck", &checkCfg{URL: srv.URL}, true); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(getErr, plugin.ErrBlockedAddress) {
		t.Errorf("a loopback URL from the UI: GET err = %v, want ErrBlockedAddress", getErr)
	}
	if _, err := e.sup.Check(context.Background(), "ck", &checkCfg{URL: "http://169.254.169.254/"}, false); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(getErr, plugin.ErrBlockedAddress) {
		t.Errorf("the metadata address: GET err = %v, want ErrBlockedAddress", getErr)
	}
}
