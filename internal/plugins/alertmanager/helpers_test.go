package alertmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// fakeAM is an Alertmanager: it answers the endpoints the plugin uses and records what was
// created and expired.
type fakeAM struct {
	srv *httptest.Server

	mu        sync.Mutex
	alerts    string // JSON array
	silences  string // JSON array
	status    int    // every request answers with this status when non-zero
	gate      chan struct{}
	created   []map[string]any
	expired   []string
	createdID string
	authz     []string // the Authorization header of every request
}

const (
	amAlertFiringCrit = `{"fingerprint":"fp-crit","labels":{"alertname":"NodeDown","severity":"critical","instance":"a"},"annotations":{"summary":"node a is down"},"startsAt":"2026-10-30T03:00:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}`
	amAlertFiringWarn = `{"fingerprint":"fp-warn","labels":{"alertname":"DiskFull","severity":"warning"},"annotations":{"description":"disk b is full"},"startsAt":"2026-10-30T04:00:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}`
	amAlertSilenced   = `{"fingerprint":"fp-sil","labels":{"alertname":"Muted","severity":"critical"},"annotations":{},"startsAt":"2026-10-30T02:00:00Z","status":{"state":"suppressed","silencedBy":["5b1c2d3e-aaaa"],"inhibitedBy":[]}}`
	amAlertInfo       = `{"fingerprint":"fp-info","labels":{"alertname":"Notice","severity":"info"},"annotations":{},"startsAt":"2026-10-30T05:00:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}`
	amAlertWatchdog   = `{"fingerprint":"fp-wd","labels":{"alertname":"Watchdog","severity":"none"},"annotations":{},"startsAt":"2026-10-01T00:00:00Z","status":{"state":"active","silencedBy":[],"inhibitedBy":[]}}`

	amSilenceDash    = `{"id":"5b1c2d3e-aaaa","status":{"state":"active"},"matchers":[{"name":"alertname","value":"Muted","isRegex":false,"isEqual":true}],"createdBy":"dash:alice","comment":"maintenance","startsAt":"2026-10-30T20:00:00Z","endsAt":"2026-10-30T22:00:00Z"}`
	amSilenceForeign = `{"id":"7c7c7c7c-bbbb","status":{"state":"active"},"matchers":[{"name":"alertname","value":"Other","isRegex":false,"isEqual":true}],"createdBy":"carol@example.com","comment":"from the UI","startsAt":"2026-10-30T19:00:00Z","endsAt":"2026-10-30T21:00:00Z"}`
)

func newFakeAM(t *testing.T) *fakeAM {
	t.Helper()
	f := &fakeAM{alerts: "[]", silences: "[]", createdID: "abcdef12-3456"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.authz = append(f.authz, r.Header.Get("Authorization"))
	gate, status := f.gate, f.status
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		http.Error(w, "boom", status)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/alerts":
		io.WriteString(w, f.alerts)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/silences":
		io.WriteString(w, f.silences)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/silences":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		fmt.Fprintf(w, `{"silenceID":%q}`, f.createdID)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/silence/"):
		f.expired = append(f.expired, strings.TrimPrefix(r.URL.Path, "/api/v2/silence/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAM) set(alerts, silences []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = "[" + strings.Join(alerts, ",") + "]"
	f.silences = "[" + strings.Join(silences, ",") + "]"
}

func (f *fakeAM) setStatus(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = code
}

// sawAuth reports whether a request carried exactly this Authorization header.
func (f *fakeAM) sawAuth(v string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.authz, v)
}

func (f *fakeAM) snapshot() (created []map[string]any, expired []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created), slices.Clone(f.expired)
}

var alice = auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}

// env is the plugin on a real supervisor and hub, against a fake Alertmanager.
type env struct {
	am  *fakeAM
	hub *hub.Hub
	sup *plugin.Supervisor
	set *plugintest.Settings
	cfg *pluginconf.Alertmanager
}

type envOpts struct {
	cfg  func(*pluginconf.Alertmanager) // adjusts the default configuration
	ic   func(*plugin.InstanceConfig)   // adjusts the instance settings
	demo bool                           // run the twin
	am   *fakeAM                        // use this upstream
}

// newEnv starts the plugin (or its twin) with the default configuration pointed at the fake and
// actions enabled. It polls every 20ms.
func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	am := o.am
	if am == nil {
		am = newFakeAM(t)
	}
	cfg := pluginconf.DefaultAlertmanager()
	cfg.URL = am.srv.URL
	cfg.Actions.Enabled = true
	if o.cfg != nil {
		o.cfg(&cfg)
	}
	ic := plugin.InstanceConfig{Name: ID, Type: ID, Config: &cfg, ActionsEnabled: cfg.Actions.Enabled}
	if o.ic != nil {
		o.ic(&ic)
	}
	h := hub.New()
	set := &plugintest.Settings{}
	set.Set(ic)
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.DiscardHandler), Demo: o.demo,
	})
	if err := sup.Add(func() plugin.Plugin { return newPlugin(20 * time.Millisecond) }); err != nil {
		t.Fatal(err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return &env{am: am, hub: h, sup: sup, set: set, cfg: &cfg}
}

// topic waits for a published alerts payload that satisfies ok, and returns it with its sources.
func (e *env) topic(t *testing.T, what string, ok func(model.AlertsData) bool) (model.AlertsData, []model.SourceStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last model.AlertsData
	for time.Now().Before(deadline) {
		if ev, found := e.hub.Latest("", "alertmanager.alerts"); found {
			var env model.Envelope
			if err := json.Unmarshal(ev.Data, &env); err != nil {
				t.Fatal(err)
			}
			var d model.AlertsData
			if err := json.Unmarshal(env.Data, &d); err != nil {
				t.Fatal(err)
			}
			last = d
			if ok(d) {
				return d, env.Sources
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last payload %+v", what, last)
	return model.AlertsData{}, nil
}

// anyData accepts the first payload.
func anyData(model.AlertsData) bool { return true }

func (e *env) eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) info(t *testing.T) model.PluginInfo {
	t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == string(ID) {
			return pi
		}
	}
	t.Fatal("the plugin has no row")
	return model.PluginInfo{}
}

// act runs an action as alice, the way the API does after it has decoded and checked the input.
func (e *env) act(t *testing.T, action string, in any) (any, error) {
	t.Helper()
	ref, ok := e.sup.Action(ID, action)
	if !ok {
		t.Fatalf("action %s is not mounted", action)
	}
	if v, ok := in.(interface{ Validate() error }); ok {
		if err := v.Validate(); err != nil {
			t.Fatalf("the input fails its own Validate: %v", err)
		}
	}
	return ref.Run(context.Background(), alice, in)
}

func createIn(mod func(*silenceCreateInput)) *silenceCreateInput {
	in := &silenceCreateInput{model.SilenceRequest{
		Matchers: []model.SilenceMatcher{{Name: "alertname", Value: "HighTemp"}, {Name: "instance", Value: "pve-730"}},
		Duration: "4h",
		Comment:  "maintenance window",
	}}
	if mod != nil {
		mod(in)
	}
	return in
}

// actionError is the *plugin.ActionError inside err, or fails.
func actionError(t *testing.T, err error) *plugin.ActionError {
	t.Helper()
	ae, ok := err.(*plugin.ActionError)
	if !ok {
		t.Fatalf("err = %v (%T), want a *plugin.ActionError", err, err)
	}
	return ae
}
