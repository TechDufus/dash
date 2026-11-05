package api

import (
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

const proxmoxPath = "/api/v1/settings/plugins/proxmox"

// proxmoxStored is a proxmox section saved from the UI, its token secret encrypted.
func proxmoxStored(t *testing.T, secretValue string) map[string]any {
	return map[string]any{
		"plugins.proxmox.url": "https://pve.example.com:8006", "plugins.proxmox.tokenID": "u@pve!t",
		"plugins.proxmox.tokenSecret": settingstest.Encrypt(t, "plugins.proxmox.tokenSecret", secretValue),
	}
}

func TestPutLockedByFileIs409(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{File: "plugins:\n  grafana:\n    url: https://grafana.example.com\n"})
	for _, patch := range []map[string]any{{"url": "https://other.example.com"}, {"url": nil}} {
		got := wantErr(t, e.put("/api/v1/settings/plugins/grafana", patch), http.StatusConflict, model.ErrCodeLockedField)
		want := model.SettingsErrorDetails{Path: "plugins.grafana.url", Source: "file", Ref: e.f.ConfigPath}
		if !reflectEqual(got.Details, want) {
			t.Errorf("details = %+v, want %+v", got.Details, want)
		}
	}
	if e.f.St.Revision() != 0 {
		t.Error("a locked write changed the store")
	}
}

func TestPutLockedByEnvIs409(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Env: []string{"DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com"}})
	got := wantErr(t, e.put("/api/v1/settings/plugins/headlamp", map[string]any{"url": "https://x.example.com"}), http.StatusConflict, model.ErrCodeLockedField)
	want := model.SettingsErrorDetails{Path: "plugins.headlamp.url", Source: "env", Ref: "DASH_PLUGINS_HEADLAMP_URL"}
	if !reflectEqual(got.Details, want) {
		t.Errorf("details = %+v, want %+v", got.Details, want)
	}
}

// Removing the stored value a file value shadows is the one write a locked field allows.
func TestDeleteStoredOnLockedPathIsTheOnlyAllowedWrite(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{
		File:   "plugins:\n  grafana:\n    url: https://grafana.example.com\n",
		Stored: map[string]any{"plugins.grafana.url": "https://stored.example.com"},
	})
	wantErr(t, e.put("/api/v1/settings/plugins/grafana", map[string]any{"url": nil}), http.StatusConflict, model.ErrCodeLockedField)
	wantOK(t, e.do(http.MethodDelete, "/api/v1/settings/plugins/grafana/stored/url", nil, e.ifMatch()))
	if _, ok := config.StoredAt(e.f.Stored(), "plugins.grafana.url"); ok {
		t.Error("the stored value is still there")
	}
	if f := field(t, e.pluginView(t, "grafana").Fields, "plugins.grafana.url"); f.Shadowed || f.Stored || f.Source != "file" {
		t.Errorf("after the delete: %+v", f)
	}
	recs := e.records()
	if len(recs) != 2 || recs[0].Action != actionSettingsRemoveStored || !slices.Equal(recs[0].Fields, []string{"plugins.grafana.url"}) {
		t.Errorf("audit = %+v", recs)
	}
}

