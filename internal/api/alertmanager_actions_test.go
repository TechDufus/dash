package api

// The silence actions of the alertmanager plugin, through the whole API chain (host check, CSRF,
// auth, the core's action pipeline: role, rate limit, strict body, audit intent and outcome) and
// against a fake Alertmanager. They replace the legacy matrix of silences_test.go and the routes
// POST /api/silences and DELETE /api/silences/{id}.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
)

// amUpstream is a minimal Alertmanager: the silence list, and what was created and expired.
type amUpstream struct {
	srv *httptest.Server

	mu       sync.Mutex
	silences string
	fail     int
	created  []map[string]any
	expired  []string
}

const (
	amSilenceID      = "abcdef12-3456"
	amDashSilence    = "5b1c2d3e-aaaa-bbbb-cccc-0123456789ab"
	amForeignSilence = "7c7c7c7c-bbbb-cccc-dddd-0123456789ab"
)

func newAMUpstream(t *testing.T) *amUpstream {
	t.Helper()
	u := &amUpstream{silences: fmt.Sprintf(`[
	  {"id":%q,"status":{"state":"active"},"matchers":[{"name":"alertname","value":"Muted","isRegex":false,"isEqual":true}],"createdBy":"dash:alice","comment":"c","startsAt":"2026-10-30T20:00:00Z","endsAt":"2026-10-30T22:00:00Z"},
	  {"id":%q,"status":{"state":"active"},"matchers":[],"createdBy":"carol@example.com","comment":"from the UI","startsAt":"2026-10-30T20:00:00Z","endsAt":"2026-10-30T22:00:00Z"}]`,
		amDashSilence, amForeignSilence)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.fail != 0 {
			http.Error(w, "boom", u.fail)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/alerts":
			io.WriteString(w, "[]")
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/silences":
			io.WriteString(w, u.silences)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			u.created = append(u.created, b)
			fmt.Fprintf(w, `{"silenceID":%q}`, amSilenceID)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/silence/"):
			u.expired = append(u.expired, strings.TrimPrefix(r.URL.Path, "/api/v2/silence/"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *amUpstream) setFail(code int) { u.mu.Lock(); u.fail = code; u.mu.Unlock() }

func (u *amUpstream) seen() (created []map[string]any, expired []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.created), slices.Clone(u.expired)
}

// amPlugin registers the real alertmanager plugin.
type amPlugin struct{}

func (amPlugin) Factory() plugin.Factory { return alertmanager.New }

type amEnv struct {
	*pluginEnv
	am *amUpstream
	m  *obs.Metrics
}

// newAMEnv serves the alertmanager plugin, with its actions enabled unless mod turns them off,
// through a Server with metrics on.
func newAMEnv(t *testing.T, mod ...func(*pluginconf.Alertmanager)) *amEnv {
	t.Helper()
	u := newAMUpstream(t)
	cfg := pluginconf.DefaultAlertmanager()
	cfg.URL = u.srv.URL
	cfg.Actions.Enabled = true
	for _, m := range mod {
		m(&cfg)
	}
	e := newPluginEnv(t, []plugin.InstanceConfig{{Name: "alertmanager", Type: "alertmanager", Config: &cfg, ActionsEnabled: cfg.Actions.Enabled}}, amPlugin{})
	ae := &amEnv{pluginEnv: e, am: u, m: obs.New("test", RoutePatterns(), e.sup.Infos)}
	e.Server.d.Metrics = ae.m
	e.eventually(t, "alertmanager to run", func() bool { return e.sup.Running("alertmanager") })
	return ae
}

const (
	amCreatePath = "/api/v1/plugins/alertmanager/actions/silence.create"
	amExpirePath = "/api/v1/plugins/alertmanager/actions/silence.expire"
)

func (e *amEnv) post(path string, body any) *httptest.ResponseRecorder {
	return req{method: http.MethodPost, path: path, body: body}.do(e.Handler())
}

func amSilenceBody(mod func(*model.SilenceRequest)) model.SilenceRequest {
	r := model.SilenceRequest{
		Matchers: []model.SilenceMatcher{{Name: "alertname", Value: "HighTemp"}, {Name: "instance", Value: "pve-730"}},
		Duration: "4h",
		Comment:  "maintenance window",
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestAlertmanagerSilenceCreateMatrix(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*model.SilenceRequest)
		code int
		dur  time.Duration
		err  string
	}{
		{"1h", func(r *model.SilenceRequest) { r.Duration = "1h" }, 200, time.Hour, ""},
		{"4h", nil, 200, 4 * time.Hour, ""},
		{"24h", func(r *model.SilenceRequest) { r.Duration = "24h" }, 200, 24 * time.Hour, ""},
		{"7d", func(r *model.SilenceRequest) { r.Duration = "7d" }, 200, 168 * time.Hour, ""},
		{"2h rejected", func(r *model.SilenceRequest) { r.Duration = "2h" }, 400, 0, model.ErrCodeInvalidInput},
		{"empty duration", func(r *model.SilenceRequest) { r.Duration = "" }, 400, 0, model.ErrCodeInvalidInput},
		{"30d rejected", func(r *model.SilenceRequest) { r.Duration = "30d" }, 400, 0, model.ErrCodeInvalidInput},
		{"no alertname matcher", func(r *model.SilenceRequest) { r.Matchers = []model.SilenceMatcher{{Name: "instance", Value: "x"}} }, 400, 0, model.ErrCodeInvalidInput},
		{"no matchers", func(r *model.SilenceRequest) { r.Matchers = nil }, 400, 0, model.ErrCodeInvalidInput},
		{"empty comment", func(r *model.SilenceRequest) { r.Comment = "" }, 400, 0, model.ErrCodeInvalidInput},
		{"whitespace comment", func(r *model.SilenceRequest) { r.Comment = " \t\n " }, 400, 0, model.ErrCodeInvalidInput},
		{"500 char comment ok", func(r *model.SilenceRequest) { r.Comment = strings.Repeat("x", 500) }, 200, 4 * time.Hour, ""},
		{"501 char comment", func(r *model.SilenceRequest) { r.Comment = strings.Repeat("x", 501) }, 400, 0, model.ErrCodeInvalidInput},
		{"empty value", func(r *model.SilenceRequest) { r.Matchers[1].Value = "" }, 400, 0, model.ErrCodeInvalidInput},
		{"empty name", func(r *model.SilenceRequest) { r.Matchers[1].Name = "" }, 400, 0, model.ErrCodeInvalidInput},
		{"21 matchers", func(r *model.SilenceRequest) {
			r.Matchers = []model.SilenceMatcher{{Name: "alertname", Value: "A"}}
			for range 20 {
				r.Matchers = append(r.Matchers, model.SilenceMatcher{Name: "l", Value: "v"})
			}
		}, 400, 0, model.ErrCodeInvalidInput},
		{"Watchdog is protected", func(r *model.SilenceRequest) { r.Matchers[0].Value = "Watchdog" }, 403, 0, model.ErrCodeForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newAMEnv(t)
			w := e.post(amCreatePath, amSilenceBody(tc.mod))
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			created, _ := e.am.seen()
			if tc.code != 200 {
				if got := wantError(t, w, tc.code, tc.err); got.Error == "" {
					t.Error("empty error")
				}
				if len(created) != 0 {
					t.Error("Alertmanager must not be called")
				}
				return
			}
			if len(created) != 1 {
				t.Fatalf("created = %v", created)
			}
			c := created[0]
			start, _ := time.Parse(time.RFC3339, c["startsAt"].(string))
			end, _ := time.Parse(time.RFC3339, c["endsAt"].(string))
			if end.Sub(start) != tc.dur {
				t.Errorf("length = %v, want %v", end.Sub(start), tc.dur)
			}
			if d := time.Since(start); d < -time.Minute || d > time.Minute {
				t.Errorf("startsAt = %v", start)
			}
			if c["createdBy"] != "dash:alice" {
				t.Errorf("createdBy = %v", c["createdBy"])
			}
			var resp model.SilenceResponse
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.SilenceID != amSilenceID {
				t.Errorf("response = %s", w.Body)
			}
		})
	}
}

func TestAlertmanagerSilenceCreateDetails(t *testing.T) {
	t.Run("comment is trimmed and the matchers go as exact ones", func(t *testing.T) {
		e := newAMEnv(t)
		e.post(amCreatePath, amSilenceBody(func(r *model.SilenceRequest) { r.Comment = "  hi  " }))
		created, _ := e.am.seen()
		if len(created) != 1 || created[0]["comment"] != "hi" {
			t.Fatalf("created = %v", created)
		}
		ms := created[0]["matchers"].([]any)
		if len(ms) != 2 || ms[0].(map[string]any)["name"] != "alertname" || ms[0].(map[string]any)["isEqual"] != true || ms[0].(map[string]any)["isRegex"] != false {
			t.Errorf("matchers = %v", ms)
		}
	})
	t.Run("an unknown field is a 400", func(t *testing.T) {
		e := newAMEnv(t)
		w := e.post(amCreatePath, `{"matchers":[{"name":"alertname","value":"A"}],"duration":"1h","comment":"c","regex":true}`)
		wantError(t, w, 400, model.ErrCodeInvalidInput)
		if created, _ := e.am.seen(); len(created) != 0 {
			t.Error("Alertmanager was called")
		}
	})
	t.Run("an Alertmanager failure is a 502 that names no URL, and audited as an error against the alert", func(t *testing.T) {
		e := newAMEnv(t)
		e.am.setFail(http.StatusInternalServerError)
		w := e.post(amCreatePath, amSilenceBody(nil))
		got := wantError(t, w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable)
		if strings.Contains(got.Error, "127.0.0.1") || strings.Contains(got.Error, "http://") {
			t.Errorf("error = %q", got.Error)
		}
		recs := auditRecords(t, e.dir)
		if len(recs) != 2 || recs[1].Result != audit.ResultError {
			t.Fatalf("records = %+v", recs)
		}
		for _, r := range recs {
			if r.Plugin != "alertmanager" || r.Action != "silence.create" || r.Target != (audit.Target{Kind: "Silence", Name: "HighTemp"}) {
				t.Errorf("record = %+v, want the intent's target Silence/HighTemp on a failure", r)
			}
		}
	})
	t.Run("success audits the silence id, with the request id and the user", func(t *testing.T) {
		e := newAMEnv(t)
		w := e.post(amCreatePath, amSilenceBody(nil))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		recs := auditRecords(t, e.dir)
		if len(recs) != 2 || recs[0].Kind != audit.KindIntent || recs[1].Kind != audit.KindOutcome {
			t.Fatalf("records = %+v", recs)
		}
		intent, outcome := recs[0], recs[1]
		if intent.Target != (audit.Target{Kind: "Silence", Name: "HighTemp"}) {
			t.Errorf("intent target = %+v, want the alert", intent.Target)
		}
		if outcome.Target != (audit.Target{Kind: "Silence", Name: amSilenceID}) || outcome.Result != audit.ResultOK {
			t.Errorf("outcome = %+v, want Silence/%s ok", outcome, amSilenceID)
		}
		for _, r := range recs {
			if r.Plugin != "alertmanager" || r.Action != "silence.create" || r.Actor.User != "alice" || r.RequestID == "" || r.RequestID != w.Header().Get("X-Request-Id") {
				t.Errorf("record = %+v (response request id %q)", r, w.Header().Get("X-Request-Id"))
			}
		}
	})
}

func TestAlertmanagerSilenceExpire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     string
		fail   int
		code   int
		errTag string
		expire bool // Alertmanager receives the DELETE
	}{
		{"a silence dash created", amDashSilence, 0, 202, "", true},
		{"a malformed id", "bad", 0, 400, model.ErrCodeInvalidInput, false},
		{"a silence nobody knows", "00000000-1111-2222-3333-444444444444", 0, 404, model.ErrCodeNotFound, false},
		{"a silence made in Alertmanager itself", amForeignSilence, 0, 403, model.ErrCodeForbidden, false},
		{"Alertmanager down", amDashSilence, http.StatusBadGateway, 502, model.ErrCodeUpstreamUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAMEnv(t)
			e.am.setFail(tc.fail)
			w := e.post(amExpirePath, map[string]string{"id": tc.id})
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.errTag != "" {
				wantError(t, w, tc.code, tc.errTag)
			} else if strings.TrimSpace(w.Body.String()) != `{"status":"accepted"}` {
				t.Errorf("body = %q", w.Body)
			}
			_, expired := e.am.seen()
			if got := slices.Contains(expired, tc.id); got != tc.expire {
				t.Errorf("DELETE reached Alertmanager = %v, want %v", got, tc.expire)
			}
			// A malformed id never reaches the action: no intent to record.
			recs := auditRecords(t, e.dir)
			if tc.code == 400 {
				if len(recs) != 0 {
					t.Errorf("records = %+v", recs)
				}
				return
			}
			if len(recs) != 2 || recs[0].Action != "silence.expire" || recs[0].Plugin != "alertmanager" ||
				recs[0].Target != (audit.Target{Kind: "Silence", Name: tc.id}) || recs[1].Target != recs[0].Target {
				t.Errorf("records = %+v", recs)
			}
			if want := map[bool]string{true: audit.ResultOK, false: audit.ResultError}[tc.code == 202]; recs[1].Result != want {
				t.Errorf("result = %q, want %q", recs[1].Result, want)
			}
		})
	}
}

