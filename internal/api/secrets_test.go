package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// canary has characters that change under URL encoding, so every form of it differs.
const canary = "canary/secret+value=1234"

// canaryForms is the canary as it can show up in an upstream's text.
func canaryForms() []string {
	return []string{
		canary,
		base64.StdEncoding.EncodeToString([]byte(canary)),
		base64.RawURLEncoding.EncodeToString([]byte(canary)),
		url.QueryEscape(canary),
		url.PathEscape(canary),
	}
}

func canaryRegistry() *secret.Registry {
	reg := secret.NewRegistry()
	reg.Add(secret.New(canary, ""))
	return reg
}

// captureLog routes the default logger through the redacting handler into a buffer.
func captureLog(t *testing.T, reg *secret.Registry) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(secret.NewHandler(slog.NewTextHandler(buf, nil), reg)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// An upstream error carries URLs, addresses and sometimes credentials. API errors built from
// it name the category only; the detail goes to the log, scrubbed (P1.4 acceptance).
func TestUpstreamErrorsReachClientsAsCategoryOnly(t *testing.T) {
	reg := canaryRegistry()
	detail := "Post \"http://vm.internal.example.com:8428/api/v1/query_range\": dial tcp 192.0.2.7:8428: connect: connection refused; sent " + canary
	refused := errors.New(detail)
	denied := &upstream.StatusError{Code: 403, Body: "token " + canary + " rejected by vm.internal.example.com"}
	unavailable := &upstream.StatusError{Code: 503, Body: "backend 192.0.2.7 down"}

	tests := []struct {
		name string
		req  req
		mod  func(*Deps)
		plug func(*harness) // set: the request goes to a plugin harness instead of newServer
		want string
	}{
		{name: "series", req: req{path: "/api/v1/series?name=sr.cpu&range=1h"}, mod: func(d *Deps) { d.Plugins = promPlugin(&fakeProm{err: refused}) }, want: "metrics unavailable: unreachable"},
		{name: "plugin route", req: req{path: "/api/v1/plugins/rt/items?ns=media"}, plug: func(h *harness) {
			h.items = func(*http.Request) (any, error) { return nil, denied }
		}, want: "rt: auth"},
		{name: "plugin stream before it begins", req: req{path: "/api/v1/plugins/rt/tail?ns=media&pod=p"}, plug: func(h *harness) {
			h.tail = func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { return denied }
		}, want: "rt: auth"},
		{name: "plugin action", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.create", body: silenceIn{Alertname: "HighTemp"}}, plug: func(h *harness) {
			h.act = func(string, any) (any, error) { return nil, refused }
		}, want: "aa: unreachable"},
		{name: "plugin action 503", req: req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.expire", body: expireIn{ID: "abc"}}, plug: func(h *harness) {
			h.act = func(string, any) (any, error) { return nil, unavailable }
		}, want: "aa: http_503"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t, reg)
			var handler http.Handler
			if tc.plug != nil {
				h := newHarness(t)
				tc.plug(h)
				h.d.Secrets = reg
				handler = h.Handler()
			} else {
				handler = newServer(func(d *Deps) { tc.mod(d); d.Secrets = reg }).Handler()
			}
			w := tc.req.do(handler)
			if w.Code != 502 {
				t.Fatalf("code = %d, want 502 (%s)", w.Code, w.Body)
			}
			if got := decodeErr(t, w); got != tc.want {
				t.Errorf("error = %q, want %q", got, tc.want)
			}
			body := bodyWithoutRequestID(w)
			for _, leak := range append(canaryForms(), "example.com", "192.0.2.7", "8428") {
				if strings.Contains(body, leak) {
					t.Errorf("response carries %q: %s", leak, w.Body)
				}
			}
			out := logged.String()
			if !strings.Contains(out, "upstream request failed") || !strings.Contains(out, "category=") {
				t.Errorf("the detail should be logged with its category:\n%s", out)
			}
			for _, form := range canaryForms() {
				if strings.Contains(out, form) {
					t.Errorf("the log carries the secret form %q:\n%s", form, out)
				}
			}
		})
	}
}

// bodyWithoutRequestID is the response body without its request id, which is random hex: a
// short canary such as a port number could otherwise match it by chance.
func bodyWithoutRequestID(w *httptest.ResponseRecorder) string {
	id := w.Header().Get("X-Request-Id")
	if id == "" {
		return w.Body.String()
	}
	return strings.ReplaceAll(w.Body.String(), id, "")
}