func TestEveryWriteVerbIs409WhenSettingsAreReadonly(t *testing.T) {
	for name, o := range map[string]settingstest.Options{
		"settings.ui readonly": {ReadOnly: true, SecretKey: true},
		"no data dir":          {NoDataDir: true, SecretKey: true},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSettingsEnv(t, o)
			for _, rq := range []struct {
				method, path string
				body         any
			}{
				{"PUT", "/api/v1/settings/general", map[string]any{"ui": map[string]any{"iconBaseURL": "https://icons.example.com/"}}},
				{"PUT", proxmoxPath, map[string]any{"node": "pve"}},
				{"PUT", proxmoxPath + "/secrets/tokenSecret", map[string]any{"value": "s"}},
				{"DELETE", proxmoxPath + "/secrets/tokenSecret", nil},
				{"DELETE", proxmoxPath + "/stored/node", nil},
			} {
				w := e.do(rq.method, rq.path, rq.body, e.ifMatch())
				if w.Code != http.StatusConflict || decodeBody[model.APIError](t, w).Code != model.ErrCodeReadonlySettings {
					t.Errorf("%s %s = %d %s", rq.method, rq.path, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestStaleIfMatchIs412(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: map[string]any{"plugins.proxmox.node": "pve"}})
	stale := map[string]string{"If-Match": `"7"`}
	for _, rq := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/v1/settings/general", map[string]any{"ui": map[string]any{"iconBaseURL": "https://icons.example.com/"}}},
		{"PUT", proxmoxPath, map[string]any{"node": "pve2"}},
		{"PUT", "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken", map[string]any{"value": "s"}},
		{"DELETE", proxmoxPath + "/stored/node", nil},
	} {
		for _, h := range []map[string]string{stale, {"If-Match": "nonsense"}} {
			wantErr(t, e.do(rq.method, rq.path, rq.body, h), http.StatusPreconditionFailed, model.ErrCodePreconditionFailed)
		}
	}
	if e.f.St.Revision() != 1 {
		t.Errorf("revision %d after refused writes", e.f.St.Revision())
	}
	// A bare number is the revision too.
	wantOK(t, e.do(http.MethodPut, proxmoxPath, map[string]any{"node": "pve2"}, map[string]string{"If-Match": "1"}))
}

func TestMissingIfMatchIs428(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	for _, path := range []string{"/api/v1/settings/general", proxmoxPath} {
		wantErr(t, e.do(http.MethodPut, path, map[string]any{}, nil), http.StatusPreconditionRequired, model.ErrCodePreconditionRequired)
	}
	// It is optional on the secret routes.
	wantOK(t, e.do(http.MethodPut, "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken", map[string]any{"value": "tok-0123456789"}, nil))
}

func TestMergePatchNullResetsToDefault(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{
		"plugins.proxmox.node": "pve", "plugins.proxmox.storage.source": "prometheus",
	}})
	wantOK(t, e.put(proxmoxPath, map[string]any{"node": nil, "storage": nil}))
	if len(e.f.Stored()) != 0 {
		t.Errorf("stored = %v", e.f.Stored())
	}
	res := e.f.Resolved()
	if res.Tree.Plugins.Proxmox.Node != "" || res.Provenance["plugins.proxmox.storage.source"].Source != config.SourceDefault {
		t.Errorf("after reset: node %q, storage %+v", res.Tree.Plugins.Proxmox.Node, res.Provenance["plugins.proxmox.storage.source"])
	}
}