// The protected alert is refused and the refusal is on the record: an intent and an error
// outcome against Silence/Watchdog.
func TestAlertmanagerProtectedAlertIs403AndAudited(t *testing.T) {
	e := newAMEnv(t)
	w := e.post(amCreatePath, amSilenceBody(func(r *model.SilenceRequest) { r.Matchers[0].Value = "Watchdog" }))
	wantError(t, w, http.StatusForbidden, model.ErrCodeForbidden)
	recs := auditRecords(t, e.dir)
	if len(recs) != 2 || recs[0].Kind != audit.KindIntent || recs[1].Kind != audit.KindOutcome || recs[1].Result != audit.ResultError {
		t.Fatalf("records = %+v", recs)
	}
	if recs[0].Target != (audit.Target{Kind: "Silence", Name: "Watchdog"}) || recs[0].Plugin != "alertmanager" || recs[0].Action != "silence.create" {
		t.Errorf("intent = %+v", recs[0])
	}
	if created, _ := e.am.seen(); len(created) != 0 {
		t.Error("Alertmanager was called")
	}
}

// Without actions.enabled the row lists no action and both routes are 404; with it, both are
// listed for an admin with their minRole.
func TestAlertmanagerActionsFollowTheirToggle(t *testing.T) {
	e := newAMEnv(t, func(c *pluginconf.Alertmanager) { c.Actions.Enabled = false })
	if row := e.infos(t)["alertmanager"]; len(row.Actions) != 0 {
		t.Errorf("actions listed while disabled: %+v", row.Actions)
	}
	wantError(t, e.post(amCreatePath, amSilenceBody(nil)), http.StatusNotFound, model.ErrCodeNotFound)
	wantError(t, e.post(amExpirePath, map[string]string{"id": amDashSilence}), http.StatusNotFound, model.ErrCodeNotFound)
	if created, _ := e.am.seen(); len(created) != 0 {
		t.Error("Alertmanager was called")
	}

	on := newAMEnv(t)
	var names []string
	for _, a := range on.infos(t)["alertmanager"].Actions {
		names = append(names, a.Name)
		if a.MinRole != "admin" || a.Destructive {
			t.Errorf("action %+v", a)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"silence.create", "silence.expire"}) {
		t.Errorf("row actions = %v", names)
	}
	var me struct {
		Scopes       []string `json:"scopes"`
		Capabilities struct {
			Actions bool `json:"actions"`
		} `json:"capabilities"`
	}
	w := req{path: "/api/v1/me"}.do(on.Handler())
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || !me.Capabilities.Actions || !slices.Contains(me.Scopes, "actions:alertmanager") {
		t.Errorf("me = %s (%v)", w.Body, err)
	}
	// A viewer does not meet the actions' minRole: no scope, no capability.
	if vm := meAs(t, on.Server, auth.RoleViewer); vm.Capabilities.Actions || slices.Contains(vm.Scopes, "actions:alertmanager") {
		t.Errorf("viewer me = scopes %v, actions %v; want neither", vm.Scopes, vm.Capabilities.Actions)
	}
	if am := meAs(t, on.Server, auth.RoleAdmin); !am.Capabilities.Actions || !slices.Contains(am.Scopes, "actions:alertmanager") {
		t.Errorf("admin me = scopes %v, actions %v", am.Scopes, am.Capabilities.Actions)
	}
}

