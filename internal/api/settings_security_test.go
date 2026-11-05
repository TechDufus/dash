package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/model"
)

// A k8s: reference from the UI may not name a Secret key that the operator's configuration (the
// environment or the file, any field of any plugin) already references: through the secrets
// endpoint, a patch and a connection test it is a 409 naming the UI path, with a fixed text. The
// same Secret with another key, or another Secret, still saves.
func TestUIK8sRefToAnOperatorSecretIsLocked(t *testing.T) {
	for name, o := range map[string]settingstest.Options{
		"env":  {Env: []string{"DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=k8s:dash/pve#token"}},
		"file": {File: "plugins:\n  proxmox:\n    url: https://pve.example.com:8006\n    tokenID: u@pve!t\n    tokenSecret: k8s:dash/pve#token\n"},
	} {
		t.Run(name, func(t *testing.T) {
			checks, seen := capture()
			o.SecretKey, o.Namespace, o.Checker = true, "dash", checks
			o.Stored = map[string]any{"plugins.gatus.url": "https://gatus.example.com"}
			e := newSettingsEnv(t, o)
			for _, w := range []struct {
				label string
				do    func() *httptest.ResponseRecorder
			}{
				{"secrets endpoint", func() *httptest.ResponseRecorder {
					return e.put(bearerPath, map[string]any{"ref": "k8s:dash/pve#token"})
				}},
				{"secrets endpoint, padded", func() *httptest.ResponseRecorder {
					return e.put(bearerPath, map[string]any{"ref": "  k8s:dash/pve#token "})
				}},
				{"patch", func() *httptest.ResponseRecorder {
					return e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://evil.example.com", "auth": map[string]any{"bearerToken": "k8s:dash/pve#token"}})
				}},
				{"test", func() *httptest.ResponseRecorder {
					return e.test("gatus", map[string]any{"patch": map[string]any{"url": "https://evil.example.com"}, "secrets": map[string]any{"auth.bearerToken": "k8s:dash/pve#token"}})
				}},
			} {
				got := wantErr(t, w.do(), http.StatusConflict, model.ErrCodeLockedField)
				if got.Error != msgOperatorRef || got.Details.Path != gatusBearer {
					t.Errorf("%s: %+v", w.label, got)
				}
			}
			if s := seen(); len(s) != 0 {
				t.Errorf("a refused test reached the check: %q", s)
			}
			if e.f.St.Revision() != 1 {
				t.Errorf("a refused reference was stored: revision %d", e.f.St.Revision())
			}
			wantOK(t, e.put(bearerPath, map[string]any{"ref": "k8s:dash/pve#other"}))
			wantOK(t, e.put(bearerPath, map[string]any{"ref": "k8s:dash/gatus#token"}))
		})
	}
}

// A URL with userinfo (user, or user:password) is refused on every UI write and test with a
// fixed text at its path: in a plugin's url, a launcher link's href, ui.iconBaseURL. Credentials
// belong in the secret fields; a plain field is stored and shown in clear. What the answer says
// never repeats the URL.
func TestURLCredentialsAreRefusedFromTheUI(t *testing.T) {
	const canary = "URLPW-canary-5c1e"
	checks, seen := capture()
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Checker: checks})
	for _, c := range []struct {
		label, method, path, field string
		body                       any
	}{
		{"plugin url", http.MethodPut, "/api/v1/settings/plugins/gatus", "plugins.gatus.url", map[string]any{"url": "https://u:" + canary + "@g.example.com"}},
		{"plugin url, user only", http.MethodPut, "/api/v1/settings/plugins/gatus", "plugins.gatus.url", map[string]any{"url": "https://" + canary + "@g.example.com"}},
		{"launcher href", http.MethodPut, "/api/v1/settings/plugins/launcher", "plugins.launcher.links",
			map[string]any{"links": []any{map[string]any{"name": "a", "href": "https://u:" + canary + "@x.example.com"}}}},
		{"icon base", http.MethodPut, "/api/v1/settings/general", "ui.iconBaseURL", map[string]any{"ui": map[string]any{"iconBaseURL": "https://u:" + canary + "@cdn.example.com/i"}}},
		{"test url", http.MethodPost, "/api/v1/settings/plugins/gatus/test", "plugins.gatus.url", map[string]any{"patch": map[string]any{"url": "https://u:" + canary + "@g.example.com"}}},
	} {
		h := e.ifMatch()
		if c.method == http.MethodPost {
			h = nil
		}
		w := e.do(c.method, c.path, c.body, h)
		got := wantErr(t, w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if len(got.Details.Fields) != 1 || got.Details.Fields[0] != (model.SettingsFieldError{Path: c.field, Message: msgURLCredentials}) {
			t.Errorf("%s: %+v", c.label, got.Details)
		}
		if strings.Contains(w.Body.String(), canary) {
			t.Errorf("%s: the answer repeats the URL: %s", c.label, w.Body)
		}
	}
	if e.f.St.Revision() != 0 || len(seen()) != 0 {
		t.Errorf("a refused URL was stored or tested: revision %d, checks %q", e.f.St.Revision(), seen())
	}
}

