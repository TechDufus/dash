package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// restartIn is the input of the fixture's "restart" action.
type restartIn struct {
	Namespace string `json:"namespace" dash:"required"`
	Name      string `json:"name" dash:"required"`
	Kind      string `json:"kind,omitempty" dash:"enum=Deployment|StatefulSet"`
	Confirm   string `json:"confirm,omitempty"`
}

// scaleIn is the input of "scale", which checks itself.
type scaleIn struct {
	Replicas int `json:"replicas"`
}

func (s *scaleIn) Validate() error {
	if s.Replicas < 0 {
		return errors.New("replicas must not be negative")
	}
	return nil
}

// actCall is one Act the fixture saw.
type actCall struct {
	action string
	p      auth.Principal
	input  any
}

// actionEnv is a pluginEnv with the fixture plugin "aa": restart (input, target, destructive),
// scale (input with Validate) and ping (no input).
type actionEnv struct {
	*pluginEnv
	mu    sync.Mutex
	calls []actCall
}

func (e *actionEnv) acts() []actCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]actCall(nil), e.calls...)
}

// newActionEnv serves the fixture with its actions enabled; fn is what Act returns.
func newActionEnv(t *testing.T, fn func(action string, in any) (any, error)) *actionEnv {
	t.Helper()
	return newActionEnvWith(t, fn, []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}})
}

// newActionEnvWith is newActionEnv with the plugin settings and more plugins of the test's own.
func newActionEnvWith(t *testing.T, fn func(action string, in any) (any, error), ics []plugin.InstanceConfig, extra ...factoryOf) *actionEnv {
	t.Helper()
	return newActionEnvSpecs(t, fn, ics, nil, extra...)
}

// newActionEnvSpecs is newActionEnvWith with more actions on the fixture plugin "aa".
func newActionEnvSpecs(t *testing.T, fn func(action string, in any) (any, error), ics []plugin.InstanceConfig, more []plugin.ActionSpec, extra ...factoryOf) *actionEnv {
	t.Helper()
	e := &actionEnv{}
	f := e.fixture(fn, more)
	e.pluginEnv = newPluginEnv(t, ics, append([]factoryOf{f}, extra...)...)
	return e
}

// fixture is the plugin "aa" whose Act records every call in e and answers with fn.
func (e *actionEnv) fixture(fn func(action string, in any) (any, error), more []plugin.ActionSpec) *plugintest.Fake {
	f := plugintest.New("aa")
	f.M.Actions = append([]plugin.ActionSpec{
		{
			Name: "restart", Title: "Restart", Destructive: true,
			Input:  func() any { return &restartIn{} },
			Target: func(in any) (string, string, string) { i := in.(*restartIn); return "Workload", i.Namespace, i.Name },
		},
		{Name: "scale", Title: "Scale", Input: func() any { return &scaleIn{} }},
		{Name: "ping", Title: "Ping", Idempotent: true},
	}, more...)
	f.ActionFn = func(_ context.Context, action string, p auth.Principal, in any) (any, error) {
		e.mu.Lock()
		e.calls = append(e.calls, actCall{action, p, in})
		e.mu.Unlock()
		if fn == nil {
			return nil, nil
		}
		return fn(action, in)
	}
	return f
}

// nonActor is a plugin that declares actions but has no Act.
type nonActor struct{ f *plugintest.Fake }

func (n nonActor) Factory() plugin.Factory {
	return func() plugin.Plugin { return struct{ plugin.Plugin }{n.f} }
}

func (e *actionEnv) post(action string, body any) *httptest.ResponseRecorder {
	return req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/" + action, body: body}.do(e.Handler())
}

var okRestart = restartIn{Namespace: "apps", Name: "web", Kind: "Deployment"}

func apiError(t *testing.T, w *httptest.ResponseRecorder) model.APIError {
	t.Helper()
	var e model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not an APIError: %v", w.Body, err)
	}
	return e
}

// wantError fails unless w is the given status with the given code and a message.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) model.APIError {
	t.Helper()
	e := apiError(t, w)
	if w.Code != status || e.Code != code || e.Error == "" {
		t.Fatalf("got %d %+v, want %d with code %q and a message", w.Code, e, status, code)
	}
	return e
}

// roleStub raises the role every action of the wrapped plugins needs; no role in G2 reaches
// it, so it stands in for a viewer.
type roleStub struct {
	PluginsAPI
	min auth.Role
}

