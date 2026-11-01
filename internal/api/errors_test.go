package api

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
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
	failProm := func(d *Deps) { d.Prom = &fakeProm{err: context.DeadlineExceeded} }
	vmErr := func(err error) func(*Deps) { return func(d *Deps) { d.Data = fakeData{vmErr: err} } }
	logsErr := func(err error) func(*Deps) { return func(d *Deps) { d.VLogs = &fakeLogs{err: err} } }

	for _, tc := range []struct {
		name   string
		req    req
		mod    func(*Deps)
		srv    func(*Server)
		status int
		code   string
	}{
		{name: "unknown series range", req: req{path: "/api/v1/series?name=host.cpu&range=bad"}, status: 400, code: "invalid_input"},
		{name: "unknown series name", req: req{path: "/api/v1/series?name=nope&range=1h"}, status: 400, code: "invalid_input"},
		{name: "invalid series node", req: req{path: "/api/v1/series?name=node.cpu&range=1h&node=BAD_"}, status: 400, code: "invalid_input"},
		{name: "unknown series node", req: req{path: "/api/v1/series?name=node.cpu&range=1h&node=ghost"}, status: 404, code: "not_found"},
		{name: "series upstream failure", req: req{path: "/api/v1/series?name=host.cpu&range=1h"}, mod: failProm, status: 502, code: "upstream_unavailable"},
		{name: "invalid vmid", req: req{path: "/api/vms/abc"}, status: 400, code: "invalid_input"},
		{name: "vm not found", req: req{path: "/api/vms/100"}, mod: vmErr(topics.ErrNotFound), status: 404, code: "not_found"},
		{name: "vm upstream failure", req: req{path: "/api/vms/100"}, mod: vmErr(context.DeadlineExceeded), status: 502, code: "upstream_unavailable"},
		{name: "invalid pod name", req: req{path: "/api/pods/apps/BAD_"}, status: 400, code: "invalid_input"},
		{name: "pod not found", req: req{path: "/api/pods/apps/missing"}, status: 404, code: "not_found"},
		{name: "invalid log query", req: req{path: "/api/logs?ns=media&pod=web"}, mod: logsErr(upstream.ErrInvalid), status: 400, code: "invalid_input"},
		{name: "logs upstream failure", req: req{path: "/api/logs?ns=media&pod=web"}, mod: logsErr(context.DeadlineExceeded), status: 502, code: "upstream_unavailable"},
		{name: "log tail without a target", req: req{path: "/api/logs/tail"}, status: 400, code: "invalid_input"},
		{name: "log tail cap", req: req{path: "/api/logs/tail?ns=media&pod=web"}, srv: capped, status: 429, code: "too_many_streams"},
		{name: "invalid topic name", req: req{path: "/api/v1/topics/BAD"}, status: 400, code: "invalid_input"},
		{name: "unknown topic", req: req{path: "/api/v1/topics/vms"}, status: 404, code: "not_found"},
		{name: "invalid stream filter", req: req{path: "/api/v1/stream?topics=BAD!"}, status: 400, code: "invalid_input"},
		{name: "stream cap per user", req: req{path: "/api/v1/stream"}, srv: capped, status: 429, code: "too_many_streams"},
		{name: "stream cap overall", req: req{path: "/api/v1/stream"}, srv: full, status: 503, code: "too_many_streams"},
		{name: "restart body", req: req{method: "POST", path: "/api/actions/restart", body: "{"}, status: 400, code: "invalid_input"},
		{name: "restart kind", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Pod", Namespace: "apps", Name: "web"}}, status: 400, code: "invalid_input"},
		{name: "restart name", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "BAD_"}}, status: 400, code: "invalid_input"},
		{name: "restart in a missing namespace", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "nope", Name: "web"}}, status: 404, code: "not_found"},
		{name: "restart without the name typed", req: req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web"}}, status: 400, code: "invalid_input"},
		{name: "reconcile kind", req: req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Pod", Namespace: "flux-system", Name: "app"}}, status: 400, code: "invalid_input"},
		{name: "silence without matchers", req: req{method: "POST", path: "/api/silences", body: model.SilenceRequest{Duration: "1h", Comment: "x"}}, status: 400, code: "invalid_input"},
		{name: "expire an invalid silence", req: req{method: "DELETE", path: "/api/silences/bad"}, status: 400, code: "invalid_input"},
		{name: "csrf", req: req{method: "POST", path: "/api/silences", header: map[string]string{"X-Dash-Csrf": ""}}, status: 403, code: "csrf_failed"},
		{name: "unauthenticated", req: req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}, status: 401, code: "unauthenticated"},
		{name: "unknown host", req: req{path: "/api/v1/me", host: "evil.example"}, status: 421, code: "unknown_host"},
		{name: "unknown API path", req: req{path: "/api/nope"}, status: 404, code: "not_found"},
		{name: "method not allowed on a known path", req: req{method: "DELETE", path: "/api/v1/me"}, status: 404, code: "not_found"},
		{name: "unknown plugin route", req: req{path: "/api/v1/plugins/ghost/x"}, status: 404, code: "not_found"},
		{name: "unknown plugin action", req: req{method: "POST", path: "/api/v1/plugins/ghost/actions/x"}, status: 404, code: "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, am := serverWithFakes(tc.mod)
			am.err = nil
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
