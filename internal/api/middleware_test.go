package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

func TestCSRF(t *testing.T) {
	body := model.ReconcileRequest{Kind: "Nope", Namespace: "flux-system", Name: "x"} // reaches the handler, answers 400
	tests := []struct {
		name   string
		method string
		header map[string]string
		raw    bool
		code   int
	}{
		{"missing csrf header", "POST", map[string]string{"X-Dash-Csrf": ""}, false, 403},
		{"no csrf and no origin", "POST", nil, true, 403},
		{"wrong csrf value", "POST", map[string]string{"X-Dash-Csrf": "0"}, false, 403},
		{"wrong origin", "POST", map[string]string{"Origin": "https://evil.example"}, false, 403},
		{"missing origin", "POST", map[string]string{"Origin": ""}, false, 403},
		{"origin with path", "POST", map[string]string{"Origin": testOrigin + "/x"}, false, 403},
		{"both correct", "POST", nil, false, 400},
		{"delete wrong origin", "DELETE", map[string]string{"Origin": "https://evil.example"}, false, 403},
		{"get unaffected", "GET", nil, true, 404}, // no GET route here; the CSRF gate must not answer 403
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := req{method: tc.method, path: "/api/actions/reconcile", body: body, header: tc.header, raw: tc.raw}.do(newServer(nil).Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.code == 403 {
				if got := decodeErr(t, w); got != "csrf check failed" {
					t.Errorf("error = %q", got)
				}
				if ct := w.Header().Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q", ct)
				}
			}
		})
	}
}

func TestUserResolution(t *testing.T) {
	tests := []struct {
		name   string
		dev    bool
		header map[string]string
		code   int
		user   string
	}{
		{"no headers prod", false, map[string]string{"X-Forwarded-Preferred-Username": ""}, 401, ""},
		{"preferred username", false, map[string]string{"X-Forwarded-Preferred-Username": "alice", "X-Forwarded-User": "sub-123"}, 200, "alice"},
		{"falls back to forwarded user", false, map[string]string{"X-Forwarded-Preferred-Username": "", "X-Forwarded-User": "bob"}, 200, "bob"},
		{"dev default", true, map[string]string{"X-Forwarded-Preferred-Username": ""}, 200, "dev"},
		{"dev header wins", true, map[string]string{"X-Forwarded-Preferred-Username": "carol"}, 200, "carol"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(func(d *Deps) { d.Cfg = config.Config{Dev: tc.dev, PublicURL: testOrigin}; d.Version = "v9" })
			w := req{path: "/api/me", header: tc.header}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d", w.Code, tc.code)
			}
			if tc.code == 401 {
				if decodeErr(t, w) == "" {
					t.Error("empty error")
				}
				return
			}
			var me model.Me
			if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
				t.Fatal(err)
			}
			if me.User != tc.user || me.Version != "v9" {
				t.Errorf("me = %+v", me)
			}
		})
	}

	t.Run("every api path is gated, including stream and unknown", func(t *testing.T) {
		h := newServer(nil).Handler()
		for _, p := range []string{"/api/stream", "/api/series", "/api/logs/tail", "/api/unknown", "/api/vms/1"} {
			w := req{path: p, header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s: code = %d, want 401", p, w.Code)
			}
		}
	})

	t.Run("healthz is unaffected", func(t *testing.T) {
		w := req{path: "/healthz", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(newServer(nil).HealthHandler())
		if w.Code != 200 || w.Body.String() != "ok" {
			t.Errorf("code = %d body = %q", w.Code, w.Body)
		}
	})
}

func TestHostCheck(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()

	w := req{path: "/api/me", host: "evil.example"}.do(h)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("unknown host: code = %d, want 421", w.Code)
	}
	if msg := decodeErr(t, w); !strings.Contains(msg, "DASH_ALLOWED_HOSTS") {
		t.Errorf("unknown host: error = %q", msg)
	}

	if w := (req{path: "/api/me"}).do(h); w.Code != http.StatusOK {
		t.Errorf("default host: code = %d, want 200", w.Code)
	}
	if w := (req{path: "/api/me", host: "DASH.example.com:443"}).do(h); w.Code != http.StatusOK {
		t.Errorf("public host with port and case: code = %d, want 200", w.Code)
	}

	// The health listener is probed by IP, so it never checks Host.
	if w := (req{path: "/healthz", host: "192.0.2.10:8081"}).do(s.HealthHandler()); w.Code != http.StatusOK {
		t.Errorf("health handler with an unlisted host: code = %d, want 200", w.Code)
	}
}
