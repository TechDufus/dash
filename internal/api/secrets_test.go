package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/model"
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
		want string
	}{
		{"series", req{path: "/api/v1/series?name=host.cpu&range=1h"}, func(d *Deps) { d.Prom = &fakeProm{err: refused} }, "metrics unavailable: unreachable"},
		{"logs", req{path: "/api/logs?ns=media"}, func(d *Deps) { d.VLogs = &fakeLogs{err: denied} }, "logs unavailable: auth"},
		{"vm detail", req{path: "/api/vms/100"}, func(d *Deps) { d.Data = fakeData{vmErr: unavailable} }, "vm details unavailable: http_503"},
		{"silence create", req{method: "POST", path: "/api/silences", body: silenceBody(nil)}, func(d *Deps) { d.AM = &fakeAM{err: refused} }, "alertmanager: unreachable"},
		{"silence expire", req{method: "DELETE", path: "/api/silences/abc"}, func(d *Deps) { d.AM = &fakeAM{err: unavailable} }, "alertmanager: http_503"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logged := captureLog(t, reg)
			s := newServer(func(d *Deps) { tc.mod(d); d.Secrets = reg })
			w := tc.req.do(s.Handler())
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
// echoes (P1.4 acceptance). Through the real restart action, so removing the Scrub fails it.
func TestKubePolicyMessagesAreScrubbedOnTheWayOut(t *testing.T) {
	const pat = "dash_pat_abcdefghij0123456789ABCDEFGHIJ0123456789abc"
	msg := "homelab guardrail: denied; webhook echoed " + strings.Join(canaryForms(), " ") + " and " + pat
	for _, code := range []int32{403, 404, 409, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			reg := canaryRegistry()
			kc := fake.NewClientset(workloadObjects()...)
			kerr := &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: code, Message: msg}}
			kc.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, kerr })
			s := newServer(func(d *Deps) { d.Kube = kc; d.Secrets = reg })
			w := req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}}.do(s.Handler())
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

// Log lines are application output: whatever a pod prints, a credential never reaches the
// browser, as an exact value, in an encoded form, or as a dash token (P1.4 acceptance).
func TestLogsAPIScrubsSecretsAndTokens(t *testing.T) {
	reg := canaryRegistry()
	var msgs []string
	for i, form := range canaryForms() {
		msgs = append(msgs, fmt.Sprintf("line %d token=%s end", i, form))
	}
	msgs = append(msgs,
		"pat dash_pat_abcDEF123_secretPART456 end",
		"setup dash_setup_Zm9vYmFyYmF6 end",
		"access dash_at_abc.def end",
		"sealed enc:v1:1a2b3c4d:AAAAAAAAAAAAAAAA end",
		"plain line stays",
	)
	var lines []model.LogLine
	for i, m := range msgs {
		lines = append(lines, model.LogLine{Time: fixedNow.Add(time.Duration(i) * time.Second), Pod: "p", Container: "c", Msg: m})
	}
	forbidden := append(canaryForms(), "dash_pat_abcDEF123", "secretPART456", "dash_setup_Zm9v", "dash_at_abc", "enc:v1:1a2b3c4d", "AAAAAAAAAAAAAAAA")

	check := func(t *testing.T, name, out string) {
		t.Helper()
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Errorf("%s carries %q:\n%s", name, f, out)
			}
		}
		if !strings.Contains(out, "plain line stays") || strings.Count(out, "[redacted]") < len(msgs)-1 {
			t.Errorf("%s: want every secret line redacted and the plain one kept:\n%s", name, out)
		}
	}

	t.Run("query", func(t *testing.T) {
		logs := &fakeLogs{lines: lines}
		s := newServer(func(d *Deps) { d.VLogs, d.Secrets = logs, reg })
		w := req{path: "/api/logs?ns=media&pod=p"}.do(s.Handler())
		if w.Code != 200 {
			t.Fatalf("code = %d", w.Code)
		}
		check(t, "/api/logs", w.Body.String())
	})
	t.Run("tail", func(t *testing.T) {
		logs := &fakeLogs{tail: func(emit func(model.LogLine) error) error {
			for _, l := range lines {
				if err := emit(l); err != nil {
					return err
				}
			}
			return nil
		}}
		s := newServer(func(d *Deps) { d.VLogs, d.Secrets = logs, reg })
		_, stream := sseClient(t, s.Handler(), "/api/logs/tail?ns=media&pod=p")
		var out strings.Builder
		for l := range stream {
			out.WriteString(l + "\n")
		}
		check(t, "/api/logs/tail", out.String())
	})
}
