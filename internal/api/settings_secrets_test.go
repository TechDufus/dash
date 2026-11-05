package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

const bearerPath = "/api/v1/settings/plugins/gatus/secrets/auth.bearerToken"

func TestSecretReplaceAndClear(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	res := wantOK(t, e.put(bearerPath, map[string]any{"value": "first-token-0123456789"}))
	if res.Revision != 1 || e.f.Resolved().Tree.Plugins.Gatus.Auth.BearerToken.Reveal() != "first-token-0123456789" {
		t.Fatalf("after replace: %+v, running %q", res, e.f.Resolved().Tree.Plugins.Gatus.Auth.BearerToken.Reveal())
	}
	v, _ := config.StoredAt(e.f.Stored(), gatusBearer)
	if !secret.IsEncrypted(v.(string)) {
		t.Error("the stored secret is not encrypted")
	}
	// The field path is the associated data: the ciphertext opens at its own path only.
	keys := e.f.Resolved().Keys
	if plain, err := keys.Decrypt(gatusBearer, v.(string)); err != nil || plain != "first-token-0123456789" {
		t.Errorf("decrypt at its path: %v", err)
	}
	if _, err := keys.Decrypt("plugins.prometheus.auth.bearerToken", v.(string)); !errors.Is(err, secret.ErrDecrypt) {
		t.Errorf("decrypt at another path = %v, want ErrDecrypt", err)
	}
	f := field(t, e.pluginView(t, "gatus").Fields, gatusBearer)
	if !f.Set || !f.Secret || f.Value != nil || f.Source != "ui" || !f.Stored {
		t.Errorf("field after replace = %+v", f)
	}
	wantOK(t, e.do(http.MethodDelete, bearerPath, nil, e.ifMatch()))
	if _, ok := config.StoredAt(e.f.Stored(), gatusBearer); ok || e.f.Resolved().Tree.Plugins.Gatus.Auth.BearerToken.IsSet() {
		t.Error("the secret is still there after clear")
	}
	var actions []string
	for _, r := range e.records() {
		actions = append(actions, string(r.Kind)+" "+r.Action)
	}
	want := []string{"outcome settings.secret.clear", "intent settings.secret.clear", "outcome settings.secret.replace", "intent settings.secret.replace"}
	if !slices.Equal(actions, want) {
		t.Errorf("audit = %q", actions)
	}
}

// A secret replace and clear are audited like a settings.set of the same request shape: the same
// actor, auth mode, target (the plugin) and peer, the request's own id, the field path and
// never the value.
func TestSecretWritesAuditTheirTargetPeerAndRequestID(t *testing.T) {
	const canary = "token-canary-77c1"
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	with := func(id string) map[string]string {
		h := e.ifMatch()
		h["X-Request-Id"] = id
		return h
	}
	wantOK(t, e.do(http.MethodPut, "/api/v1/settings/plugins/gatus", map[string]any{"criticalGroups": []string{"core"}}, with("req-set")))
	wantOK(t, e.do(http.MethodPut, bearerPath, map[string]any{"value": canary}, with("req-replace")))
	wantOK(t, e.do(http.MethodDelete, bearerPath, nil, with("req-clear")))

	byAction := map[string][]audit.Record{}
	for _, r := range e.records() {
		byAction[r.Action] = append(byAction[r.Action], r)
	}
	set := byAction[actionSettingsSet]
	if len(set) != 2 || set[0].Target != (audit.Target{Kind: "plugin", Name: "gatus"}) || set[0].Peer == "" || set[0].RequestID != "req-set" {
		t.Fatalf("settings.set records = %+v", set)
	}
	for action, id := range map[string]string{actionSecretReplace: "req-replace", actionSecretClear: "req-clear"} {
		recs := byAction[action]
		if len(recs) != 2 || recs[0].ID != recs[1].ID {
			t.Fatalf("%s: records = %+v, want an intent and its outcome", action, recs)
		}
		for _, r := range recs {
			if r.Target != set[0].Target || r.Peer != set[0].Peer || r.Actor != set[0].Actor || r.AuthMode != set[0].AuthMode ||
				r.RequestID != id || !slices.Equal(r.Fields, []string{gatusBearer}) || r.Source != "ui" {
				t.Errorf("%s %s = %+v\nwant as settings.set %+v, request id %s", action, r.Kind, r, set[0], id)
			}
		}
	}
	b, _ := json.Marshal(e.records())
	if strings.Contains(string(b), canary) {
		t.Errorf("the secret reached the audit: %s", b)
	}
}

