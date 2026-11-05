package api

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
)

// upstream is a fake upstream behind the checks' transport: it records each request's host and
// Authorization header and answers 200 without dialing anything.
type fakeUpstream struct {
	mu   sync.Mutex
	seen []string // "host authorization"
}

func (u *fakeUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.seen = append(u.seen, r.URL.Host+" "+r.Header.Get("Authorization"))
	u.mu.Unlock()
	body := `[]`
	if strings.Contains(r.URL.Path, "/api2/json/version") {
		body = `{"data":{"version":"8.2"}}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (u *fakeUpstream) requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.seen)
}

// checkSupervisor is a running supervisor with the real gatus and proxmox plugins, whose checks
// go through rt when it is set (the dial guard still runs inside a real transport otherwise).
func checkSupervisor(t *testing.T, rt http.RoundTripper) *plugin.Supervisor {
	t.Helper()
	opts := plugin.Options{
		Publisher: &plugintest.Hub{}, Settings: &plugintest.Settings{}, StatusEvery: time.Hour,
		StopTimeout: time.Second, Log: slog.New(slog.DiscardHandler),
	}
	if rt != nil {
		opts.Transport = func(plugin.ID, http.RoundTripper) http.RoundTripper { return rt }
	}
	sup := plugin.NewSupervisor(opts)
	if err := sup.Add(gatus.New, proxmox.New); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	return sup
}

func (e *settingsEnv) test(id string, body any) *httptest.ResponseRecorder {
	return e.do(http.MethodPost, "/api/v1/settings/plugins/"+id+"/test", body, nil)
}

func checkReport(t *testing.T, w *httptest.ResponseRecorder) model.CheckReport {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("test = %d %s", w.Code, w.Body.String())
	}
	return decodeBody[model.CheckReport](t, w)
}

const gatusBearer = "plugins.gatus.auth.bearerToken"

func gatusStored(t *testing.T, token string) map[string]any {
	return map[string]any{
		"plugins.gatus.url": "https://gatus.example.com", gatusBearer: settingstest.Encrypt(t, gatusBearer, token),
	}
}

// capture is a Checker for gatus that records the URL and bearer token of every candidate.
func capture() (settingstest.Checks, func() []string) {
	var mu sync.Mutex
	var seen []string
	c := settingstest.Checks{"gatus": func(_ context.Context, cfg any, _ bool) (plugin.CheckReport, error) {
		mu.Lock()
		defer mu.Unlock()
		g := cfg.(*pluginconf.Gatus)
		seen = append(seen, g.URL+" "+g.Auth.BearerToken.Reveal())
		return plugin.CheckReport{Checks: []plugin.CheckResult{{Name: "read", OK: true}}}, nil
	}}
	return c, func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(seen) }
}

// The canary of design D16: a stored secret goes to the destination it was saved for, never to
// a changed one. The candidate the check gets has no secret, and a real upstream behind a changed
// TLS setting receives no Authorization header.
func TestTestConnectionSendsNoStoredSecretToAChangedHost(t *testing.T) {
	const canary = "stored-token-canary-0a3f"
	checks, seen := capture()
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: gatusStored(t, canary), Checker: checks})
	checkReport(t, e.test("gatus", map[string]any{}))
	checkReport(t, e.test("gatus", map[string]any{"patch": map[string]any{"url": "https://evil.example.com"}}))
	checkReport(t, e.test("gatus", map[string]any{"patch": map[string]any{"url": "https://gatus.example.com:8443"}}))
	checkReport(t, e.test("gatus", map[string]any{"patch": map[string]any{"url": "http://gatus.example.com:443"}})) // clear text
	want := []string{"https://gatus.example.com " + canary, "https://evil.example.com ", "https://gatus.example.com:8443 ", "http://gatus.example.com:443 "}
	if got := seen(); !slices.Equal(got, want) {
		t.Errorf("candidates %q, want %q", got, want)
	}
	if got := e.f.Resolved().Tree.Plugins.Gatus.Auth.BearerToken.Reveal(); got != canary {
		t.Errorf("the running secret changed to %q", got)
	}

	// A real upstream: the operator's URL (loopback is theirs to reach), the stored secret, and a
	// TLS change from the UI.
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	live := newSettingsEnv(t, settingstest.Options{
		SecretKey: true, Checker: checkSupervisor(t, nil),
		File:   "plugins:\n  gatus:\n    url: " + srv.URL + "\n    tls:\n      insecureSkipVerify: true\n",
		Stored: map[string]any{gatusBearer: settingstest.Encrypt(t, gatusBearer, canary)},
	})
	for _, body := range []map[string]any{{}, {"patch": map[string]any{"tls": map[string]any{"caFile": ca, "insecureSkipVerify": false}}}} {
		if rep := checkReport(t, live.test("gatus", body)); len(rep.Checks) != 1 || !rep.Checks[0].OK {
			t.Fatalf("%v: report %+v", body, rep)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(auths, []string{"Bearer " + canary, ""}) {
		t.Errorf("the upstream saw Authorization %q", auths)
	}
}

// A typed secret is used for the test and kept nowhere: not in the store, not in what runs.
func TestTestConnectionUsesSuppliedSecretOnlyInMemory(t *testing.T) {
	const typed = "typed-token-0123456789"
	checks, seen := capture()
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true, Stored: gatusStored(t, "old-token-0123456789"), Checker: checks})
	rev, before := e.f.St.Revision(), e.f.Stored()
	checkReport(t, e.test("gatus", map[string]any{
		"patch": map[string]any{"url": "https://new.example.com"}, "secrets": map[string]any{"auth.bearerToken": typed},
	}))
	if got := seen(); !slices.Equal(got, []string{"https://new.example.com " + typed}) {
		t.Errorf("candidates %q", got)
	}
	if e.f.St.Revision() != rev || !reflectEqual(e.f.Stored(), before) {
		t.Error("the test wrote the store")
	}
	res := e.f.Resolved()
	if res.Tree.Plugins.Gatus.URL != "https://gatus.example.com" || res.Tree.Plugins.Gatus.Auth.BearerToken.Reveal() != "old-token-0123456789" {
		t.Error("the test changed the running configuration")
	}
	// Without a key the typed secret cannot be read like a stored one.
	nk := newSettingsEnv(t, settingstest.Options{Checker: checks})
	wantErr(t, nk.test("gatus", map[string]any{"secrets": map[string]any{"auth.bearerToken": typed}}), http.StatusConflict, model.ErrCodeConflict)
}

func TestTestConnectionRefusesALockedSecretOnAChangedHost(t *testing.T) {
	up := &fakeUpstream{}
	e := newSettingsEnv(t, settingstest.Options{
		Stored:  map[string]any{"plugins.gatus.url": "https://gatus.example.com"},
		Env:     []string{"DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN=env-token-0123456789"},
		Checker: checkSupervisor(t, up),
	})
	got := wantErr(t, e.test("gatus", map[string]any{"patch": map[string]any{"url": "https://evil.example.com"}}), http.StatusConflict, model.ErrCodeLockedField)
	if got.Details.Path != gatusBearer || got.Details.Reason != model.SettingsReasonSecretLocked {
		t.Errorf("details = %+v", got.Details)
	}
	if len(up.requests()) != 0 {
		t.Errorf("a refused test reached the upstream: %q", up.requests())
	}
}

// A secret the environment or the file sets cannot be replaced for a test by typing another: the
// test would pair the operator's locked configuration with a host typed into the form, and a
// test answers it as the secrets endpoint does, 409 naming the layer. Literal and reference
// (a k8s: one included) alike; the check never runs.
func TestTestConnectionRefusesASuppliedSecretForALockedField(t *testing.T) {
	for name, tc := range map[string]struct {
		o           settingstest.Options
		source, ref string
	}{
		"env literal":  {settingstest.Options{Env: []string{"DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN=env-token-0123456789"}}, "env", "DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN"},
		"env k8s ref":  {settingstest.Options{Env: []string{"DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN=k8s:dash/g#t"}}, "env", "DASH_PLUGINS_GATUS_AUTH_BEARER_TOKEN"},
		"file literal": {settingstest.Options{File: "plugins:\n  gatus:\n    auth:\n      bearerToken: file-token-0123456789\n"}, "file", ""},
	} {
		t.Run(name, func(t *testing.T) {
			checks, seen := capture()
			o := tc.o
			o.SecretKey, o.Checker, o.Namespace = true, checks, "dash"
			o.Stored = map[string]any{"plugins.gatus.url": "https://gatus.example.com"}
			e := newSettingsEnv(t, o)
			for _, patch := range []map[string]any{{}, {"url": "https://evil.example.com"}, {"tls": map[string]any{"insecureSkipVerify": true}}} {
				got := wantErr(t, e.test("gatus", map[string]any{"patch": patch, "secrets": map[string]any{"auth.bearerToken": "typed-0123456789"}}),
					http.StatusConflict, model.ErrCodeLockedField)
				if got.Details.Path != gatusBearer || string(got.Details.Source) != tc.source || (tc.ref != "" && got.Details.Ref != tc.ref) {
					t.Errorf("%v: details = %+v", patch, got.Details)
				}
			}
			if got := seen(); len(got) != 0 {
				t.Errorf("a refused test reached the check: %q", got)
			}
		})
	}
}

// blocked reports whether the report's one check is the dial guard's refusal.
func blocked(rep model.CheckReport) bool {
	return len(rep.Checks) == 1 && !rep.Checks[0].OK && rep.Checks[0].Category == "unreachable" && rep.Checks[0].Message == "blocked address"
}

func TestTestConnectionFromUIRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") }))
	t.Cleanup(srv.Close)
	e := newSettingsEnv(t, settingstest.Options{Checker: checkSupervisor(t, nil)})
	if rep := checkReport(t, e.test("gatus", map[string]any{"patch": map[string]any{"url": srv.URL}})); !blocked(rep) {
		t.Errorf("report = %+v", rep)
	}
}

// A URL saved from the UI cannot reach loopback; the operator's can.
func TestUIURLToLoopbackFailsToDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "[]") }))
	t.Cleanup(srv.Close)
	ui := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{"plugins.gatus.url": srv.URL}, Checker: checkSupervisor(t, nil)})
	if rep := checkReport(t, ui.test("gatus", map[string]any{})); !blocked(rep) {
		t.Errorf("UI URL: report = %+v", rep)
	}
	file := newSettingsEnv(t, settingstest.Options{File: "plugins:\n  gatus:\n    url: " + srv.URL + "\n", Checker: checkSupervisor(t, nil)})
	if rep := checkReport(t, file.test("gatus", map[string]any{})); len(rep.Checks) != 1 || !rep.Checks[0].OK {
		t.Errorf("file URL: report = %+v", rep)
	}
}

func TestMetadataAddressRefusedForEverySource(t *testing.T) {
	for _, u := range []string{"http://169.254.169.254", "http://[fd00:ec2::254]"} {
		for name, o := range map[string]settingstest.Options{
			"file": {File: "plugins:\n  gatus:\n    url: " + u + "\n"},
			"env":  {Env: []string{"DASH_PLUGINS_GATUS_URL=" + u}},
			"ui":   {Stored: map[string]any{"plugins.gatus.url": u}},
		} {
			o.Checker = checkSupervisor(t, nil)
			e := newSettingsEnv(t, o)
			if rep := checkReport(t, e.test("gatus", map[string]any{})); !blocked(rep) {
				t.Errorf("%s from %s: report = %+v", u, name, rep)
			}
		}
	}
}

// A new k8s: reference has no value in a candidate: the check says not configured, and no handle
// is made for it (ruling 6).
func TestTestConnectionWithANewK8sRefIsNotConfigured(t *testing.T) {
	up := &fakeUpstream{}
	e := newSettingsEnv(t, settingstest.Options{Namespace: "dash", Checker: checkSupervisor(t, up)})
	rep := checkReport(t, e.test("proxmox", map[string]any{
		"patch":   map[string]any{"url": "https://pve.example.com:8006", "tokenID": "u@pve!t"},
		"secrets": map[string]any{"tokenSecret": "k8s:dash/pve#token"},
	}))
	if len(rep.Checks) != 1 || rep.Checks[0].Category != "not_configured" {
		t.Errorf("report = %+v", rep)
	}
	if refs := e.f.Refs.Refs(); len(refs) != 0 || len(up.requests()) != 0 {
		t.Errorf("handles %v, requests %q", refs, up.requests())
	}
}

func TestTestConnectionIsRateLimited(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Checker: settingstest.Checks{
		"gatus": func(context.Context, any, bool) (plugin.CheckReport, error) {
			return plugin.CheckReport{Checks: []plugin.CheckResult{{Name: "read", OK: true}}}, nil
		},
	}})
	for i := range 10 {
		if w := e.test("gatus", map[string]any{}); w.Code != http.StatusOK {
			t.Fatalf("test %d = %d", i+1, w.Code)
		}
	}
	w := e.test("gatus", map[string]any{})
	wantErr(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	// The window slides with the server's clock.
	e.s.now = func() time.Time { return fixedNow.Add(time.Minute + time.Second) }
	if w := e.test("gatus", map[string]any{}); w.Code != http.StatusOK {
		t.Errorf("after the window: %d", w.Code)
	}
	var tests int
	for _, rec := range e.records() {
		if rec.Action == actionSettingsTest {
			tests++
			if rec.Kind != audit.KindEvent || rec.Plugin != "gatus" || len(rec.Fields) != 0 {
				t.Errorf("record = %+v", rec)
			}
		}
	}
	if tests != 11 {
		t.Errorf("%d settings.test events, want 11", tests)
	}
}

func TestPluginWithoutCheckerHasNoTestRoute(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{Checker: settingstest.Checks{
		"gatus": func(context.Context, any, bool) (plugin.CheckReport, error) { return plugin.CheckReport{}, nil },
	}})
	wantErr(t, e.test("grafana", map[string]any{}), http.StatusNotFound, model.ErrCodeNotFound)
	view := decodeBody[model.SettingsView](t, e.do(http.MethodGet, "/api/v1/settings", nil, nil))
	for _, p := range view.Plugins {
		if p.Testable != (p.ID == "gatus") {
			t.Errorf("%s: testable %v", p.ID, p.Testable)
		}
	}
	if e.pluginView(t, "grafana").Testable || !e.pluginView(t, "gatus").Testable {
		t.Error("plugin view testable is wrong")
	}
}

// The plugin's refusal of a candidate can hold values: the answer is fixed text (ruling 8).
func TestTestConnectionRefusalIsCleaned(t *testing.T) {
	const canary = "refusal-canary-77ab"
	e := newSettingsEnv(t, settingstest.Options{Checker: settingstest.Checks{
		"gatus": func(context.Context, any, bool) (plugin.CheckReport, error) {
			return plugin.CheckReport{}, errors.New("gatus: url " + canary + " is wrong")
		},
	}})
	w := e.test("gatus", map[string]any{})
	got := wantErr(t, w, http.StatusUnprocessableEntity, model.ErrCodeInvalidInput)
	if got.Details.Reason != model.SettingsReasonInvalidConfiguration || strings.Contains(w.Body.String(), canary) {
		t.Errorf("answer = %s", w.Body.String())
	}
	for _, rec := range e.records() {
		if strings.Contains(rec.Error, canary) {
			t.Errorf("audit = %+v", rec)
		}
	}
}

// A failed connection test is audited with the categories of its failed checks as its error:
// fixed text, sorted, each once. A category outside the fixed set, which a plugin could fill
// with a URL or a message, is recorded as "failed"; a passing test records no error.
func TestTestConnectionAuditsFailedCategories(t *testing.T) {
	const canary = "https://category-canary.example.com"
	var checks []plugin.CheckResult
	e := newSettingsEnv(t, settingstest.Options{Checker: settingstest.Checks{
		"gatus": func(context.Context, any, bool) (plugin.CheckReport, error) {
			return plugin.CheckReport{Checks: checks}, nil
		},
	}})
	for _, tc := range []struct {
		checks []plugin.CheckResult
		result string
		err    string
	}{
		{[]plugin.CheckResult{{Name: "read", OK: true}}, "ok", ""},
		{[]plugin.CheckResult{
			{Name: "a", Category: "unreachable"}, {Name: "b", Category: "auth"}, {Name: "c", OK: true}, {Name: "d", Category: "unreachable"},
		}, audit.ResultError, "auth, unreachable"},
		{[]plugin.CheckResult{{Name: "a", Category: "http_503"}, {Name: "b", Category: canary}, {Name: "c", Category: "http_5xx"}}, audit.ResultError, "failed, http_503"},
	} {
		checks = tc.checks
		checkReport(t, e.test("gatus", map[string]any{}))
		rec := e.records()[0]
		if rec.Action != actionSettingsTest || rec.Result != tc.result || rec.Error != tc.err || rec.Reason != "" {
			t.Errorf("%+v: record result %q error %q reason %q, want %q %q", tc.checks, rec.Result, rec.Error, rec.Reason, tc.result, tc.err)
		}
	}
}
