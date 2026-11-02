package alertmanager

import (
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// The manifest keeps what the legacy shim declared: the nav entry, the version and description,
// and the topic and action names the SPA and the audit log depend on.
func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "alertmanager" || m.Name != "Alertmanager" || m.Version != "1.0.0" || m.Description != "Alerts and silences." {
		t.Errorf("identity = %q %q %q %q", m.ID, m.Name, m.Version, m.Description)
	}
	if !slices.Equal(m.Uses, []plugin.ID{"grafana"}) || len(m.Requires) != 0 {
		t.Errorf("uses/requires = %v / %v", m.Uses, m.Requires)
	}
	nav := model.NavItem{Path: "/alerts", Label: "Alerts", Title: "Alerts and silences", Icon: "BellRing"}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != nav {
		t.Errorf("nav = %+v, want %+v", m.UI.Nav, nav)
	}
	if len(m.Topics) != 1 || m.Topics[0].Name != "alerts" {
		t.Errorf("topics = %+v: want one topic named alerts (published as alertmanager.alerts)", m.Topics)
	}
	if _, ok := m.Topics[0].Type.(model.AlertsData); !ok {
		t.Errorf("topic type = %T", m.Topics[0].Type)
	}
	if _, ok := m.Config().(*pluginconf.Alertmanager); !ok {
		t.Errorf("config = %T", m.Config())
	}

	want := map[string]struct{ idempotent bool }{"silence.create": {false}, "silence.expire": {true}}
	if len(m.Actions) != len(want) {
		t.Fatalf("actions = %+v", m.Actions)
	}
	for _, a := range m.Actions {
		w, ok := want[a.Name]
		if !ok || a.MinRole != "admin" || a.Destructive || a.Idempotent != w.idempotent || a.Confirm != plugin.ConfirmNone ||
			a.Input == nil || a.Target == nil || a.Summary == nil || a.Title == "" {
			t.Errorf("action %+v", a)
		}
	}
}

func TestConfiguredAndValidate(t *testing.T) {
	p := &Plugin{}
	cfg := pluginconf.DefaultAlertmanager()
	if p.Configured(&cfg) {
		t.Error("no URL: configured")
	}
	cfg.URL = "http://alertmanager.example.com"
	if !p.Configured(&cfg) || p.Validate(&cfg) != nil {
		t.Errorf("a URL: configured=%v validate=%v", p.Configured(&cfg), p.Validate(&cfg))
	}
	cfg.SilenceDurations = []string{"2h"}
	if err := p.Validate(&cfg); err == nil || !strings.Contains(err.Error(), "silenceDurations") {
		t.Errorf("a duration the action does not know: %v", err)
	}
	cfg = pluginconf.DefaultAlertmanager()
	cfg.Auth.Basic.Username = "u"
	if p.Validate(&cfg) == nil {
		t.Error("a user name without a password passed")
	}
	if p.Validate("nope") == nil || p.Configured("nope") {
		t.Error("another config type was accepted")
	}
}

func TestStartRefusesAnotherConfigType(t *testing.T) {
	if err := (&Plugin{}).Start(t.Context(), nil, "nope"); err == nil {
		t.Error("Start accepted a string as its configuration")
	}
}

