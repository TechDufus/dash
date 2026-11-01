package api

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

// The namespace policy and the error answers of the GET routes of plugins, through the real chain.

func TestPluginRouteNamespacePolicy(t *testing.T) {
	h := newHarness(t, harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{"kube-system"}}))
	var ran atomic.Int32
	h.items = func(*http.Request) (any, error) { ran.Add(1); return []string{"x"}, nil }

	for _, tc := range []struct {
		name, path string
		status     int
	}{
		{"a denied namespace", "/api/v1/plugins/rt/items?ns=kube-system", 403},
		{"the alias parameter is the same namespace", "/api/v1/plugins/rt/items?namespace=kube-system", 403},
		{"a path value, which the router has set by then", "/api/v1/plugins/rt/ns/kube-system/items", 403},
		{"an allowed namespace", "/api/v1/plugins/rt/items?ns=media", 200},
		{"a path value that is allowed", "/api/v1/plugins/rt/ns/media/items", 200},
		{"no namespace at all", "/api/v1/plugins/rt/items", 200},
		{"a namespace that only contains the denied one", "/api/v1/plugins/rt/items?ns=kube-system-2", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := ran.Load()
			w := h.get(tc.path)
			if w.Code != tc.status {
				t.Fatalf("%s = %d %s, want %d", tc.path, w.Code, w.Body, tc.status)
			}
			if tc.status == 403 {
				e := wantError(t, w, 403, model.ErrCodeForbidden)
				if e.RequestID == "" || e.RequestID != w.Header().Get("X-Request-Id") {
					t.Errorf("requestId %q, header %q", e.RequestID, w.Header().Get("X-Request-Id"))
				}
				if ran.Load() != before {
					t.Error("the handler ran for a denied namespace")
				}
			} else if ran.Load() != before+1 {
				t.Error("the handler did not run for an allowed request")
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store under /api/v1", cc)
			}
		})
	}

	// Three refusals within the minute, one record; each one counted.
	recs := recordsOf(h.audit(t), audit.KindDenial)
	if len(recs) != 1 || recs[0].Reason != obs.AuthNamespaceDenied.String() || recs[0].Actor.User != "alice" {
		t.Errorf("denials = %+v, want one namespace_denied for alice", recs)
	}
	if v, _ := seriesValue(t, h.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "namespace_denied"}); v != 3 {
		t.Errorf("namespace_denied counted %v, want 3", v)
	}
	h.clk.advance(time.Minute + time.Second)
	h.get("/api/v1/plugins/rt/items?ns=kube-system")
	if n := len(recordsOf(h.audit(t), audit.KindDenial)); n != 2 {
		t.Errorf("denials after the window = %d, want 2", n)
	}
}

// ReadDenyNamespaces belongs to the instance: another instance's list does not apply, and a
// route without RouteSpec.Namespace is never refused.
func TestPluginRouteNamespacePolicyIsPerInstanceAndPerRoute(t *testing.T) {
	h := newHarness(t,
		harnessAA(plugin.InstanceConfig{ActionsEnabled: true, ReadDenyNamespaces: []string{"media"}}),
		harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{"kube-system"}}),
		harnessRoutes(func(h *harness, r plugin.Routes) {
			r.Get("/blind", h.serveItems, plugin.RouteSpec{Summary: "No namespace declared", Scope: "read"})
		}),
	)
	if w := h.get("/api/v1/plugins/rt/blind?ns=kube-system"); w.Code != http.StatusOK {
		t.Errorf("a route that declares no namespace = %d, want 200", w.Code)
	}
	if w := h.get("/api/v1/plugins/rt/blind?ns=media"); w.Code != http.StatusOK {
		t.Errorf("the list of aa applied to rt: %d", w.Code)
	}
}

func TestPluginFailHidesWhatTheUpstreamSaidAndWritesARefusalAsItIs(t *testing.T) {
	const canaryText = "tok3n-s3cr3t-value"
	reg := secret.NewRegistry()
	reg.Add(secret.New(canaryText, ""))
	logged := captureLog(t, reg)

	h := newHarness(t)
	h.d.Secrets = reg
	h.items = func(r *http.Request) (any, error) {
		switch r.URL.Query().Get("q") {
		case "refusal":
			return nil, &plugin.ActionError{Status: http.StatusConflict, Code: model.ErrCodeConflict, Message: "busy: " + canaryText + " held it", Details: map[string]string{"by": "someone"}}
		case "badcode":
			return nil, &plugin.ActionError{Status: http.StatusConflict, Code: "made_up", Message: "busy"}
		}
		return nil, errors.New(`Get "https://vm.internal.example.com:8428/select": dial tcp 192.0.2.7:8428: connect: connection refused; token ` + canaryText)
	}

	// Anything but an ActionError: the category, never the URL, host, port or secret.
	w := h.get("/api/v1/plugins/rt/items?ns=media")
	e := wantError(t, w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable)
	if e.Error != "rt: unreachable" {
		t.Errorf("message = %q, want the category only", e.Error)
	}
	for _, leak := range []string{canaryText, "example.com", "192.0.2.7", "8428", "vm.internal"} {
		if strings.Contains(bodyWithoutRequestID(w), leak) {
			t.Errorf("body carries %q: %s", leak, w.Body)
		}
	}
	out := logged.String()
	if !strings.Contains(out, "upstream request failed") || strings.Contains(out, canaryText) {
		t.Errorf("the detail must be logged, scrubbed:\n%s", out)
	}

	// An ActionError: status, code and details as the plugin wrote them, the message scrubbed.
	w = h.get("/api/v1/plugins/rt/items?ns=media&q=refusal")
	e = wantError(t, w, http.StatusConflict, model.ErrCodeConflict)
	if strings.Contains(w.Body.String(), canaryText) || !strings.HasPrefix(e.Error, "busy: ") || !strings.Contains(e.Error, " held it") {
		t.Errorf("message = %q, want it scrubbed but kept", e.Error)
	}
	if d, ok := e.Details.(map[string]any); !ok || d["by"] != "someone" {
		t.Errorf("details = %#v", e.Details)
	}
	// A code outside the documented set becomes the status's own, as for an action.
	if e := wantError(t, h.get("/api/v1/plugins/rt/items?ns=media&q=badcode"), http.StatusConflict, model.ErrCodeConflict); e.Code != model.ErrCodeConflict {
		t.Errorf("code = %q", e.Code)
	}
}

func TestPluginRoutePanicIsTheRecovered500(t *testing.T) {
	h := newHarness(t)
	wantInternal(t, "GET boom", h.get("/api/v1/plugins/rt/boom"))
	// The server carries on.
	if w := h.get("/api/v1/plugins/rt/items?ns=media"); w.Code != http.StatusOK {
		t.Errorf("after the panic: %d %s", w.Code, w.Body)
	}
	if v, _ := seriesValue(t, h.scrape(t), "dash_http_requests_total", map[string]string{"route": "GET /api/v1/plugins/{id}/{rest...}", "code": "500"}); v != 1 {
		t.Errorf("500s counted under the plugin route label = %v, want 1", v)
	}
}
