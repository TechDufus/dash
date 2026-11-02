package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// statusCodes is the table of design L.2 (contract §3.2): the codes an error of each status may
// carry. A status with several is told apart by what failed.
var statusCodes = map[int][]string{
	http.StatusBadRequest:          {model.ErrCodeInvalidInput},
	http.StatusUnauthorized:        {model.ErrCodeUnauthenticated},
	http.StatusForbidden:           {model.ErrCodeForbidden, model.ErrCodeCSRFFailed},
	http.StatusNotFound:            {model.ErrCodeNotFound},
	http.StatusConflict:            {model.ErrCodeConflict},
	http.StatusMisdirectedRequest:  {model.ErrCodeUnknownHost},
	http.StatusUnprocessableEntity: {model.ErrCodeInvalidInput},
	http.StatusTooManyRequests:     {model.ErrCodeRateLimited, model.ErrCodeTooManyStreams},
	http.StatusInternalServerError: {model.ErrCodeInternal},
	http.StatusBadGateway:          {model.ErrCodeUpstreamUnavailable},
	http.StatusServiceUnavailable:  {model.ErrCodeAuditUnavailable, model.ErrCodeTooManyStreams},
}

// Every error answer of the API names its reason, and the reason belongs to its status.
func TestEveryErrorPathHasACodeThatMatchesItsStatus(t *testing.T) {
	capped := func(s *Server) { s.events, s.tails = newStreamLimiter(0, 10), newStreamLimiter(0, 10) }
	full := func(s *Server) { s.events = newStreamLimiter(10, 0) }
	failProm := func(d *Deps) { d.Plugins = promPlugin(&fakeProm{err: context.DeadlineExceeded}) }
	podErr := func(err error) func(*Deps) { return func(d *Deps) { d.Data = fakeData{podErr: err} } }
	noHook := func(*harness) {}
	fullTails := func(s *Server) { s.tails = newStreamLimiter(10, 0) }

	for _, tc := range []struct {
		name   string
		req    req
		mod    func(*Deps)
		plug   func(*harness) // set: the request goes to a plugin harness (hopts) after plug adjusts it
		hopts  []func(*harnessOpts)
		srv    func(*Server)
		status int
		code   string
	}{
		{name: "unknown series range", req: req{path: "/api/v1/series?name=node.cpu&range=bad&node=cp1"}, status: 400, code: "invalid_input"},
		{name: "unknown series name", req: req{path: "/api/v1/series?name=nope&range=1h"}, status: 400, code: "invalid_input"},
		{name: "invalid series node", req: req{path: "/api/v1/series?name=node.cpu&range=1h&node=BAD_"}, status: 400, code: "invalid_input"},
		{name: "unknown series node", req: req{path: "/api/v1/series?name=node.cpu&range=1h&node=ghost"}, status: 404, code: "not_found"},
		{name: "series upstream failure", req: req{path: "/api/v1/series?name=node.cpu&range=1h&node=cp1"}, mod: failProm, status: 502, code: "upstream_unavailable"},
		{name: "invalid pod name", req: req{path: "/api/pods/apps/BAD_"}, status: 400, code: "invalid_input"},
		{name: "pod not found", req: req{path: "/api/pods/apps/missing"}, status: 404, code: "not_found"},
		{name: "pod not found upstream", req: req{path: "/api/pods/apps/web"}, mod: podErr(topics.ErrNotFound), status: 404, code: "not_found"},
		{name: "pod upstream failure", req: req{path: "/api/pods/apps/web"}, mod: podErr(context.DeadlineExceeded), status: 502, code: "upstream_unavailable"},
		{name: "plugin route input error", req: req{path: "/api/v1/plugins/rt/items?ns=media"}, plug: func(h *harness) {
			h.items = func(*http.Request) (any, error) { return nil, upstream.ErrInvalid }
		}, status: 400, code: "invalid_input"},
		{name: "plugin route upstream failure", req: req{path: "/api/v1/plugins/rt/items?ns=media"}, plug: func(h *harness) {
			h.items = func(*http.Request) (any, error) { return nil, context.DeadlineExceeded }
		}, status: 502, code: "upstream_unavailable"},
		{name: "plugin route in a denied namespace", req: req{path: "/api/v1/plugins/rt/items?ns=kube-system"}, plug: noHook,
			hopts: []func(*harnessOpts){harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{"kube-system"}})}, status: 403, code: "forbidden"},
		{name: "plugin stream without a target", req: req{path: "/api/v1/plugins/rt/tail"}, plug: noHook, status: 400, code: "invalid_input"},
		{name: "plugin stream upstream failure before it begins", req: req{path: "/api/v1/plugins/rt/tail?ns=media&pod=web"}, plug: func(h *harness) {
			h.tail = func(context.Context, *http.Request, auth.Principal, plugin.Stream) error {
				return context.DeadlineExceeded
			}
		}, status: 502, code: "upstream_unavailable"},
		{name: "plugin stream cap per user", req: req{path: "/api/v1/plugins/rt/tail?ns=media&pod=web"}, plug: noHook, srv: capped, status: 429, code: "too_many_streams"},
		{name: "plugin stream cap overall", req: req{path: "/api/v1/plugins/rt/tail?ns=media&pod=web"}, plug: noHook, srv: fullTails, status: 503, code: "too_many_streams"},
		{name: "invalid topic name", req: req{path: "/api/v1/topics/BAD"}, status: 400, code: "invalid_input"},
		{name: "unknown topic", req: req{path: "/api/v1/topics/cluster"}, status: 404, code: "not_found"},
		{name: "invalid stream filter", req: req{path: "/api/v1/stream?topics=BAD!"}, status: 400, code: "invalid_input"},
		{name: "stream cap per user", req: req{path: "/api/v1/stream"}, srv: capped, status: 429, code: "too_many_streams"},
		{name: "stream cap overall", req: req{path: "/api/v1/stream"}, srv: full, status: 503, code: "too_many_streams"},
		{name: "restart body", req: req{method: "POST", path: "/api/actions/restart", body: "{"}, status: 400, code: "invalid_input"},
		{name: "restart kind", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Pod", Namespace: "apps", Name: "web"}}, status: 400, code: "invalid_input"},
		{name: "restart name", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "BAD_"}}, status: 400, code: "invalid_input"},
		{name: "restart in a missing namespace", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "nope", Name: "web"}}, status: 404, code: "not_found"},
		{name: "restart without the name typed", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web"}}, status: 400, code: "invalid_input"},
		{name: "reconcile kind", req: req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Pod", Namespace: "flux-system", Name: "app"}}, status: 400, code: "invalid_input"},
		{name: "plugin action input", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/restart", body: map[string]string{"namespace": "apps"}}, plug: noHook, status: 400, code: "invalid_input"},
		{name: "plugin action refusal", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.expire", body: expireIn{ID: "bad"}}, plug: func(h *harness) {
			h.act = func(string, any) (any, error) {
				return nil, &plugin.ActionError{Status: 400, Code: model.ErrCodeInvalidInput, Message: "not a silence id"}
			}
		}, status: 400, code: "invalid_input"},
		{name: "plugin action upstream failure", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/ping"}, plug: func(h *harness) {
			h.act = func(string, any) (any, error) { return nil, context.DeadlineExceeded }
		}, status: 502, code: "upstream_unavailable"},
		{name: "csrf", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/ping", header: map[string]string{"X-Dash-Csrf": ""}}, plug: noHook, status: 403, code: "csrf_failed"},
		{name: "unauthenticated", req: req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}, status: 401, code: "unauthenticated"},
		{name: "unknown host", req: req{path: "/api/v1/me", host: "evil.example"}, status: 421, code: "unknown_host"},
		{name: "unknown API path", req: req{path: "/api/nope"}, status: 404, code: "not_found"},
		{name: "method not allowed on a known path", req: req{method: "DELETE", path: "/api/v1/me"}, status: 404, code: "not_found"},
		{name: "unknown plugin route", req: req{path: "/api/v1/plugins/ghost/x"}, status: 404, code: "not_found"},
		{name: "unknown plugin action", req: req{method: "POST", path: "/api/v1/plugins/ghost/actions/x"}, status: 404, code: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s *Server
			if tc.plug != nil {
				h := newHarness(t, tc.hopts...)
				tc.plug(h)
				s = h.Server
			} else {
				s, _, _ = serverWithFakes(tc.mod)
			}
			if tc.srv != nil {
				tc.srv(s)
			}
			w := tc.req.do(s.Handler())
			if w.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body, tc.status)
			}
			e := apiError(t, w)
			if e.Error == "" || e.Code != tc.code {
				t.Errorf("error = %+v, want code %q and a message", e, tc.code)
			}
			if !slices.Contains(statusCodes[w.Code], e.Code) {
				t.Errorf("code %q is not one of %v for status %d", e.Code, statusCodes[w.Code], w.Code)
			}
		})
	}
}

func TestKubeStatusCodes(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusForbidden:           model.ErrCodeForbidden,
		http.StatusNotFound:            model.ErrCodeNotFound,
		http.StatusConflict:            model.ErrCodeConflict,
		http.StatusUnprocessableEntity: model.ErrCodeInvalidInput,
	} {
		if got := kubeErrCode(status); got != want {
			t.Errorf("kubeErrCode(%d) = %q, want %q", status, got, want)
		}
		if !slices.Contains(statusCodes[status], want) {
			t.Errorf("%d: %q is not in the status table", status, want)
		}
	}
}