// A poll publishes the lists with the fields the plugin owns: refs, protected, dash and the
// durations the dialog offers; alerts newest first, silences by expiry; the ignore list is the
// only filter.
func TestTopicCarriesRefsFlagsAndDurations(t *testing.T) {
	am := newFakeAM(t)
	am.set(
		[]string{amAlertFiringCrit, amAlertFiringWarn, amAlertSilenced, amAlertWatchdog},
		[]string{amSilenceDash, amSilenceForeign},
	)
	e := newEnv(t, envOpts{am: am, cfg: func(c *pluginconf.Alertmanager) {
		c.IgnoreAlerts = []string{"InfoInhibitor"} // Watchdog is no longer dropped
		c.ProtectedAlerts = []string{"Watchdog", "DiskFull"}
		c.SilenceDurations = []string{"1h", "24h"}
	}})
	d, sources := e.topic(t, "the first poll", func(d model.AlertsData) bool { return len(d.Alerts) == 4 })

	var names []string
	for _, a := range d.Alerts {
		names = append(names, a.Name)
	}
	// Newest first by startsAt: DiskFull (04:00), NodeDown (03:00), Muted (02:00), Watchdog (Oct 1).
	if !slices.Equal(names, []string{"DiskFull", "NodeDown", "Muted", "Watchdog"}) {
		t.Errorf("alert order = %v", names)
	}
	protected := map[string]bool{}
	for _, a := range d.Alerts {
		protected[a.Name] = a.Protected
		if want := "alertmanager:Alert/" + a.Fingerprint; a.Ref != want {
			t.Errorf("ref of %s = %q, want %q", a.Name, a.Ref, want)
		}
	}
	if !protected["Watchdog"] || !protected["DiskFull"] || protected["NodeDown"] || protected["Muted"] {
		t.Errorf("protected = %v", protected)
	}
	// Silences by endsAt ascending: the foreign one ends first.
	if len(d.Silences) != 2 || d.Silences[0].ID != "7c7c7c7c-bbbb" || d.Silences[1].ID != "5b1c2d3e-aaaa" {
		t.Fatalf("silences = %+v", d.Silences)
	}
	if d.Silences[0].Dash || !d.Silences[1].Dash {
		t.Errorf("dash flags = %v %v: only dash:alice's silence is dash's", d.Silences[0].Dash, d.Silences[1].Dash)
	}
	for _, s := range d.Silences {
		if want := "alertmanager:Silence/" + s.ID; s.Ref != want {
			t.Errorf("silence ref = %q, want %q", s.Ref, want)
		}
	}
	if !slices.Equal(d.SilenceDurations, []string{"1h", "24h"}) {
		t.Errorf("durations = %v", d.SilenceDurations)
	}
	if len(sources) != 1 || sources[0].Name != "alertmanager" || !sources[0].OK {
		t.Errorf("sources = %+v", sources)
	}

	plugintest.AssertRefs(t, e.sup, ID, d, model.Alert{}, model.Silence{})
}

func TestIgnoreAlertsDropsOnlyTheNamedAlerts(t *testing.T) {
	am := newFakeAM(t)
	am.set([]string{amAlertFiringCrit, amAlertFiringWarn, amAlertWatchdog, amAlertInfo}, nil)

	e := newEnv(t, envOpts{am: am})
	d, _ := e.topic(t, "the first poll", anyData)
	if len(d.Alerts) != 3 {
		t.Errorf("default ignore list keeps %d alerts, want 3 (everything but Watchdog): %+v", len(d.Alerts), d.Alerts)
	}
	for _, a := range d.Alerts {
		if a.Name == "Watchdog" {
			t.Error("Watchdog reached the topic although ignoreAlerts lists it")
		}
	}

	e2 := newEnv(t, envOpts{am: am, cfg: func(c *pluginconf.Alertmanager) { c.IgnoreAlerts = []string{"DiskFull", "Notice"} }})
	d2, _ := e2.topic(t, "the first poll", anyData)
	var names []string
	for _, a := range d2.Alerts {
		names = append(names, a.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"NodeDown", "Watchdog"}) {
		t.Errorf("custom ignore list keeps %v", names)
	}

	// An empty topic is [] and not null.
	am.set(nil, nil)
	e3 := newEnv(t, envOpts{am: am})
	if d3, _ := e3.topic(t, "an empty poll", anyData); d3.Alerts == nil || d3.Silences == nil {
		t.Errorf("empty payload has nil lists: %+v", d3)
	}
}