func TestSecretEndpointsMapEveryError(t *testing.T) {
	locked := newSettingsEnv(t, settingstest.Options{SecretKey: true, Env: []string{"DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN=env-token-0123456789"}})
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		got := wantErr(t, locked.do(method, bearerPath, map[string]any{"value": "x"}, nil), http.StatusConflict, model.ErrCodeLockedField)
		if got.Details.Path != gatusBearer || got.Details.Source != "env" || got.Details.Ref != "DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN" {
			t.Errorf("%s locked: %+v", method, got.Details)
		}
	}

	nokey := newSettingsEnv(t, settingstest.Options{})
	got := wantErr(t, nokey.put(bearerPath, map[string]any{"value": "token-0123456789"}), http.StatusConflict, model.ErrCodeConflict)
	if got.Details.Reason != model.SettingsReasonNoSecretKey {
		t.Errorf("no key: %+v", got.Details)
	}

	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	for _, tc := range []struct {
		path string
		body any
		msg  string
	}{
		{"/api/v1/settings/plugins/gatus/secrets/url", map[string]any{"value": "x"}, msgNotUISecret},
		{bearerPath, map[string]any{"value": strings.Repeat("x", maxSecretBytes+1)}, msgSecretTooLong},
		{bearerPath, map[string]any{}, msgSecretBody},
		{bearerPath, map[string]any{"value": "x", "ref": "k8s:a/b#c"}, msgSecretBody},
		{bearerPath, map[string]any{"ref": "k8s:Not_A_Namespace/b#c"}, msgBadK8sRef},
	} {
		got := wantErr(t, e.put(tc.path, tc.body), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
		if len(got.Details.Fields) != 1 || got.Details.Fields[0].Message != tc.msg {
			t.Errorf("%s %v: %+v", tc.path, tc.body, got.Details)
		}
	}
	wantErr(t, e.do(http.MethodPut, bearerPath, `{"value":"x","extra":1}`, e.ifMatch()), http.StatusBadRequest, model.ErrCodeInvalidInput)
	if e.f.St.Revision() != 0 {
		t.Error("a refused secret write changed the store")
	}
}

// The plaintext of a secret reaches no answer, no audit record and no log line.
func TestSecretValueNeverAppearsInAuditOrLogs(t *testing.T) {
	const canary = "secret-canary-31f9c0"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	reg := secret.NewRegistry()
	dir := t.TempDir()
	a, err := audit.Open(dir, audit.Options{Logger: logger, Scrub: reg.Scrub})
	if err != nil {
		t.Fatal(err)
	}
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Registry: reg, Log: logger}, withAudit(a))
	var bodies strings.Builder
	for _, w := range []string{
		e.put(bearerPath, map[string]any{"value": canary}).Body.String(),
		e.put(proxmoxPath, map[string]any{"url": "https://pve.example.com", "tokenID": "u@pve!t", "tokenSecret": canary + "-pve"}).Body.String(),
		e.do(http.MethodGet, "/api/v1/settings", nil, nil).Body.String(),
		e.do(http.MethodGet, "/api/v1/settings/plugins/gatus", nil, nil).Body.String(),
		e.do(http.MethodDelete, bearerPath, nil, nil).Body.String(),
	} {
		bodies.WriteString(w)
	}
	recs, _, _ := a.Recent(100, "", audit.RingMain)
	b, _ := json.Marshal(recs)
	file, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil || len(file) == 0 {
		t.Fatalf("audit file: %v", err)
	}
	for name, s := range map[string]string{"answers": bodies.String(), "audit": string(b), "logs": logs.String(), "audit file": string(file)} {
		if strings.Contains(s, canary) {
			t.Errorf("the secret is in the %s: %s", name, s)
		}
	}
	if strings.Contains(string(file), "enc:v1:") {
		t.Error("a ciphertext reached the audit file: it holds paths only")
	}
	if len(recs) != 6 {
		t.Errorf("%d audit records, want 6", len(recs))
	}
}