// server.*, auth.*, settings.ui, settings.secretNamespaces and dataDir are refused by the server's
// rule through PUT general; ui.* goes through and is restart-marked.
func TestPatchOfIneligiblePathIs409WithTheEnvelope(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	for path, patch := range map[string]map[string]any{
		"server.listen":             {"server": map[string]any{"listen": "0.0.0.0:8080"}},
		"auth.mode":                 {"auth": map[string]any{"mode": "none"}},
		"settings.ui":               {"settings": map[string]any{"ui": "readonly"}},
		"settings.secretNamespaces": {"settings": map[string]any{"secretNamespaces": []any{"kube-system"}}},
		"dataDir":                   {"dataDir": "/tmp"},
	} {
		got := wantErr(t, e.put("/api/v1/settings/general", patch), http.StatusConflict, model.ErrCodeLockedField)
		if got.Details.Path != path || got.Details.Source != model.SettingLockIneligible || got.Details.Ref != "" {
			t.Errorf("%s: details %+v", path, got.Details)
		}
	}
	res := wantOK(t, e.put("/api/v1/settings/general", map[string]any{"ui": map[string]any{"iconBaseURL": "https://icons.example.com/svg"}}))
	if !slices.Contains(res.RestartRequired, "ui.iconBaseURL") || res.Revision != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestPatchWithFileOrEnvRefIsRejected(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Namespace: "dash"})
	for _, patch := range []map[string]any{
		{"node": "env:HOME"},
		{"node": " file:/etc/passwd"},
		{"criticalVMs": map[string]any{"tags": []any{"ok", "file:/etc/shadow"}}},
	} {
		got := wantErr(t, e.put(proxmoxPath, patch), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if len(got.Details.Fields) != 1 || !strings.HasPrefix(got.Details.Fields[0].Path, "plugins.proxmox.") || got.Details.Fields[0].Message != msgReference {
			t.Errorf("%v: details %+v", patch, got.Details)
		}
	}
	const sp = "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken"
	for _, ref := range []string{"env:TOKEN", "file:/run/secret"} {
		wantErr(t, e.put(sp, map[string]any{"ref": ref}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	}
	if e.f.St.Revision() != 0 {
		t.Fatal("a refused write changed the store")
	}
	// A k8s: reference is what the UI layer may hold for a secret.
	wantOK(t, e.put(sp, map[string]any{"ref": "k8s:dash/gatus#token"}))
	if v, _ := config.StoredAt(e.f.Stored(), "plugins.gatus.auth.bearerToken"); v != "k8s:dash/gatus#token" {
		t.Errorf("stored = %v", v)
	}
}

func TestPatchValidationErrorsCarryPaths(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	for _, tc := range []struct {
		patch map[string]any
		want  []model.SettingsFieldError
	}{
		{map[string]any{"url": 5}, []model.SettingsFieldError{{Path: "plugins.gatus.url", Message: "expected a string, got number"}}},
		{map[string]any{"urlx": "a"}, []model.SettingsFieldError{{Path: "plugins.gatus.urlx", Message: msgUnknownField}}},
		{map[string]any{"url": "ftp://gatus.example.com"}, []model.SettingsFieldError{{Path: "plugins.gatus.url", Message: "must be an http(s) URL with a host, e.g. https://example.com"}}},
	} {
		got := wantErr(t, e.put("/api/v1/settings/plugins/gatus", tc.patch), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if !reflectEqual(got.Details.Fields, tc.want) || got.Details.Reason != "" {
			t.Errorf("%v: details %+v, want fields %+v", tc.patch, got.Details, tc.want)
		}
	}
}

// A key of another section is no key of the plugin's: 422, and plugins.* is not written through
// PUT general.
func TestKeyOutsideThePluginSectionIs422(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	got := wantErr(t, e.put(proxmoxPath, map[string]any{"server": map[string]any{"listen": "0.0.0.0:1"}}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if len(got.Details.Fields) != 1 || got.Details.Fields[0].Path != "plugins.proxmox.server" {
		t.Errorf("details = %+v", got.Details)
	}
	got = wantErr(t, e.put("/api/v1/settings/general", map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"node": "pve"}}}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if len(got.Details.Fields) != 1 || got.Details.Fields[0].Path != "plugins.proxmox.node" {
		t.Errorf("details = %+v", got.Details)
	}
}

// Every leaf of a patch lands in one revision, or none does.
func TestPutIsAtomicAcrossLeaves(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	wantErr(t, e.put(proxmoxPath, map[string]any{"node": "pve", "storage": map[string]any{"source": "nope"}}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if e.f.St.Revision() != 0 || len(e.f.Stored()) != 0 {
		t.Fatalf("a refused patch wrote: revision %d, %v", e.f.St.Revision(), e.f.Stored())
	}
	res := wantOK(t, e.put(proxmoxPath, map[string]any{"node": "pve", "storage": map[string]any{"source": "prometheus"}, "criticalVMs": map[string]any{"ids": []any{101, 102}}}))
	if res.Revision != 1 || e.f.St.Revision() != 1 {
		t.Errorf("three leaves took revisions up to %d", e.f.St.Revision())
	}
}

// The patch is decoded without UseNumber (ruling 5): 1.0 is the number 1, so it saves into an
// integer setting and equals a stored 1. Decoded as a json.Number it would be the text "1.0",
// which an integer setting refuses.
func TestMergePatchNumbersAreNumbers(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{"plugins.victorialogs.limit": 1}})
	const path = "/api/v1/settings/plugins/victorialogs"
	wantOK(t, e.put(path, map[string]any{"limit": json.RawMessage("1.0")}))
	if got := e.f.Resolved().Tree.Plugins.VictoriaLogs.Limit; got != 1 {
		t.Errorf("running limit = %d, want 1", got)
	}
	wantOK(t, e.put(path, map[string]any{"limit": json.RawMessage("2.0")}))
	if got := e.f.Resolved().Tree.Plugins.VictoriaLogs.Limit; got != 2 {
		t.Errorf("running limit = %d, want 2", got)
	}
}

// A PUT is in effect when it answers: the reloader's tick never runs in this test.
func TestPutAppliesWithoutWaitingForTheTick(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	wantOK(t, e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://gatus.example.com/"}))
	res := e.f.Resolved()
	if res.Tree.Plugins.Gatus.URL != "https://gatus.example.com" || res.Provenance["plugins.gatus.url"].Source != config.SourceUI {
		t.Errorf("running gatus.url = %q (%+v)", res.Tree.Plugins.Gatus.URL, res.Provenance["plugins.gatus.url"])
	}
}

// A patch the candidate accepts but the reload refuses leaves the store with its previous values,
// answers the reload's problem by path and records a failed outcome with a fixed text.
func TestReloadFailureAfterApplyRollsBack(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{"plugins.gatus.url": "https://old.example.com"}})
	calls := 0
	e.f.BeforeReload = func() error {
		calls++
		if calls == 1 {
			return &config.FieldError{Path: "plugins.gatus.url", Message: "refused by the reload"}
		}
		return nil
	}
	got := wantErr(t, e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://new.example.com"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if len(got.Details.Fields) != 1 || got.Details.Fields[0].Path != "plugins.gatus.url" {
		t.Errorf("details = %+v", got.Details)
	}
	if v, _ := config.StoredAt(e.f.Stored(), "plugins.gatus.url"); v != "https://old.example.com" {
		t.Errorf("stored url = %v, want the previous one", v)
	}
	if u := e.f.Resolved().Tree.Plugins.Gatus.URL; u != "https://old.example.com" {
		t.Errorf("running url = %q", u)
	}
	recs := e.records()
	if len(recs) != 2 || recs[0].Kind != audit.KindOutcome || recs[0].Result != audit.ResultError || recs[0].Error != errSettingsReload.Error() {
		t.Errorf("audit = %+v", recs)
	}
}

// A failure no field carries is reason invalid_configuration with a fixed text, in the answer and
// in the audit outcome: never the error's own text, which names a variable or a file. A file
// that does not resolve without the request either says so: the file is the one to fix.
func TestNonFieldFailureIsInvalidConfigurationWithFixedText(t *testing.T) {
	const unset = "DASH_TEST_UNSET_CANARY_4F2A"
	bad := "plugins:\n  grafana:\n    url: https://grafana.example.com\n    auth:\n      bearerToken: env:" + unset + "\n"
	check := func(t *testing.T, e *settingsEnv, w string) {
		t.Helper()
		for _, rec := range e.records() {
			b, _ := json.Marshal(rec)
			w += string(b)
		}
		if strings.Contains(w, unset) || strings.Contains(w, "not set") {
			t.Errorf("the error's text leaked: %s", w)
		}
	}

	t.Run("candidate", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{})
		e.f.WriteConfig(t, bad)
		w := e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://gatus.example.com"})
		got := wantErr(t, w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if got.Details.Reason != model.SettingsReasonInvalidConfiguration || got.Error != msgOperatorConfig || len(got.Details.Fields) != 0 {
			t.Errorf("answer = %+v", got)
		}
		check(t, e, w.Body.String())
	})
	t.Run("reload", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{})
		e.f.BeforeReload = func() error { e.f.WriteConfig(t, bad); return nil }
		w := e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://gatus.example.com"})
		got := wantErr(t, w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if got.Details.Reason != model.SettingsReasonInvalidConfiguration || got.Error != msgOperatorConfig {
			t.Errorf("answer = %+v", got)
		}
		recs := e.records()
		if len(recs) != 2 || recs[0].Result != audit.ResultError || recs[0].Error != errSettingsReload.Error() {
			t.Errorf("audit = %+v", recs)
		}
		check(t, e, w.Body.String())
	})
}

// Two writes from the same revision: the lock serialises them, and the second finds the
// revision moved.
func TestConcurrentPutsSerialiseUnderTheReloaderLock(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	h := e.ifMatch()
	codes := make([]int, 2)
	var wg sync.WaitGroup
	for i, node := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.do(http.MethodPut, proxmoxPath, map[string]any{"node": node}, h).Code
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if !slices.Equal(codes, []int{http.StatusOK, http.StatusPreconditionFailed}) || e.f.St.Revision() != 1 {
		t.Errorf("codes %v, revision %d", codes, e.f.St.Revision())
	}
}

// A first setup moves the URL from nothing to a host: the typed secret rides in the same patch.
func TestFirstSetupSavesTheSecretWithTheURL(t *testing.T) {
	const canary = "first-secret-canary-91d0"
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	wantOK(t, e.put(proxmoxPath, map[string]any{"url": "https://pve.example.com:8006", "tokenID": "u@pve!t", "tokenSecret": canary}))
	if got := e.f.Resolved().Tree.Plugins.Proxmox.TokenSecret.Reveal(); got != canary {
		t.Errorf("running secret = %q", got)
	}
	if v, _ := config.StoredAt(e.f.Stored(), "plugins.proxmox.tokenSecret"); !secret.IsEncrypted(v.(string)) {
		t.Errorf("stored secret is not encrypted")
	}
	// Without a destination change a secret goes through the secrets endpoint.
	got := wantErr(t, e.put(proxmoxPath, map[string]any{"tokenSecret": "other-secret"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if len(got.Details.Fields) != 1 || got.Details.Fields[0] != (model.SettingsFieldError{Path: "plugins.proxmox.tokenSecret", Message: msgUseSecrets}) {
		t.Errorf("details = %+v", got.Details)
	}
	// Without a key there is nowhere to keep it.
	nk := newSettingsEnv(t, settingstest.Options{})
	wantErr(t, nk.put(proxmoxPath, map[string]any{"url": "https://pve.example.com:8006", "tokenID": "u@pve!t", "tokenSecret": canary}), http.StatusConflict, model.ErrCodeConflict)
}

// Changing tls.caFile, tls.insecureSkipVerify or the URL's host or port needs every set secret in
// the same patch; one from a locked layer refuses the change.
func TestHostBindingCoversTLSLeaves(t *testing.T) {
	changes := []map[string]any{
		{"tls": map[string]any{"caFile": "/etc/ssl/pve.pem"}},
		{"tls": map[string]any{"insecureSkipVerify": true}},
		{"url": "https://other.example.com:8006"},
		{"url": "https://pve.example.com:8007"},
	}
	t.Run("stored secret", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: proxmoxStored(t, "old-secret-0123456789")})
		for _, patch := range changes {
			got := wantErr(t, e.put(proxmoxPath, patch), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
			if len(got.Details.Fields) != 1 || got.Details.Fields[0] != (model.SettingsFieldError{Path: "plugins.proxmox.tokenSecret", Message: msgReenterSecret}) {
				t.Errorf("%v: details %+v", patch, got.Details)
			}
		}
		// The path or scheme alone is no new destination.
		wantOK(t, e.put(proxmoxPath, map[string]any{"url": "https://pve.example.com:8006/"}))
		patch := map[string]any{"tls": map[string]any{"caFile": "/etc/ssl/pve.pem"}, "tokenSecret": "new-secret-0123456789"}
		wantOK(t, e.put(proxmoxPath, patch))
		if got := e.f.Resolved().Tree.Plugins.Proxmox.TokenSecret.Reveal(); got != "new-secret-0123456789" {
			t.Errorf("secret after the change = %q", got)
		}
	})
	t.Run("locked secret", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{
			Stored: map[string]any{"plugins.proxmox.url": "https://pve.example.com:8006", "plugins.proxmox.tokenID": "u@pve!t"},
			Env:    []string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=env-secret-0123456789"},
		})
		for _, patch := range changes {
			got := wantErr(t, e.put(proxmoxPath, patch), http.StatusConflict, model.ErrCodeLockedField)
			if got.Details.Path != "plugins.proxmox.tokenSecret" || got.Details.Reason != model.SettingsReasonSecretLocked {
				t.Errorf("%v: details %+v", patch, got.Details)
			}
		}
	})
}