// Attention and the KPI follow severityMap; a silenced, inhibited or unmapped alert is not
// attention, and the KPI counts every firing alert.
func TestAttentionAndKPIFollowTheSeverityMap(t *testing.T) {
	am := newFakeAM(t)
	am.set([]string{amAlertFiringCrit, amAlertFiringWarn, amAlertSilenced, amAlertInfo}, []string{amSilenceDash})

	e := newEnv(t, envOpts{am: am, cfg: func(c *pluginconf.Alertmanager) {
		// A custom map: the warning label is critical here, critical is not mapped at all.
		c.SeverityMap = map[string]string{"warning": "crit", "info": "warn"}
	}})
	e.topic(t, "the first poll", func(d model.AlertsData) bool { return len(d.Alerts) == 4 })

	cs := e.sup.Contributors()
	if len(cs) != 1 || cs[0].ID != ID || cs[0].Attention == nil || cs[0].KPIs == nil || cs[0].Topology != nil {
		t.Fatalf("contributors = %+v", cs)
	}
	items := cs[0].Attention.Attention(time.Now())
	got := map[string]model.Severity{}
	for _, it := range items {
		got[it.ID] = it.Severity
		if it.Plugin != "alertmanager" || it.Source != "alertmanager" || it.Route != "/alerts" || it.Links == nil {
			t.Errorf("item %+v", it)
		}
	}
	// fp-warn is DiskFull (warning -> crit), fp-info is Notice (info -> warn); fp-crit is
	// unmapped; fp-sil is silenced.
	want := map[string]model.Severity{"alert:fp-warn": model.SeverityCrit, "alert:fp-info": model.SeverityWarn}
	if len(got) != len(want) || got["alert:fp-warn"] != want["alert:fp-warn"] || got["alert:fp-info"] != want["alert:fp-info"] {
		t.Errorf("attention = %v, want %v", got, want)
	}
	for _, it := range items {
		if it.ID == "alert:fp-warn" {
			if it.Title != "DiskFull" || it.Detail != "disk b is full" || it.Ref != "alertmanager:Alert/fp-warn" || it.Since.IsZero() {
				t.Errorf("DiskFull item = %+v (the detail falls back to the description)", it)
			}
		}
		if it.ID == "alert:fp-crit" {
			t.Error("an alert whose severity is not in the map raised attention")
		}
	}

	k := cs[0].KPIs.KPIs()
	// Three alerts fire (crit, warn, info); the silenced one does not. A firing alert maps to crit.
	if len(k) != 1 || k[0].Key != "firing" || k[0].Plugin != "alertmanager" || k[0].Value != 3 || k[0].Health != model.HealthCrit {
		t.Errorf("kpis = %+v", k)
	}
}

func TestKPIHealth(t *testing.T) {
	sev := map[string]string{"critical": "crit", "warning": "warn"}
	alert := func(severity, state string, silenced ...string) model.Alert {
		return model.Alert{Fingerprint: "f", Name: "A", Severity: severity, State: state, SilencedBy: silenced, InhibitedBy: []string{}}
	}
	for _, tc := range []struct {
		name   string
		alerts []model.Alert
		value  float64
		health model.Health
	}{
		{"nothing fires", nil, 0, model.HealthOK},
		{"only a silenced alert", []model.Alert{alert("critical", "suppressed", "s")}, 0, model.HealthOK},
		{"a warning", []model.Alert{alert("warning", "active")}, 1, model.HealthWarn},
		{"an alert with no mapped severity still counts and warns", []model.Alert{alert("none", "active")}, 1, model.HealthWarn},
		{"a critical", []model.Alert{alert("warning", "active"), alert("critical", "active")}, 2, model.HealthCrit},
	} {
		got := kpisOf(tc.alerts, sev)
		if len(got) != 1 || got[0].Value != tc.value || got[0].Health != tc.health {
			t.Errorf("%s: %+v, want value %v health %s", tc.name, got, tc.value, tc.health)
		}
	}
}

func TestFiringRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    model.Alert
		want bool
	}{
		{"active", model.Alert{State: "active"}, true},
		{"suppressed", model.Alert{State: "suppressed"}, false},
		{"unprocessed", model.Alert{State: "unprocessed"}, false},
		{"silenced but listed active", model.Alert{State: "active", SilencedBy: []string{"s"}}, false},
		{"inhibited but listed active", model.Alert{State: "active", InhibitedBy: []string{"i"}}, false},
	} {
		if got := firing(tc.a); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The plugin is starting until the first poll has ended, healthy after it, in error with a
// category (never a URL) when a poll fails, and healthy again when the next succeeds.
func TestStatusFollowsThePolls(t *testing.T) {
	am := newFakeAM(t)
	am.gate = make(chan struct{})
	e := newEnv(t, envOpts{am: am})

	e.eventually(t, "the instance to run", func() bool { return e.sup.Running(ID) })
	if pi := e.info(t); pi.State != plugin.StateStarting {
		t.Errorf("before the first poll: %s %q", pi.State, pi.Message)
	}
	close(am.gate)
	e.eventually(t, "healthy", func() bool { return e.info(t).State == plugin.StateHealthy })

	am.setStatus(http.StatusInternalServerError)
	e.eventually(t, "error", func() bool { return e.info(t).State == plugin.StateError })
	pi := e.info(t)
	if pi.Message != "http_500" || strings.Contains(pi.Message, "127.0.0.1") {
		t.Errorf("message = %q, want the category http_500", pi.Message)
	}
	// The topic still carries the last good lists, with the failing source.
	_, sources := e.topic(t, "the failing source", func(model.AlertsData) bool {
		ev, _ := e.hub.Latest("", "alertmanager.alerts")
		return strings.Contains(string(ev.Data), `"ok":false`)
	})
	if len(sources) != 1 || sources[0].OK || sources[0].Error != "http_500" {
		t.Errorf("sources = %+v", sources)
	}
	// Its status reaches the overview through the contributor.
	if cs := e.sup.Contributors(); len(cs) != 1 || cs[0].Sources == nil {
		t.Fatalf("contributors = %+v", cs)
	} else if src := cs[0].Sources(); len(src) != 1 || src[0].Name != "alertmanager" || src[0].OK || src[0].Error != "http_500" {
		t.Errorf("contributor sources = %+v", src)
	}
	if pi.State == plugin.StateDegraded {
		t.Error("one source can never degrade the plugin")
	}

	am.setStatus(0)
	e.eventually(t, "healthy again", func() bool { return e.info(t).State == plugin.StateHealthy })
}

func TestUnauthorizedIsAnAuthCategory(t *testing.T) {
	am := newFakeAM(t)
	am.setStatus(http.StatusUnauthorized)
	e := newEnv(t, envOpts{am: am})
	e.eventually(t, "error", func() bool { return e.info(t).State == plugin.StateError })
	if m := e.info(t).Message; m != "auth" {
		t.Errorf("message = %q", m)
	}
}

// Credentials ride on every request, and a secret rotated in place applies at the next one.
func TestCredentialsAreSentAndRotate(t *testing.T) {
	token := secret.New("one", "")
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Alertmanager) { c.Auth.BearerToken = token }})
	e.eventually(t, "a request with the first token", func() bool { return e.am.sawAuth("Bearer one") })
	token.Update("two")
	e.eventually(t, "a request with the rotated token", func() bool { return e.am.sawAuth("Bearer two") })

	basic := newEnv(t, envOpts{cfg: func(c *pluginconf.Alertmanager) {
		c.Auth.Basic = pluginconf.BasicAuth{Username: "dash", Password: secret.New("pw", "")}
	}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("dash:pw"))
	basic.eventually(t, "a basic-auth request", func() bool { return basic.am.sawAuth(want) })
}