func (r roleStub) Action(i plugin.ID, n string) (plugin.ActionRef, bool) {
	ref, ok := r.PluginsAPI.Action(i, n)
	ref.Spec.MinRole = r.min
	return ref, ok
}

func TestActionNeedsRole(t *testing.T) {
	e := newActionEnv(t, nil)
	e.d.Plugins = roleStub{e.sup, "owner"}

	wantError(t, e.post("restart", okRestart), http.StatusForbidden, model.ErrCodeForbidden)
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times for a refused principal", n)
	}
	recs := auditRecords(t, e.dir)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one denial", recs)
	}
	if r := recs[0]; r.Kind != audit.KindDenial || r.Reason != "forbidden" || r.Actor.User != "alice" || r.Action != "POST /api/v1/plugins/aa/actions/restart" {
		t.Errorf("denial = %+v", r)
	}

	// The same principal passes where the role is enough.
	e.d.Plugins = e.sup
	if w := e.post("restart", okRestart); w.Code != http.StatusAccepted {
		t.Fatalf("admin restart = %d %s", w.Code, w.Body)
	}
}

func TestActionCSRFFailure(t *testing.T) {
	e := newActionEnv(t, nil)
	bad := req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/restart", body: okRestart, header: map[string]string{"X-Dash-Csrf": ""}}
	wantError(t, bad.do(e.Handler()), http.StatusForbidden, model.ErrCodeCSRFFailed)
	bad.header = map[string]string{"Origin": "https://evil.example"}
	wantError(t, bad.do(e.Handler()), http.StatusForbidden, model.ErrCodeCSRFFailed)

	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times without the CSRF header", n)
	}
	recs := auditRecords(t, e.dir)
	if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "csrf" {
		t.Fatalf("records = %+v, want one csrf denial (the second is rate limited)", recs)
	}
}