// auth.mode=none on a listener that is not loopback: an unauthenticated administrator, so every
// settings write is refused; reads still answer.
func TestSettingsWritesRefusedInNoneModeOffLoopback(t *testing.T) {
	none := func(listen string) func(*Deps) {
		return func(d *Deps) {
			d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
			d.Cfg.Listen = listen
		}
	}
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true}, none("0.0.0.0:8080"))
	for _, rq := range []struct {
		method, path string
		body         any
	}{
		{"PUT", "/api/v1/settings/general", map[string]any{}},
		{"PUT", proxmoxPath, map[string]any{"node": "pve"}},
		{"PUT", proxmoxPath + "/secrets/tokenSecret", map[string]any{"value": "s"}},
		{"DELETE", proxmoxPath + "/secrets/tokenSecret", nil},
		{"DELETE", proxmoxPath + "/stored/node", nil},
	} {
		wantErr(t, e.do(rq.method, rq.path, rq.body, e.ifMatch()), http.StatusForbidden, model.ErrCodeForbidden)
	}
	if w := e.do(http.MethodGet, "/api/v1/settings", nil, nil); w.Code != http.StatusOK {
		t.Errorf("read = %d", w.Code)
	}
	lo := newSettingsEnv(t, settingstest.Options{}, none("127.0.0.1:8080"))
	wantOK(t, lo.put(proxmoxPath, map[string]any{"node": "pve"}))
}