// A bearer token that arrives after the authorizer was built (a k8s: reference read later) is
// sent from the next request on: the authorizer decides per request.
func TestAuthorizerSendsATokenThatArrivesLater(t *testing.T) {
	token, err := secret.NewK8sRefs(nil).Handle("k8s:media/am-credentials#token")
	if err != nil {
		t.Fatal(err)
	}
	authz := authorizer(pluginconf.Auth{BearerToken: token})
	header := func() string {
		r, _ := http.NewRequest(http.MethodGet, "http://am.example.com/", nil)
		authz(r)
		return r.Header.Get("Authorization")
	}
	if got := header(); got != "" {
		t.Fatalf("a pending token sent %q", got)
	}
	token.Update("filled")
	if got := header(); got != "Bearer filled" {
		t.Errorf("after the fill: %q", got)
	}
}

// A plugin whose k8s: token is pending does not start, and so sends nothing without it; the fill
// starts it with the header.
func TestPendingTokenWaitsThenIsSent(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	const ref = "k8s:media/am-credentials#token"
	token, err := refs.Handle(ref)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Alertmanager) { c.Auth.BearerToken = token }})
	refs.OnFill(e.sup.Reload)
	if e.sup.Running(ID) {
		t.Fatal("the plugin started with its token pending")
	}
	if st := e.info(t).State; st != model.PluginUnconfigured && st != model.PluginDisabled {
		t.Errorf("state = %s", st)
	}
	refs.Fill(ref, "from-the-secret")
	e.eventually(t, "a request with the filled token", func() bool { return e.am.sawAuth("Bearer from-the-secret") })
	if e.am.sawAuth("") {
		t.Error("a request went out without the token")
	}
}

// Visibility rule (a): topics reach a stream opened while the row was hidden.
func TestTopicsReachAnOpenStreamWhenTheRowTurnsVisible(t *testing.T) {
	am := newFakeAM(t)
	am.set([]string{amAlertFiringWarn}, []string{amSilenceDash})
	before := pluginconf.DefaultAlertmanager() // no URL: the row is disabled
	after := pluginconf.DefaultAlertmanager()
	after.URL = am.srv.URL
	plugintest.AssertTopicsReachOpenStream(t, New,
		plugin.InstanceConfig{Config: &before},
		plugin.InstanceConfig{Config: &after})
}

func TestDisabledWithoutURLAndUnconfiguredWhenEnabled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled *bool
		want    plugin.State
	}{
		{"auto", nil, plugin.StateDisabled},
		{"enabled", new(true), plugin.StateUnconfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, envOpts{
				cfg: func(c *pluginconf.Alertmanager) { c.URL = "" },
				ic:  func(ic *plugin.InstanceConfig) { ic.Enabled = tc.enabled },
			})
			e.eventually(t, "the state", func() bool { return e.info(t).State == tc.want })
			if e.sup.Running(ID) {
				t.Error("the plugin runs without a URL")
			}
		})
	}
}

// Reconfiguring actions.enabled restarts the instance and mounts or unmounts the actions.
func TestActionsFollowTheirToggle(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Alertmanager) { c.Actions.Enabled = false }})
	e.eventually(t, "the instance to run", func() bool { return e.sup.Running(ID) })
	if _, ok := e.sup.Action(ID, "silence.create"); ok {
		t.Error("silence.create is mounted with actions.enabled false")
	}
	if n := len(e.info(t).Actions); n != 0 {
		t.Errorf("the row lists %d actions", n)
	}

	on := *e.cfg
	on.Actions.Enabled = true
	e.set.Set(plugin.InstanceConfig{Name: ID, Type: ID, Config: &on, ActionsEnabled: true})
	e.sup.Reload()
	e.eventually(t, "the actions to mount", func() bool { _, ok := e.sup.Action(ID, "silence.expire"); return ok })
	var names []string
	for _, a := range e.info(t).Actions {
		names = append(names, a.Name)
		if a.MinRole != "admin" {
			t.Errorf("%s minRole = %q", a.Name, a.MinRole)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"silence.create", "silence.expire"}) {
		t.Errorf("row actions = %v", names)
	}
}
