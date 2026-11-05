package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

// The canaries are secrets from each layer: the environment, the config file, the store and a
// write through the API.
const (
	canaryEnv  = "canary-env-6b1d0c"
	canaryFile = "canary-file-93ae27"
	canaryUI   = "canary-ui-4f08e1"
	canaryPut  = "canary-put-c27d55"
)

func canaryEnvOf(t *testing.T, logger *slog.Logger, a *audit.Log, reg *secret.Registry) *settingsEnv {
	return newSettingsEnv(t, settingstest.Options{
		SecretKey: true, Namespace: "dash", Registry: reg, Log: logger,
		Checker: settingstest.Checks{"prometheus": func(context.Context, any, bool) (plugin.CheckReport, error) {
			return plugin.CheckReport{Checks: []plugin.CheckResult{{Name: "query", OK: false, Category: "auth", Message: "authentication failed"}}}, nil
		}},
		Env:  []string{"DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + canaryEnv},
		File: "plugins:\n  alertmanager:\n    url: https://am.example.com\n    auth:\n      bearerToken: " + canaryFile + "\n",
		Stored: map[string]any{
			"plugins.prometheus.url":              "https://prom.example.com",
			"plugins.prometheus.auth.bearerToken": settingstest.Encrypt(t, "plugins.prometheus.auth.bearerToken", canaryUI),
		},
	}, withAudit(a))
}

// A secret field carries metadata only: set, its layer and its reference, never a value.
func TestSecretsAreMetadataOnly(t *testing.T) {
	e := canaryEnvOf(t, nil, nil, nil)
	for id, want := range map[string]model.SettingField{
		"proxmox":      {Path: "plugins.proxmox.tokenSecret", Source: "env", Ref: "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", Locked: true, LockedBy: "env"},
		"alertmanager": {Path: "plugins.alertmanager.auth.bearerToken", Source: "file", Ref: e.f.ConfigPath, Locked: true, LockedBy: "file"},
		"prometheus":   {Path: "plugins.prometheus.auth.bearerToken", Source: "ui", Stored: true},
	} {
		got := field(t, e.pluginView(t, id).Fields, want.Path)
		want.Type, want.Secret, want.Set, want.Doc = "string", true, true, got.Doc
		if !reflectEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", id, got, want)
		}
	}
}

// TestNoEndpointEverReturnsASecret sends a request to every settings route, writes included, and
// looks for each canary in every answer's body and headers, in the audit records (the rings and
// the file) and in every log line.
func TestNoEndpointEverReturnsASecret(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := secret.NewRegistry()
	dir := t.TempDir()
	a, err := audit.Open(dir, audit.Options{Logger: logger, Scrub: reg.Scrub})
	if err != nil {
		t.Fatal(err)
	}
	e := canaryEnvOf(t, logger, a, reg)
	var seen strings.Builder
	record := func(rq req) {
		rq.header = e.ifMatch() // every write names the revision it read, as the UI does
		w := rq.do(e.h)
		b, _ := json.Marshal(w.Header())
		seen.Write(b)
		seen.Write(w.Body.Bytes())
	}
	ifm := map[string]string{}
	for _, rq := range []req{
		{method: "GET", path: "/api/v1/settings"},
		{method: "GET", path: "/api/v1/settings/schema"},
		{method: "GET", path: "/api/v1/settings/export"},
		{method: "GET", path: "/api/v1/settings/plugins/proxmox"},
		{method: "GET", path: "/api/v1/settings/plugins/alertmanager"},
		{method: "GET", path: "/api/v1/settings/plugins/prometheus"},
		{method: "PUT", path: "/api/v1/settings/plugins/victorialogs/secrets/auth.bearerToken", body: map[string]any{"value": canaryPut}},
		{method: "PUT", path: "/api/v1/settings/plugins/prometheus", body: map[string]any{"timeout": "45s"}, header: ifm},
		{method: "PUT", path: "/api/v1/settings/plugins/prometheus", body: map[string]any{"url": "https://prom2.example.com"}, header: ifm},
		{method: "PUT", path: "/api/v1/settings/plugins/proxmox", body: map[string]any{"url": "https://pve2.example.com"}, header: ifm},
		{method: "PUT", path: "/api/v1/settings/general", body: map[string]any{"server": map[string]any{"listen": "x"}}, header: ifm},
		{method: "POST", path: "/api/v1/settings/plugins/prometheus/test", body: map[string]any{}},
		{method: "POST", path: "/api/v1/settings/plugins/prometheus/test", body: map[string]any{"patch": map[string]any{"url": "https://prom3.example.com"}}},
		{method: "DELETE", path: "/api/v1/settings/plugins/prometheus/stored/auth.bearerToken", header: ifm},
		{method: "DELETE", path: "/api/v1/settings/plugins/victorialogs/secrets/auth.bearerToken"},
		{method: "GET", path: "/api/v1/settings/audit?limit=500"},
		{method: "GET", path: "/api/v1/settings/audit?ring=unauth"},
	} {
		if rq.header == nil {
			rq.header = ifm
		}
		record(rq)
	}
	recs, _, _ := a.Recent(500, "", audit.RingMain)
	b, _ := json.Marshal(recs)
	file, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) < 8 {
		t.Fatalf("only %d audit records: the writes did not run", len(recs))
	}
	for _, c := range []string{canaryEnv, canaryFile, canaryUI, canaryPut} {
		for name, s := range map[string]string{"answers": seen.String(), "audit": string(b), "audit file": string(file), "logs": logs.String()} {
			if strings.Contains(s, c) {
				t.Errorf("%s is in the %s", c, name)
			}
		}
	}
	// The registry knows every one, so the log handler and the API scrub them.
	for _, c := range []string{canaryEnv, canaryFile, canaryUI, canaryPut} {
		if reg.Scrub("x "+c) == "x "+c {
			t.Errorf("%s is not registered", c)
		}
	}
	if !strings.Contains(seen.String(), `"set":true`) {
		t.Error("the answers carry no secret metadata: the test saw nothing")
	}
}
