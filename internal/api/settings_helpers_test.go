package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// settingsEnv is a Server over a settingstest.Fake, with the audit log and the secret registry
// the two share. Every test ends by checking that no lock-requiring call ran outside WithLock
// (ruling 1).
type settingsEnv struct {
	f     *settingstest.Fake
	s     *Server
	h     http.Handler
	audit *audit.Log
	reg   *secret.Registry
}

// newSettingsEnv builds the env. The audit log is one without a file unless a mod sets
// Deps.Audit (withAudit); records() reads whichever the server got.
func newSettingsEnv(t *testing.T, o settingstest.Options, mods ...func(*Deps)) *settingsEnv {
	t.Helper()
	e := &settingsEnv{reg: o.Registry}
	if e.reg == nil {
		e.reg = secret.NewRegistry()
	}
	a, err := audit.Open("", audit.Options{Scrub: e.reg.Scrub})
	if err != nil {
		t.Fatal(err)
	}
	o.Registry = e.reg
	e.f = settingstest.New(t, o)
	e.s = newServer(func(d *Deps) {
		d.Settings, d.Secrets, d.Audit = e.f, e.reg, a
		for _, m := range mods {
			m(d)
		}
		e.audit = d.Audit
	})
	e.h = e.s.Handler()
	t.Cleanup(func() {
		if u := e.f.Unlocked(); len(u) != 0 {
			t.Errorf("calls outside WithLock: %v", u)
		}
	})
	return e
}

// withAudit gives the server a for its audit log.
func withAudit(a *audit.Log) func(*Deps) { return func(d *Deps) { d.Audit = a } }

// do sends one request as alice, the administrator.
func (e *settingsEnv) do(method, path string, body any, header map[string]string) *httptest.ResponseRecorder {
	return req{method: method, path: path, body: body, header: header}.do(e.h)
}

// put sends a write with If-Match naming the current revision.
func (e *settingsEnv) put(path string, body any) *httptest.ResponseRecorder {
	return e.do(http.MethodPut, path, body, e.ifMatch())
}

func (e *settingsEnv) ifMatch() map[string]string {
	return map[string]string{"If-Match": `"` + strconv.FormatUint(e.f.St.Revision(), 10) + `"`}
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return v
}

// settingsErr is the envelope of a refused settings request.
type settingsErr struct {
	Error   string                     `json:"error"`
	Code    string                     `json:"code"`
	Details model.SettingsErrorDetails `json:"details"`
}

func wantErr(t *testing.T, w *httptest.ResponseRecorder, status int, code string) settingsErr {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	e := decodeBody[settingsErr](t, w)
	if e.Code != code {
		t.Fatalf("code %q, want %q: %s", e.Code, code, w.Body.String())
	}
	return e
}

func wantOK(t *testing.T, w *httptest.ResponseRecorder) model.SettingsPatchResult {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return decodeBody[model.SettingsPatchResult](t, w)
}

// field finds path among fields.
func field(t *testing.T, fields []model.SettingField, path string) model.SettingField {
	t.Helper()
	for _, f := range fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no field %s", path)
	return model.SettingField{}
}

func (e *settingsEnv) pluginView(t *testing.T, id string) model.SettingsPluginView {
	t.Helper()
	w := e.do(http.MethodGet, "/api/v1/settings/plugins/"+id, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET plugin %s: %d %s", id, w.Code, w.Body.String())
	}
	return decodeBody[model.SettingsPluginView](t, w)
}

// records lists the main ring, newest first.
func (e *settingsEnv) records() []audit.Record {
	recs, _, _ := e.audit.Recent(500, "", audit.RingMain)
	return recs
}