func TestAlertmanagerEleventhActionInAMinuteIs429(t *testing.T) {
	e := newAMEnv(t)
	for i := 1; i <= actionRateLimit; i++ {
		if w := e.post(amExpirePath, map[string]string{"id": amDashSilence}); w.Code != http.StatusAccepted {
			t.Fatalf("action %d = %d %s", i, w.Code, w.Body)
		}
	}
	w := e.post(amCreatePath, amSilenceBody(nil))
	wantError(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if created, _ := e.am.seen(); len(created) != 0 {
		t.Error("the limited create reached Alertmanager")
	}
}

func TestAlertmanagerActionMetrics(t *testing.T) {
	e := newAMEnv(t)
	e.post(amCreatePath, amSilenceBody(nil))
	e.am.setFail(http.StatusInternalServerError)
	e.post(amCreatePath, amSilenceBody(nil))
	e.am.setFail(0)
	e.post(amExpirePath, map[string]string{"id": amDashSilence})

	ss := scrapeMetrics(t, e.Server)
	for _, want := range []struct {
		action, outcome string
		n               float64
	}{
		{"silence.create", obs.ActionOK, 1},
		{"silence.create", obs.ActionError, 1},
		{"silence.expire", obs.ActionOK, 1},
	} {
		got, ok := seriesValue(t, ss, "dash_actions_total", map[string]string{"plugin": "alertmanager", "action": want.action, "outcome": want.outcome})
		if !ok || got != want.n {
			t.Errorf("dash_actions_total %s/%s = %v (present %v), want %v", want.action, want.outcome, got, ok, want.n)
		}
	}
}

// A body that stalls is a 400 at the body deadline and the action does not run; a complete body
// whose action outlasts the deadline still succeeds.
func TestAlertmanagerActionBodyDeadline(t *testing.T) {
	e := newAMEnv(t)
	e.bodyTimeout = 200 * time.Millisecond
	ts := httptest.NewServer(e.Handler())
	t.Cleanup(ts.Close)

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-Preferred-Username: alice\r\n"+
		"X-Dash-Csrf: 1\r\nOrigin: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"matchers\":",
		amCreatePath, testHost, testOrigin)
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled body after %v: %v", time.Since(start), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("400 after %v, want about the 200ms body deadline", el)
	}
	if created, _ := e.am.seen(); len(created) != 0 {
		t.Errorf("the action ran: %v", created)
	}

	body, _ := json.Marshal(amSilenceBody(nil))
	r, _ := http.NewRequest(http.MethodPost, ts.URL+amCreatePath, bytes.NewReader(body))
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
	r.Header.Set("X-Dash-Csrf", "1")
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	res, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("a complete body = %d", res.StatusCode)
	}
}