// The connection test makes dash call an address the caller chooses, so auth.mode=none off
// loopback (auth.none.allowRemote) refuses it as it refuses the writes: 403 with the same message,
// before the rate limit or the body is read. On loopback it runs.
func TestSettingsConnectionTestRefusedInNoneModeOffLoopback(t *testing.T) {
	none := func(listen string) func(*Deps) {
		return func(d *Deps) {
			d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
			d.Cfg.Listen = listen
		}
	}
	errorText := func(w *httptest.ResponseRecorder) string {
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return body.Error
	}
	e := newSettingsEnv(t, settingstest.Options{Checker: checkSupervisor(t, nil)}, none("0.0.0.0:8080"))
	write := e.do("PUT", proxmoxPath, map[string]any{"node": "pve"}, e.ifMatch())
	wantErr(t, write, http.StatusForbidden, model.ErrCodeForbidden)
	// A loopback URL from the form is blocked without a dial, so the check is quick either way.
	test := e.test("gatus", map[string]any{"patch": map[string]any{"url": "http://127.0.0.1:1"}})
	wantErr(t, test, http.StatusForbidden, model.ErrCodeForbidden)
	if got, want := errorText(test), errorText(write); got != want {
		t.Errorf("connection test refusal %q, want the settings write's %q", got, want)
	}
	lo := newSettingsEnv(t, settingstest.Options{Checker: checkSupervisor(t, nil)}, none("127.0.0.1:8080"))
	checkReport(t, lo.test("gatus", map[string]any{"patch": map[string]any{"url": "http://127.0.0.1:1"}}))
}

