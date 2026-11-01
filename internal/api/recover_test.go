package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// panicIn is the input of the actions whose own callbacks panic.
type panicIn struct {
	Name string `json:"name"`
}

func (panicIn) Validate() error { panic("kaboom in Validate") }

// wantInternal checks a recovered panic: 500, code internal, the request id of the header in the
// body, and nothing of the panic or the plugin.
func wantInternal(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	code, body, e := w.Code, w.Body.String(), apiError(t, w)
	if code != http.StatusInternalServerError || e.Code != model.ErrCodeInternal {
		t.Errorf("%s = %d %s, want 500 internal", what, code, body)
	}
	if id := w.Header().Get(requestIDHeader); id == "" || e.RequestID != id {
		t.Errorf("%s: requestId %q in the body, header %q", what, e.RequestID, id)
	}
	if strings.Contains(body, "kaboom") {
		t.Errorf("%s leaks the panic: %s", what, body)
	}
}

// A panic in code the plugin supplied and the API calls (Target, Input, an input's Validate)
// answers 500 internal with a request id instead of dropping the connection; Act never runs, the
// refusal is audited, and the metrics count the request as an error, not as the 200 of an
// unwritten response.
func TestPanickingActionCallbacksAnswer500(t *testing.T) {
	more := []plugin.ActionSpec{
		{Name: "bad_target", Input: func() any { return &restartIn{} }, Target: func(any) (string, string, string) { panic("kaboom in Target") }},
		{Name: "bad_input", Input: func() any { panic("kaboom in Input") }},
		{Name: "bad_validate", Input: func() any { return &panicIn{} }},
	}
	for _, tc := range []struct {
		action string
		body   any
	}{
		{"bad_target", okRestart},
		{"bad_input", map[string]string{}},
		{"bad_validate", map[string]string{"name": "x"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			e := newActionEnvSpecs(t, nil, []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}, more)
			e.Server.d.Metrics = obs.New("test", RoutePatterns(), e.sup.Infos)

			w := e.post(tc.action, tc.body)
			wantInternal(t, tc.action, w)
			if n := len(e.acts()); n != 0 {
				t.Errorf("Act ran %d times after a panic in the plugin's callback", n)
			}

			if recs := auditRecords(t, e.dir); len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "internal" {
				t.Errorf("audit = %+v, want one internal refusal", recs)
			}

			ss := scrapeMetrics(t, e.Server)
			if v, _ := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "aa", "action": tc.action, "outcome": obs.ActionError}); v != 1 {
				t.Errorf("error outcomes of %s = %v, want 1", tc.action, v)
			}
			if v, _ := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "aa", "action": tc.action, "outcome": obs.ActionOK}); v != 0 {
				t.Errorf("%s counted as ok", tc.action)
			}
			if v, _ := seriesValue(t, ss, "dash_http_requests_total", map[string]string{"code": "500"}); v != 1 {
				t.Errorf("500s counted = %v, want 1", v)
			}
		})
	}
}

// The same for the GET routes plugins serve and for a series' Expr.
func TestPanickingPluginReadersAnswer500(t *testing.T) {
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom in a handler") }, plugin.RouteSpec{Summary: "boom", Scope: "read"})
	}
	f.M.Series = []plugin.SeriesSpec{{Name: "aa.boom", Expr: func(plugin.SeriesArgs) (string, error) { panic("kaboom in Expr") }}}
	e := newPluginEnv(t, nil, f)
	e.Server.d.Metrics = obs.New("test", RoutePatterns(), e.sup.Infos)
	h := e.Handler()

	for _, path := range []string{"/api/v1/plugins/aa/boom", "/api/v1/series?name=aa.boom&range=1h"} {
		w := req{path: path}.do(h)
		wantInternal(t, path, w)
	}
	// The server carries on.
	if w := (req{path: "/api/v1/plugins"}.do(h)); w.Code != http.StatusOK {
		t.Errorf("after the panics: %d %s", w.Code, w.Body)
	}
}

// A panic after the handler has started its answer cannot be turned into a 500: the answer
// stays as written and nothing is appended to it.
func TestPanicAfterTheAnswerStartedWritesNothingMore(t *testing.T) {
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/half", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			panic("kaboom after the header")
		}, plugin.RouteSpec{Summary: "half", Scope: "read"})
	}
	e := newPluginEnv(t, nil, f)
	w := req{path: "/api/v1/plugins/aa/half"}.do(e.Handler())
	if w.Code != http.StatusTeapot || strings.Contains(w.Body.String(), "internal") {
		t.Errorf("answer = %d %s, want the 418 left alone", w.Code, w.Body)
	}
}

// An ActionError's body carries the request id the response header has, and a code outside the
// documented set (the OpenAPI enum) is replaced by the one the status implies.
func TestActionErrorBodyCarriesRequestIDAndADocumentedCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *plugin.ActionError
		code string
	}{
		{"a documented code stays", &plugin.ActionError{Status: http.StatusConflict, Code: model.ErrCodeConflict, Message: "busy"}, model.ErrCodeConflict},
		{"an unknown code becomes the status's", &plugin.ActionError{Status: http.StatusConflict, Code: "made_up_code", Message: "busy"}, model.ErrCodeConflict},
		{"an unknown code on a 5xx", &plugin.ActionError{Status: http.StatusServiceUnavailable, Code: "nope", Message: "later"}, model.ErrCodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newActionEnv(t, func(string, any) (any, error) { return nil, tc.err })
			w := e.post("ping", nil)
			got := apiError(t, w)
			if w.Code != tc.err.Status || got.Code != tc.code {
				t.Fatalf("got %d %+v, want %d with code %q", w.Code, got, tc.err.Status, tc.code)
			}
			if id := w.Header().Get(requestIDHeader); id == "" || got.RequestID != id {
				t.Errorf("requestId in the body %q, header %q", got.RequestID, id)
			}
		})
	}
}

// A pointer field's enum is checked against the value it points to, as its schema states.
func TestEnumOnAPointerFieldChecksTheValue(t *testing.T) {
	type in struct {
		Kind *string `json:"kind" dash:"enum=Deployment|StatefulSet"`
	}
	for _, tc := range []struct {
		kind string
		ok   bool
	}{{"Deployment", true}, {"StatefulSet", true}, {"DaemonSet", false}} {
		k := tc.kind
		msg := validateInput(reflect.ValueOf(&in{Kind: &k}))
		if tc.ok && msg != "" || !tc.ok && !strings.Contains(msg, "kind must be one of Deployment, StatefulSet") {
			t.Errorf("%q: %q", tc.kind, msg)
		}
	}
}