// Kubernetes 403/404/409/422 stay verbatim (an admission denial must reach the user); other
// Kubernetes failures are a category.
func TestKubeErrorsKeepPolicyMessagesAndCategoriseTheRest(t *testing.T) {
	s := newServer(func(d *Deps) { d.Secrets = canaryRegistry() })
	gr := schema.GroupResource{Resource: "deployments"}

	w := httptest.NewRecorder()
	s.writeKubeErr(w, apierrors.NewForbidden(gr, "plex", errors.New("guardrail: protected namespace")))
	if w.Code != 403 || !strings.Contains(decodeErr(t, w), "guardrail: protected namespace") {
		t.Errorf("forbidden: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	s.writeKubeErr(w, apierrors.NewInternalError(fmt.Errorf("etcd at https://192.0.2.9:2379 timed out, token %s", canary)))
	if w.Code != 502 || decodeErr(t, w) != "kubernetes: http_500" {
		t.Errorf("internal error: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	s.writeKubeErr(w, fmt.Errorf("dial tcp 192.0.2.9:6443: connect: connection refused"))
	if w.Code != 502 || decodeErr(t, w) != "kubernetes: unreachable" {
		t.Errorf("unreachable: %d %s", w.Code, w.Body)
	}
}

// The four Kubernetes codes that keep their message are the one error path that passes upstream
// text to the browser; the scrubbing there is what stops a credential an admission webhook
// echoes (P1.4 acceptance). Through the real reconcile action, so removing the Scrub fails it.
func TestKubePolicyMessagesAreScrubbedOnTheWayOut(t *testing.T) {
	const pat = "dash_pat_abcdefghij0123456789ABCDEFGHIJ0123456789abc"
	msg := "homelab guardrail: denied; webhook echoed " + strings.Join(canaryForms(), " ") + " and " + pat
	gvr := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	for _, code := range []int32{403, 404, 409, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			reg := canaryRegistry()
			dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "KustomizationList"},
				fluxObject(gvr, "Kustomization"))
			kerr := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: code, Message: msg}}
			dyn.PrependReactor("patch", "kustomizations", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, kerr })
			s := newServer(func(d *Deps) { withCluster(dyn)(d); d.Secrets = reg })
			w := req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}}.do(s.Handler())
			if w.Code != int(code) {
				t.Fatalf("code = %d, want %d (%s)", w.Code, code, w.Body)
			}
			if !strings.Contains(w.Body.String(), "homelab guardrail: denied") {
				t.Errorf("the policy message must still reach the user: %s", w.Body)
			}
			for _, leak := range append(canaryForms(), pat) {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("response carries %q: %s", leak, w.Body)
				}
			}
		})
	}
}

// The refusal a plugin action makes is its own text and may carry what an upstream echoed (a
// Kubernetes admission webhook behind the Flux reconcile will, from the flux plugin on), so the
// core scrubs it on the way out whatever the status: the home of the check above once the legacy
// route is gone.
func TestPluginActionRefusalsAreScrubbedOnTheWayOut(t *testing.T) {
	const pat = "dash_pat_abcdefghij0123456789ABCDEFGHIJ0123456789abc"
	msg := "homelab guardrail: denied; webhook echoed " + strings.Join(canaryForms(), " ") + " and " + pat
	for _, status := range []int{400, 403, 404, 409, 422} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newHarness(t)
			h.Server.d.Secrets = canaryRegistry()
			h.act = func(string, any) (any, error) { return nil, &plugin.ActionError{Status: status, Message: msg} }
			w := req{method: "POST", path: "/api/v1/plugins/aa/actions/ping"}.do(h.Handler())
			if w.Code != status {
				t.Fatalf("code = %d, want %d (%s)", w.Code, status, w.Body)
			}
			if !strings.Contains(w.Body.String(), "homelab guardrail: denied") {
				t.Errorf("the policy message must still reach the user: %s", w.Body)
			}
			for _, leak := range append(canaryForms(), pat) {
				if strings.Contains(w.Body.String(), leak) {
					t.Errorf("response carries %q: %s", leak, w.Body)
				}
			}
		})
	}
}