// A URL's userinfo already in the store (saved before the UI refused it, or by hand) is not in
// the export, and ui.iconBaseURL's is not in /api/v1/me, which every principal reads.
func TestURLCredentialsAreNotExportedOrServedByMe(t *testing.T) {
	const canary = "URLPW-canary-77d2"
	e := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{
		"plugins.grafana.url":    "https://u:" + canary + "@grafana.example.com",
		"plugins.launcher.links": []any{map[string]any{"name": "a", "href": "https://u:" + canary + "@x.example.com"}},
	}}, func(d *Deps) { d.Cfg.IconBaseURL = "https://u:" + canary + "@cdn.example.com/icons" })
	w := e.do(http.MethodGet, "/api/v1/settings/export", nil, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), canary) || !strings.Contains(w.Body.String(), "https://grafana.example.com") {
		t.Errorf("export %d:\n%s", w.Code, w.Body)
	}
	var me model.Me
	if err := json.Unmarshal(e.do(http.MethodGet, "/api/v1/me", nil, nil).Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.UI.IconBaseURL != "https://cdn.example.com/icons" {
		t.Errorf("/me iconBaseUrl = %q", me.UI.IconBaseURL)
	}
}

// A problem of the file or the environment that blocks a UI save is no field error at a path the
// form did not send: it is the form-level reason invalid_configuration, with a text that says to
// fix the file or the environment first, and never the file's value.
func TestFileProblemBlockingAUISaveIsFormLevel(t *testing.T) {
	const canary = "FILEVAL-canary-3e9a"
	e := newSettingsEnv(t, settingstest.Options{})
	e.f.WriteConfig(t, "plugins:\n  grafana:\n    url: \"ftp://"+canary+"@x\"\n")
	w := e.put("/api/v1/settings/plugins/headlamp", map[string]any{"url": "https://headlamp.example.com"})
	got := wantErr(t, w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if got.Error != msgOperatorConfig || got.Details.Reason != model.SettingsReasonInvalidConfiguration || len(got.Details.Fields) != 0 {
		t.Errorf("answer = %+v", got)
	}
	if strings.Contains(w.Body.String(), canary) {
		t.Errorf("the file's value is in the answer: %s", w.Body)
	}
	// A problem with the value the request sent is still a field error there.
	got = wantErr(t, e.put("/api/v1/settings/plugins/headlamp", map[string]any{"url": "ftp://headlamp.example.com"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	paths := []string{}
	for _, f := range got.Details.Fields {
		paths = append(paths, f.Path)
	}
	if !strings.Contains(strings.Join(paths, ","), "plugins.headlamp.url") || strings.Contains(strings.Join(paths, ","), "plugins.grafana.url") {
		t.Errorf("fields = %v, want the supplied path only", paths)
	}
}
