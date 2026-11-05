package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
)

func exportedStore(t *testing.T, canary string) map[string]any {
	return map[string]any{
		"plugins.gatus.url":               "https://gatus.example.com",
		"plugins.gatus.criticalGroups":    []any{"core", "edge"},
		gatusBearer:                       settingstest.Encrypt(t, gatusBearer, canary),
		"plugins.proxmox.url":             "https://pve.example.com:8006",
		"plugins.proxmox.tokenID":         "u@pve!t",
		"plugins.proxmox.tokenSecret":     "k8s:dash/pve#token",
		"plugins.proxmox.criticalVMs.ids": []any{101, 102},
		"ui.iconBaseURL":                  "https://icons.example.com/svg",
	}
}

// The export is what the store holds: a stored secret is a comment naming its path, never its
// value or ciphertext; a k8s: reference stays.
func TestExportHoldsOnlyStoredKeysAndNoCiphertext(t *testing.T) {
	const canary = "export-canary-0d41"
	e := newSettingsEnv(t, settingstest.Options{
		SecretKey: true, Namespace: "dash", Stored: exportedStore(t, canary),
		File: "plugins:\n  grafana:\n    url: https://grafana.example.com\n",
		Env:  []string{"DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com"},
	})
	w := e.do(http.MethodGet, "/api/v1/settings/export", nil, nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") {
		t.Fatalf("%d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"# plugins.gatus.auth.bearerToken: secret stored in dash; supply env:NAME or file:/path here",
		`"tokenSecret": "k8s:dash/pve#token"`, `"url": "https://gatus.example.com"`, `"iconBaseURL": "https://icons.example.com/svg"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{canary, "enc:v1:", "grafana", "headlamp", "dataDir", "auth.mode"} {
		if strings.Contains(body, bad) {
			t.Errorf("export holds %q:\n%s", bad, body)
		}
	}
}

// The export is a config file dash loads, with the values the store had.
func TestExportIsValidYAMLThatLoads(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Namespace: "dash", Stored: exportedStore(t, "export-canary-0d41")})
	body := e.do(http.MethodGet, "/api/v1/settings/export", nil, nil).Body.String()
	loaded := settingstest.New(t, settingstest.Options{File: body})
	res, want := loaded.Resolved(), e.f.Resolved()
	if res.Provenance["plugins.gatus.url"].Source != "file" || res.Tree.Plugins.Gatus.URL != want.Tree.Plugins.Gatus.URL ||
		!slices.Equal(res.Tree.Plugins.Gatus.CriticalGroups, want.Tree.Plugins.Gatus.CriticalGroups) ||
		!slices.Equal(res.Tree.Plugins.Proxmox.CriticalVMs.IDs, want.Tree.Plugins.Proxmox.CriticalVMs.IDs) ||
		res.Tree.Plugins.Proxmox.TokenSecret.Ref() != "k8s:dash/pve#token" || res.Tree.UI.IconBaseURL != want.Tree.UI.IconBaseURL {
		t.Errorf("the export loaded differently:\n%s", body)
	}
	if res.Tree.Plugins.Gatus.Auth.BearerToken.IsSet() {
		t.Error("a stored secret came back through the export")
	}
}
