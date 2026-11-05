package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// The README quick start (DASH_DEMO=true, nothing else) serves the embedded UI on
// http://127.0.0.1:8080, so the browser's writes carry that Origin and must pass the CSRF and
// Host checks. The Vite dev server's origin is dev mode's and is refused here.
func TestDemoQuickStartAcceptsWritesFromItsOwnOrigin(t *testing.T) {
	cleanEnv(t)
	fw, cfg := startDemo(t, "DASH_SERVER_PUBLIC_URL=") // empty: the demo default applies
	if cfg.PublicURL != "http://127.0.0.1:8080" {
		t.Fatalf("PublicURL = %q, want http://127.0.0.1:8080", cfg.PublicURL)
	}
	h := api.New(api.Deps{
		Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup,
		Auth: auth.Fixed(auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}),
	}).Handler()
	write := func(origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/plugins/alertmanager/actions/silence.create", strings.NewReader("{}"))
		r.RemoteAddr = "127.0.0.1:41000"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Dash-Csrf", "1")
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	code := func(w *httptest.ResponseRecorder) string {
		var e struct{ Code string }
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		return e.Code
	}
	if w := write("http://127.0.0.1:8080"); w.Code == http.StatusForbidden || w.Code == http.StatusMisdirectedRequest || code(w) == model.ErrCodeCSRFFailed {
		t.Errorf("write from http://127.0.0.1:8080 = %d %s, want past the Host and CSRF checks", w.Code, w.Body)
	}
	if w := write("http://localhost:5173"); code(w) != model.ErrCodeCSRFFailed {
		t.Errorf("write from the Vite origin = %d %s, want csrf_failed", w.Code, w.Body)
	}
}