func TestActionInvalidInputIs400(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		body         any
	}{
		{"not JSON", "restart", "{"},
		{"empty body", "restart", ""},
		{"unknown field", "restart", `{"namespace":"apps","name":"web","bogus":1}`},
		{"missing required field", "restart", `{"namespace":"apps"}`},
		{"wrong type", "restart", `{"namespace":5,"name":"web"}`},
		{"enum violated", "restart", `{"namespace":"apps","name":"web","kind":"CronJob"}`},
		{"the input's own Validate", "scale", `{"replicas":-1}`},
		{"body over the cap", "restart", `{"namespace":"` + strings.Repeat("a", maxBody) + `","name":"web"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newActionEnv(t, nil)
			wantError(t, e.post(tc.action, tc.body), http.StatusBadRequest, model.ErrCodeInvalidInput)
			if n := len(e.acts()); n != 0 {
				t.Errorf("Act ran %d times", n)
			}
			// Nothing was about to happen, so there is no intent to record.
			if recs := auditRecords(t, e.dir); len(recs) != 0 {
				t.Errorf("records = %+v", recs)
			}
		})
	}

	t.Run("a valid input passes", func(t *testing.T) {
		e := newActionEnv(t, nil)
		if w := e.post("scale", `{"replicas":3}`); w.Code != http.StatusAccepted {
			t.Fatalf("scale = %d %s", w.Code, w.Body)
		}
		if w := e.post("restart", `{"namespace":"apps","name":"web","kind":"StatefulSet"}`); w.Code != http.StatusAccepted {
			t.Fatalf("restart = %d %s", w.Code, w.Body)
		}
	})
}

func TestActionsDisabledIs404(t *testing.T) {
	for _, tc := range []struct {
		name string
		ics  []plugin.InstanceConfig
		path string
	}{
		{"actions.enabled is false", []plugin.InstanceConfig{{Name: "aa"}}, "/api/v1/plugins/aa/actions/ping"},
		{"the instance is disabled", []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true, Enabled: new(false)}}, "/api/v1/plugins/aa/actions/ping"},
		{"unknown plugin", []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}, "/api/v1/plugins/zz/actions/ping"},
		{"invalid plugin id", []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}, "/api/v1/plugins/BAD!/actions/ping"},
		{"undeclared action", []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}, "/api/v1/plugins/aa/actions/nope"},
		{"not an Actor", []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}, {Name: "na", ActionsEnabled: true}}, "/api/v1/plugins/na/actions/restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			na := plugintest.New("na")
			na.M.Actions = []plugin.ActionSpec{{Name: "restart"}}
			e := newActionEnvWith(t, nil, tc.ics, nonActor{na})
			wantError(t, req{method: http.MethodPost, path: tc.path}.do(e.Handler()), http.StatusNotFound, model.ErrCodeNotFound)
			if n := len(e.acts()); n != 0 {
				t.Errorf("Act ran %d times", n)
			}
		})
	}
}

func TestActionWritesIntentAndOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		act    func(action string, in any) (any, error)
		status int
		code   string // of an error
		result string // of the outcome record
		check  func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "no result is accepted", status: http.StatusAccepted, result: audit.ResultOK,
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				if strings.TrimSpace(w.Body.String()) != `{"status":"accepted"}` {
					t.Errorf("body = %s", w.Body)
				}
			},
		},
		{
			name: "a result is returned", status: http.StatusOK, result: audit.ResultOK,
			act: func(string, any) (any, error) { return map[string]int{"restarted": 3}, nil },
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				if strings.TrimSpace(w.Body.String()) != `{"restarted":3}` {
					t.Errorf("body = %s", w.Body)
				}
			},
		},
		{
			name: "an ActionError keeps its status, code and details", status: http.StatusConflict, code: model.ErrCodeConflict, result: audit.ResultError,
			act: func(string, any) (any, error) {
				return nil, &plugin.ActionError{Status: http.StatusConflict, Code: model.ErrCodeConflict, Message: "already restarting", Details: map[string]int{"retryAfter": 5}}
			},
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				e := apiError(t, w)
				if e.Error != "already restarting" || !strings.Contains(w.Body.String(), `"details":{"retryAfter":5}`) {
					t.Errorf("body = %s", w.Body)
				}
			},
		},
		{
			name: "an ActionError without a code gets the one its status implies", status: http.StatusBadRequest, code: model.ErrCodeInvalidInput, result: audit.ResultError,
			act: func(string, any) (any, error) {
				return nil, &plugin.ActionError{Status: http.StatusBadRequest, Message: "type the workload name to confirm"}
			},
		},
		{
			name: "another error is a 502 that names no upstream", status: http.StatusBadGateway, code: model.ErrCodeUpstreamUnavailable, result: audit.ResultError,
			act: func(string, any) (any, error) {
				return nil, errors.New(`Get "http://prometheus.internal:9090/api/v1/query": dial tcp 10.0.0.9:9090: connect: connection refused`)
			},
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				if b := w.Body.String(); strings.Contains(b, "prometheus.internal") || strings.Contains(b, "10.0.0.9") || !strings.Contains(b, "aa:") {
					t.Errorf("body = %s", b)
				}
			},
		},
		{
			name: "a panic is a 500", status: http.StatusInternalServerError, code: model.ErrCodeInternal, result: audit.ResultError,
			act: func(string, any) (any, error) { panic("kaboom in the plugin") },
			check: func(t *testing.T, w *httptest.ResponseRecorder) {
				if strings.Contains(w.Body.String(), "kaboom") {
					t.Errorf("body leaks the panic: %s", w.Body)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newActionEnv(t, tc.act)
			w := e.post("restart", okRestart)
			if tc.code != "" {
				wantError(t, w, tc.status, tc.code)
			} else if w.Code != tc.status {
				t.Fatalf("code = %d %s, want %d", w.Code, w.Body, tc.status)
			}
			if tc.check != nil {
				tc.check(t, w)
			}

			acts := e.acts()
			if len(acts) != 1 {
				t.Fatalf("Act ran %d times, want once", len(acts))
			}
			a := acts[0]
			in, ok := a.input.(*restartIn)
			if a.action != "restart" || !ok || *in != okRestart {
				t.Errorf("Act got %q %#v", a.action, a.input)
			}
			if want := (auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}); a.p != want {
				t.Errorf("principal = %+v, want %+v", a.p, want)
			}

			recs := auditRecords(t, e.dir)
			if len(recs) != 2 {
				t.Fatalf("records = %+v, want intent then outcome", recs)
			}
			intent, outcome := recs[0], recs[1]
			if intent.Kind != audit.KindIntent || outcome.Kind != audit.KindOutcome || intent.ID == "" || intent.ID != outcome.ID {
				t.Errorf("records = %+v / %+v", intent, outcome)
			}
			for _, r := range recs {
				if r.Action != "restart" || r.Plugin != "aa" || r.Target != (audit.Target{Kind: "Workload", Namespace: "apps", Name: "web"}) ||
					r.Actor.User != "alice" || r.Actor.Surface != "ui" || r.AuthMode != "proxy" || r.Peer != "192.0.2.1" {
					t.Errorf("record = %+v", r)
				}
			}
			if outcome.Result != tc.result {
				t.Errorf("outcome result = %q, want %q (%+v)", outcome.Result, tc.result, outcome)
			}
		})
	}
}

func TestEleventhActionInAMinuteIs429RateLimited(t *testing.T) {
	e := newActionEnv(t, nil)
	for i := 1; i <= actionRateLimit; i++ {
		if w := e.post("ping", nil); w.Code != http.StatusAccepted {
			t.Fatalf("action %d = %d %s", i, w.Code, w.Body)
		}
	}
	w := e.post("ping", nil)
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if got := w.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
	if n := len(e.acts()); n != actionRateLimit {
		t.Errorf("Act ran %d times, want %d", n, actionRateLimit)
	}
	var denials []audit.Record
	for _, r := range auditRecords(t, e.dir) {
		if r.Kind == audit.KindDenial {
			denials = append(denials, r)
		}
	}
	if len(denials) != 1 || denials[0].Reason != "rate_limit" || denials[0].Actor.User != "alice" {
		t.Errorf("denials = %+v", denials)
	}

	// The limit is per principal, and the window slides.
	bob := req{method: http.MethodPost, path: "/api/v1/plugins/aa/actions/ping", header: map[string]string{"X-Forwarded-Preferred-Username": "bob"}}
	if w := bob.do(e.Handler()); w.Code != http.StatusAccepted {
		t.Errorf("bob = %d %s", w.Code, w.Body)
	}
	e.clk.advance(actionRateWindow + time.Second)
	if w := e.post("ping", nil); w.Code != http.StatusAccepted {
		t.Errorf("alice a window later = %d %s", w.Code, w.Body)
	}
}

func TestDeniedNamespaceIs403AndAudited(t *testing.T) {
	e := newActionEnvWith(t, nil, []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true, DenyNamespaces: []string{"kube-system"}}})
	w := e.post("restart", restartIn{Namespace: "kube-system", Name: "coredns"})
	wantError(t, w, http.StatusForbidden, model.ErrCodeForbidden)
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times in a denied namespace", n)
	}
	recs := auditRecords(t, e.dir)
	if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "namespace_denied" {
		t.Fatalf("records = %+v, want one namespace_denied denial", recs)
	}

	if w := e.post("restart", restartIn{Namespace: "apps", Name: "web"}); w.Code != http.StatusAccepted {
		t.Errorf("allowed namespace = %d %s", w.Code, w.Body)
	}
}

func TestAuditFailureIs503AuditUnavailableAndActDoesNotRun(t *testing.T) {
	e := newActionEnv(t, nil)
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// MaxSize 1 makes the next write rotate the file, and rotating inside a directory that no
	// longer exists fails: a write error on a writable data dir.
	al, err := audit.Open(dir, audit.Options{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	al.Event(audit.Record{Action: "seed"})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e.d.Audit = al

	msg := wantError(t, e.post("restart", okRestart), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	if msg.Error != "audit log unavailable" {
		t.Errorf("message = %q", msg.Error)
	}
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times without a durable intent", n)
	}
}

// The framework does not enforce ConfirmTypeName: Act checks the typed name (contract §3.6). What
// this proves is that a refusal Act returns reaches the client with its status and code, is
// audited as an error outcome each time, and does not stop a later run that succeeds.
func TestActionRefusalFromActIsAuditedAsErrorAndALaterRunSucceeds(t *testing.T) {
	e := newActionEnv(t, func(_ string, in any) (any, error) {
		if r := in.(*restartIn); r.Confirm != r.Name {
			return nil, &plugin.ActionError{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "type the workload name to confirm"}
		}
		return nil, nil
	})
	for _, confirm := range []string{"", "wrong"} {
		in := okRestart
		in.Confirm = confirm
		msg := wantError(t, e.post("restart", in), http.StatusBadRequest, model.ErrCodeInvalidInput)
		if msg.Error != "type the workload name to confirm" {
			t.Errorf("message = %q", msg.Error)
		}
	}
	in := okRestart
	in.Confirm = "web"
	if w := e.post("restart", in); w.Code != http.StatusAccepted {
		t.Fatalf("with the name typed = %d %s", w.Code, w.Body)
	}

	var results []string
	for _, r := range auditRecords(t, e.dir) {
		if r.Kind == audit.KindOutcome {
			results = append(results, r.Result)
		}
	}
	if want := []string{audit.ResultError, audit.ResultError, audit.ResultOK}; strings.Join(results, ",") != strings.Join(want, ",") {
		t.Errorf("outcomes = %v, want %v", results, want)
	}
}