// Replacing or clearing a secret leaves its old ciphertext in no backup of the store: through the
// secrets endpoint, and through a patch that re-enters it with a new destination.
func TestSecretReplaceScrubsBackups(t *testing.T) {
	for name, replace := range map[string]func(e *settingsEnv) any{
		"secrets endpoint": func(e *settingsEnv) any {
			return e.put(bearerPath, map[string]any{"value": "new-token-0123456789"})
		},
		"secrets endpoint, clear": func(e *settingsEnv) any {
			return e.do(http.MethodDelete, bearerPath, nil, e.ifMatch())
		},
		"patch": func(e *settingsEnv) any {
			return e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://other.example.com", "auth": map[string]any{"bearerToken": "new-token-0123456789"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			old := settingstest.Encrypt(t, gatusBearer, "old-token-0123456789")
			e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: map[string]any{gatusBearer: old, "plugins.gatus.url": "https://gatus.example.com"}})
			// A write that leaves the secret alone puts the old ciphertext into a backup.
			wantOK(t, e.put("/api/v1/settings/plugins/gatus", map[string]any{"url": "https://gatus.example.com/x"}))
			wantOK(t, replace(e).(*httptest.ResponseRecorder))
			entries, err := os.ReadDir(e.f.Dir)
			if err != nil {
				t.Fatal(err)
			}
			var backups int
			for _, de := range entries {
				b, err := os.ReadFile(filepath.Join(e.f.Dir, de.Name()))
				if err != nil || de.IsDir() {
					continue
				}
				if strings.Contains(de.Name(), ".bak") {
					backups++
				}
				if bytes.Contains(b, []byte(strings.TrimPrefix(old, "enc:v1:"))) {
					t.Errorf("%s still holds the old ciphertext", de.Name())
				}
			}
			if backups == 0 {
				t.Fatal("no backup was written: the test proves nothing")
			}
		})
	}
}

// A secret write whose Reload fails is reverted, and its audit outcome says so: no record calls a
// change ok that is not in effect. The store, the running configuration and the answer are the
// previous state, as for a PUT.
func TestSecretWriteRevertedAfterReloadIsAuditedFailed(t *testing.T) {
	for name, tc := range map[string]struct {
		stored         string // the secret held before, "" for none
		method, action string
		body           any
	}{
		"replace a new secret":  {"", http.MethodPut, actionSecretReplace, map[string]any{"value": "new-token-0123456789"}},
		"replace a held secret": {"old-token-0123456789", http.MethodPut, actionSecretReplace, map[string]any{"value": "new-token-0123456789"}},
		"replace by reference":  {"old-token-0123456789", http.MethodPut, actionSecretReplace, map[string]any{"ref": "k8s:dash/gatus#token"}},
		"clear":                 {"old-token-0123456789", http.MethodDelete, actionSecretClear, nil},
	} {
		t.Run(name, func(t *testing.T) {
			o := settingstest.Options{SecretKey: true, Namespace: "dash"}
			if tc.stored != "" {
				o.Stored = map[string]any{gatusBearer: settingstest.Encrypt(t, gatusBearer, tc.stored)}
			}
			e := newSettingsEnv(t, o)
			before := e.f.Stored()
			calls := 0
			e.f.BeforeReload = func() error {
				if calls++; calls == 1 {
					return &config.FieldError{Path: gatusBearer, Message: "refused by the reload"}
				}
				return nil
			}
			got := wantErr(t, e.do(tc.method, bearerPath, tc.body, e.ifMatch()), http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
			if len(got.Details.Fields) != 1 || got.Details.Fields[0].Path != gatusBearer {
				t.Errorf("details = %+v", got.Details)
			}
			if !reflectEqual(e.f.Stored(), before) {
				t.Errorf("the store was not reverted: %v", e.f.Stored())
			}
			if running := e.f.Resolved().Tree.Plugins.Gatus.Auth.BearerToken.Reveal(); running != tc.stored {
				t.Errorf("running secret changed: set %v", running != "")
			}
			recs := e.records()
			if len(recs) != 2 || recs[1].Kind != audit.KindIntent || recs[0].Kind != audit.KindOutcome || recs[0].Action != tc.action ||
				recs[0].Result != audit.ResultError || recs[0].Error != errSettingsReload.Error() {
				t.Errorf("audit = %+v", recs)
			}
		})
	}
}