// A k8s: reference from the UI may name dash's own namespace or one settings.secretNamespaces
// lists, through the secrets endpoint and in a patch alike.
func TestUIK8sRefLimitedToOwnNamespaceAndAllowList(t *testing.T) {
	const sp = "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken"
	e := newSettingsEnv(t, settingstest.Options{Namespace: "dash"})
	got := wantErr(t, e.put(sp, map[string]any{"ref": "k8s:kube-system/admin#token"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if got.Details.Fields[0] != (model.SettingsFieldError{Path: "plugins.gatus.auth.bearerToken", Message: msgK8sNamespace}) {
		t.Errorf("details = %+v", got.Details)
	}
	wantErr(t, e.put(proxmoxPath, map[string]any{"url": "https://pve.example.com", "tokenID": "u@pve!t", "tokenSecret": "k8s:kube-system/pve#t"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	wantOK(t, e.put(sp, map[string]any{"ref": "k8s:dash/gatus#token"}))

	allow := newSettingsEnv(t, settingstest.Options{Namespace: "dash", Env: []string{`DASH_SETTINGS_SECRET_NAMESPACES=["monitoring"]`}})
	wantOK(t, allow.put(sp, map[string]any{"ref": "k8s:monitoring/gatus#token"}))
	wantErr(t, allow.put(sp, map[string]any{"ref": "k8s:kube-system/gatus#token"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)

	// The operator's references name any namespace.
	op := newSettingsEnv(t, settingstest.Options{Namespace: "dash", File: "plugins:\n  gatus:\n    auth:\n      bearerToken: k8s:kube-system/gatus#token\n"})
	if f := field(t, op.pluginView(t, "gatus").Fields, "plugins.gatus.auth.bearerToken"); !f.Set || f.Source != "file" {
		t.Errorf("file reference = %+v", f)
	}
}

// With an audit file that cannot be written, every settings write is 503 and changes nothing.
func TestAuditWriteFailureBlocksEverySettingsWrite(t *testing.T) {
	a, err := audit.Open(t.TempDir(), audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	stored := proxmoxStored(t, "old-secret-0123456789")
	stored["plugins.proxmox.node"] = "pve"
	stored["plugins.gatus.auth.bearerToken"] = settingstest.Encrypt(t, "plugins.gatus.auth.bearerToken", "bearer-0123456789")
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: stored, Namespace: "dash"}, withAudit(a))
	if err := a.Close(); err != nil { // every later write fails
		t.Fatal(err)
	}
	before := e.f.Stored()
	for _, rq := range []struct {
		method, path string
		body         any
	}{
		{"PUT", proxmoxPath, map[string]any{"node": "pve2"}},
		{"PUT", proxmoxPath + "/secrets/tokenSecret", map[string]any{"value": "new-secret-0123456789"}},
		{"PUT", "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken", map[string]any{"ref": "k8s:dash/g#t"}},
		{"DELETE", "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken", nil},
		{"DELETE", proxmoxPath + "/stored/node", nil},
	} {
		wantErr(t, e.do(rq.method, rq.path, rq.body, e.ifMatch()), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	}
	if !reflectEqual(e.f.Stored(), before) || e.f.St.Revision() != 1 {
		t.Errorf("the store changed: revision %d", e.f.St.Revision())
	}
}

// The audit of a write names the paths it touched, never the values.
func TestSettingsAuditCarriesPathsNeverValues(t *testing.T) {
	const canary = "value-canary-5be2"
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	wantOK(t, e.put(proxmoxPath, map[string]any{"node": canary, "url": "https://pve.example.com", "tokenID": "u@pve!" + canary, "tokenSecret": "s-" + canary}))
	recs := e.records()
	if len(recs) != 2 || recs[1].Action != actionSettingsSet || recs[1].Source != "ui" ||
		!slices.Equal(recs[1].Fields, []string{"plugins.proxmox.node", "plugins.proxmox.tokenID", "plugins.proxmox.tokenSecret", "plugins.proxmox.url"}) {
		t.Errorf("audit = %+v", recs)
	}
	b, _ := json.Marshal(recs)
	if strings.Contains(string(b), canary) {
		t.Errorf("a value reached the audit: %s", b)
	}
}

func reflectEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// D16, the server's half of the form protocol: a secret re-entered with any edit of the URL saves
// in one PUT, however the two spell the same host (the form asks for the secret on any edit, and
// its idea of "the same host" is not the server's). Without the secret, a URL edit that keeps
// scheme, host and port saves; a new scheme (https to http would carry the stored secret in
// clear text) needs it; a secret in a patch that edits no url or dest leaf stays 422.
func TestURLEditWithReenteredSecretSavesInOnePut(t *testing.T) {
	const tok = "plugins.proxmox.tokenSecret"
	for name, tc := range map[string]struct{ from, to string }{
		"scheme only":        {"https://pve.example.com:8006", "http://pve.example.com:8006"},
		"ipv6 spelled out":   {"http://[0:0:0:0:0:0:0:1]:9", "http://[::1]:9"},
		"short ipv4":         {"http://127.1:9", "http://127.0.0.1:9"},
		"idn and punycode":   {"https://bücher.example:8006", "https://xn--bcher-kva.example:8006"},
		"punycode and idn":   {"https://xn--bcher-kva.example:8006", "https://bücher.example:8006"},
		"upper-case host":    {"https://pve.example.com:8006", "https://PVE.example.com:8006"},
		"default port named": {"https://pve.example.com", "https://pve.example.com:443"},
	} {
		t.Run(name, func(t *testing.T) {
			stored := proxmoxStored(t, "old-secret-0123456789")
			stored["plugins.proxmox.url"] = tc.from
			e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: stored})
			wantOK(t, e.put(proxmoxPath, map[string]any{"url": tc.to, "tokenSecret": "new-secret-0123456789"}))
			res := e.f.Resolved()
			if res.Tree.Plugins.Proxmox.URL != tc.to || res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != "new-secret-0123456789" {
				t.Errorf("running url %q, secret replaced %v", res.Tree.Plugins.Proxmox.URL, res.Tree.Plugins.Proxmox.TokenSecret.Reveal() == "new-secret-0123456789")
			}
		})
	}
	t.Run("same destination spelled differently, without the secret", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: proxmoxStored(t, "old-secret-0123456789")})
		wantOK(t, e.put(proxmoxPath, map[string]any{"url": "https://PVE.example.com:8006/api"}))
		if got := e.f.Resolved().Tree.Plugins.Proxmox.TokenSecret.Reveal(); got != "old-secret-0123456789" {
			t.Errorf("the stored secret changed: %q", got)
		}
	})
	t.Run("scheme downgrade, without the secret", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: proxmoxStored(t, "old-secret-0123456789")})
		got := wantErr(t, e.put(proxmoxPath, map[string]any{"url": "http://pve.example.com:8006"}), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if len(got.Details.Fields) != 1 || got.Details.Fields[0] != (model.SettingsFieldError{Path: tok, Message: msgReenterSecret}) {
			t.Errorf("details %+v", got.Details)
		}
		if e.f.St.Revision() != 1 || e.f.Resolved().Tree.Plugins.Proxmox.URL != "https://pve.example.com:8006" {
			t.Errorf("a refused downgrade wrote: revision %d", e.f.St.Revision())
		}
	})
	t.Run("secret without a url edit", func(t *testing.T) {
		e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: proxmoxStored(t, "old-secret-0123456789")})
		for _, patch := range []map[string]any{
			{"tokenSecret": "new-secret-0123456789"},
			{"url": "https://pve.example.com:8006", "tokenSecret": "new-secret-0123456789"}, // the running value again
			{"node": "pve2", "tokenSecret": "new-secret-0123456789"},
		} {
			got := wantErr(t, e.put(proxmoxPath, patch), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
			if len(got.Details.Fields) != 1 || got.Details.Fields[0] != (model.SettingsFieldError{Path: tok, Message: msgUseSecrets}) {
				t.Errorf("%v: details %+v", slices.Sorted(maps.Keys(patch)), got.Details)
			}
		}
		if e.f.St.Revision() != 1 {
			t.Errorf("a refused patch wrote: revision %d", e.f.St.Revision())
		}
	})
}