// Every error answer of the alertmanager actions names a reason that belongs to its status.
func TestAlertmanagerErrorPathsHaveCodesThatMatchTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    req
		fail   int
		off    bool
		status int
		code   string
	}{
		{"invalid body", req{method: "POST", path: amCreatePath, body: "{"}, 0, false, 400, "invalid_input"},
		{"failed static check", req{method: "POST", path: amCreatePath, body: amSilenceBody(func(r *model.SilenceRequest) { r.Comment = "" })}, 0, false, 400, "invalid_input"},
		{"duration the instance does not offer", req{method: "POST", path: amCreatePath, body: amSilenceBody(func(r *model.SilenceRequest) { r.Duration = "9h" })}, 0, false, 400, "invalid_input"},
		{"protected alert", req{method: "POST", path: amCreatePath, body: amSilenceBody(func(r *model.SilenceRequest) { r.Matchers[0].Value = "Watchdog" })}, 0, false, 403, "forbidden"},
		{"foreign silence", req{method: "POST", path: amExpirePath, body: map[string]string{"id": amForeignSilence}}, 0, false, 403, "forbidden"},
		{"unknown silence", req{method: "POST", path: amExpirePath, body: map[string]string{"id": "00000000-1111-2222-3333-444444444444"}}, 0, false, 404, "not_found"},
		{"actions disabled", req{method: "POST", path: amCreatePath, body: amSilenceBody(nil)}, 0, true, 404, "not_found"},
		{"create upstream failure", req{method: "POST", path: amCreatePath, body: amSilenceBody(nil)}, 500, false, 502, "upstream_unavailable"},
		{"expire upstream failure", req{method: "POST", path: amExpirePath, body: map[string]string{"id": amDashSilence}}, 503, false, 502, "upstream_unavailable"},
		{"csrf", req{method: "POST", path: amCreatePath, body: amSilenceBody(nil), header: map[string]string{"X-Dash-Csrf": ""}}, 0, false, 403, "csrf_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mods []func(*pluginconf.Alertmanager)
			if tc.off {
				mods = append(mods, func(c *pluginconf.Alertmanager) { c.Actions.Enabled = false })
			}
			e := newAMEnv(t, mods...)
			e.am.setFail(tc.fail)
			w := tc.req.do(e.Handler())
			if w.Code != tc.status {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body, tc.status)
			}
			got := apiError(t, w)
			if got.Error == "" || got.Code != tc.code {
				t.Errorf("error = %+v, want code %q and a message", got, tc.code)
			}
			if !slices.Contains(statusCodes[w.Code], got.Code) {
				t.Errorf("code %q is not one of %v for status %d", got.Code, statusCodes[w.Code], w.Code)
			}
		})
	}
}