// interleaving is the fake with another admin's write landing right after the next WithLock
// returns, as a request queued on the lock would.
type interleaving struct {
	*settingstest.Fake
	after func()
}

func (b *interleaving) WithLock(fn func() error) error {
	err := b.Fake.WithLock(fn)
	if after := b.after; after != nil {
		b.after = nil
		after()
	}
	return err
}

// A write's answer carries the revision its own change produced, read under its lock: another
// write that lands between the lock's release and the answer must not lend it its revision, or
// the next If-Match would pass over a change its sender never read (a lost update).
func TestWriteAnswersItsOwnRevisionWhenAnotherWriteFollows(t *testing.T) {
	other := func(e *settingsEnv) func() {
		return func() {
			_ = e.f.WithLock(func() error {
				if _, err := e.f.Apply(map[string]any{"plugins.headlamp.cluster": "written-by-b"}, nil, nil); err != nil {
					return err
				}
				return e.f.Reload()
			})
		}
	}
	for name, rq := range map[string]struct {
		method, path string
		body         any
	}{
		"PUT plugin":    {http.MethodPut, "/api/v1/settings/plugins/grafana", map[string]any{"url": "https://a.example.com"}},
		"PUT general":   {http.MethodPut, "/api/v1/settings/general", map[string]any{"ui": map[string]any{"iconBaseURL": "https://icons.example.com/"}}},
		"PUT secret":    {http.MethodPut, bearerPath, map[string]any{"value": "new-token-0123456789"}},
		"DELETE secret": {http.MethodDelete, bearerPath, nil},
		"DELETE stored": {http.MethodDelete, proxmoxPath + "/stored/node", nil},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: map[string]any{
				"plugins.proxmox.node":           "pve",
				"plugins.gatus.auth.bearerToken": settingstest.Encrypt(t, "plugins.gatus.auth.bearerToken", "old-token-0123456789"),
			}})
			b := &interleaving{Fake: e.f, after: other(e)}
			e.s.d.Settings = b
			rev0 := e.f.St.Revision()
			res := wantOK(t, e.do(rq.method, rq.path, rq.body, map[string]string{"If-Match": `"` + strconv.FormatUint(rev0, 10) + `"`}))
			if res.Revision != rev0+1 || e.f.St.Revision() != rev0+2 {
				t.Fatalf("answered revision %d, want its own %d (store at %d)", res.Revision, rev0+1, e.f.St.Revision())
			}
			// With the revision it was given, A cannot overwrite B's value unread.
			wantErr(t, e.do(http.MethodPut, "/api/v1/settings/plugins/headlamp", map[string]any{"cluster": "a-never-saw-b"},
				map[string]string{"If-Match": `"` + strconv.FormatUint(res.Revision, 10) + `"`}), http.StatusPreconditionFailed, model.ErrCodePreconditionFailed)
		})
	}
}

// If-Match stays fail-closed: "*" names no revision, so a route that requires one answers 412,
// never 428 and never a write.
func TestIfMatchStarIs412(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	for path, patch := range map[string]map[string]any{
		"/api/v1/settings/general": {"ui": map[string]any{"iconBaseURL": "https://icons.example.com/"}},
		proxmoxPath:                {"node": "pve"},
	} {
		for _, star := range []string{"*", `"*"`, "W/*"} {
			wantErr(t, e.do(http.MethodPut, path, patch, map[string]string{"If-Match": star}), http.StatusPreconditionFailed, model.ErrCodePreconditionFailed)
		}
	}
	if e.f.St.Revision() != 0 {
		t.Errorf("If-Match: * wrote: revision %d", e.f.St.Revision())
	}
}
