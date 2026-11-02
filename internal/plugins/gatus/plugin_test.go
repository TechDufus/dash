package gatus

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// ---- a fake Gatus ----

// fakeGatus answers /api/v1/endpoints/statuses with whatever it was told to.
type fakeGatus struct {
	*httptest.Server
	mu     sync.Mutex
	body   string
	status int // 0 is 200
	query  string
	auth   string
	block  chan struct{} // when set, a request waits for it (or for the client to give up)
	hits   atomic.Int32
}

func newFakeGatus(t *testing.T) *fakeGatus {
	t.Helper()
	f := &fakeGatus{body: "[]"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGatus) serve(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)
	f.mu.Lock()
	f.query, f.auth = r.URL.RawQuery, r.Header.Get("Authorization")
	body, status, block := f.body, f.status, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		http.Error(w, body, status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func (f *fakeGatus) answer(body string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.status = body, status
}

func (f *fakeGatus) lastQuery() (query, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.query, f.auth
}

// check is one Gatus endpoint as the API reports it: oks are its results, oldest first, one minute
// apart, ending at end.
type check struct {
	group, name, host string
	oks               []bool
}

func gatusJSON(end time.Time, checks ...check) string {
	type result struct {
		Hostname  string    `json:"hostname"`
		Duration  int64     `json:"duration"`
		Success   bool      `json:"success"`
		Timestamp time.Time `json:"timestamp"`
	}
	type status struct {
		Name    string   `json:"name"`
		Group   string   `json:"group"`
		Key     string   `json:"key"`
		Results []result `json:"results"`
	}
	out := []status{}
	for _, c := range checks {
		s := status{Name: c.name, Group: c.group, Key: strings.ToLower(c.group + "_" + c.name), Results: []result{}}
		for i, ok := range c.oks {
			s.Results = append(s.Results, result{Hostname: c.host, Duration: 12e6, Success: ok,
				Timestamp: end.Add(-time.Duration(len(c.oks)-1-i) * time.Minute)})
		}
		out = append(out, s)
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// ---- a supervisor around the plugin ----

// factory is New with a short poll interval.
func factory() plugin.Plugin { return &Plugin{interval: 20 * time.Millisecond} }

func config(url string) pluginconf.Gatus {
	c := pluginconf.DefaultGatus()
	c.URL = url
	return c
}

type rig struct {
	t   *testing.T
	sup *plugin.Supervisor
	set *plugintest.Settings
	hub *plugintest.Hub
	log *syncBuffer
}

// syncBuffer is a bytes.Buffer the logger and the test can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func instance(cfg pluginconf.Gatus) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: pluginID, Type: pluginID, Config: &cfg}
}

// newRig runs the plugin under a real Supervisor, publishing to a recording hub.
func newRig(t *testing.T, cfg pluginconf.Gatus, scrub func(string) string) *rig {
	t.Helper()
	r := &rig{t: t, hub: &plugintest.Hub{}, set: &plugintest.Settings{}, log: &syncBuffer{}}
	r.set.Set(instance(cfg))
	r.sup = plugin.NewSupervisor(plugin.Options{
		Publisher: r.hub, Settings: r.set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		ScrubLog: scrub, Log: slog.New(slog.NewTextHandler(r.log, nil)),
	})
	if err := r.sup.Add(factory); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); r.sup.Shutdown(context.Background()) })
	return r
}

func (r *rig) reconfigure(cfg pluginconf.Gatus) {
	r.set.Set(instance(cfg))
	r.sup.Reload()
}

func (r *rig) row() model.PluginInfo {
	r.t.Helper()
	for _, pi := range r.sup.Infos() {
		if pi.ID == string(pluginID) {
			return pi
		}
	}
	r.t.Fatal("gatus is not registered")
	return model.PluginInfo{}
}

// endpoints is the service the instance exports, nil when it is not running.
func (r *rig) endpoints() svc.EndpointHealth {
	s, ok := r.sup.Service(pluginID)
	if !ok {
		return nil
	}
	return s.(svc.EndpointHealth)
}

func (r *rig) status() (model.StatusData, []model.SourceStatus, bool) {
	ev, ok := r.hub.Last("gatus.status")
	if !ok {
		return model.StatusData{}, nil, false
	}
	return ev.Data.(model.StatusData), ev.Sources, true
}

func (r *rig) attention() []model.AttentionItem {
	for _, c := range r.sup.Contributors() {
		if c.ID == pluginID && c.Attention != nil {
			return c.Attention.Attention(time.Now())
		}
	}
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *rig) waitState(state model.PluginState) {
	r.t.Helper()
	eventually(r.t, "the row to be "+string(state), func() bool { return r.row().State == state })
}

// ---- tests ----

var now = time.Now().UTC().Truncate(time.Second)

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "gatus" || m.Name != "Gatus" || len(m.Requires) != 0 || len(m.Uses) != 0 || len(m.Actions) != 0 {
		t.Errorf("manifest: %+v", m)
	}
	if len(m.Topics) != 1 || m.Topics[0].Name != "status" {
		t.Fatalf("topics: %+v", m.Topics)
	}
	if _, ok := m.Topics[0].Type.(model.StatusData); !ok {
		t.Errorf("topic type %T", m.Topics[0].Type)
	}
	want := model.NavItem{Path: "/status", Label: "Status", Title: "Service status", Icon: "Activity"}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != want {
		t.Errorf("nav: %+v", m.UI.Nav)
	}
	if _, ok := m.Config().(*pluginconf.Gatus); !ok {
		t.Errorf("config %T", m.Config())
	}
}

func TestUnsetURLDisablesAndEnabledWithoutURLIsUnconfigured(t *testing.T) {
	r := newRig(t, pluginconf.DefaultGatus(), nil)
	if got := r.row().State; got != model.PluginDisabled {
		t.Errorf("no URL: %s", got)
	}
	if r.endpoints() != nil {
		t.Error("a disabled plugin exports a service")
	}
	on := true
	ic := instance(pluginconf.DefaultGatus())
	ic.Enabled = &on
	r.set.Set(ic)
	r.sup.Reload()
	r.waitState(model.PluginUnconfigured)
}

func TestInvalidAuthIsUnconfigured(t *testing.T) {
	f := newFakeGatus(t)
	c := config(f.URL)
	c.Auth.Basic.Username = "dash" // without a password
	r := newRig(t, c, nil)
	r.waitState(model.PluginUnconfigured)
	if f.hits.Load() != 0 {
		t.Error("an invalid configuration still polled")
	}
	c.Auth.Basic.Password = secret.New("pw", "")
	r.reconfigure(c) // valid now: the plugin starts
	r.waitState(model.PluginHealthy)
	c.PageSize = 0
	r.reconfigure(c)
	r.waitState(model.PluginUnconfigured) // a page size of 0
	if r.endpoints() != nil {
		t.Error("an unconfigured instance exports a service")
	}
}

func TestPublishesTheStatusTopicAndGoesHealthy(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now,
		check{"platform", "Grafana", "grafana.example.com", []bool{true, true}},
		check{"apps", "Plex", "plex.example.com", []bool{true, false}},
		check{"apps", "Alpha", "alpha.example.com", nil},
	), 0)
	c := config(f.URL)
	c.CriticalGroups = []string{"platform"}
	r := newRig(t, c, nil)
	r.waitState(model.PluginHealthy)

	data, srcs, ok := r.status()
	if !ok {
		t.Fatal("nothing published")
	}
	var order []string
	for _, e := range data.Endpoints {
		order = append(order, e.Key)
		if e.Results == nil {
			t.Errorf("%s: nil Results", e.Key)
		}
	}
	if want := "apps_alpha apps_plex platform_grafana"; strings.Join(order, " ") != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	if len(data.CriticalGroups) != 1 || data.CriticalGroups[0] != "platform" {
		t.Errorf("criticalGroups = %v", data.CriticalGroups)
	}
	if len(srcs) != 1 || srcs[0].Name != "gatus" || !srcs[0].OK || srcs[0].Error != "" {
		t.Errorf("sources = %+v", srcs)
	}
	if row := r.row(); row.Message != "" || len(row.Topics) != 1 || row.Topics[0] != "gatus.status" {
		t.Errorf("row = %+v", row)
	}
	if len(data.Endpoints) > 0 && data.Endpoints[0].Healthy {
		t.Errorf("an endpoint with no results is healthy: %+v", data.Endpoints[0])
	}
	if got := data.Endpoints[1]; got.Key != "apps_plex" || got.Healthy || got.URL != "plex.example.com" {
		t.Errorf("plex: %+v", got)
	}
}

func TestPageSizeReachesTheQuery(t *testing.T) {
	f := newFakeGatus(t)
	c := config(f.URL)
	c.PageSize = 7
	newRig(t, c, nil).waitState(model.PluginHealthy)
	if q, _ := f.lastQuery(); q != "page=1&pageSize=7" {
		t.Errorf("query = %q", q)
	}
}

func TestStatusMapping(t *testing.T) {
	f := newFakeGatus(t)
	f.block = make(chan struct{})
	r := newRig(t, config(f.URL), nil)
	r.waitState(model.PluginStarting)
	if r.row().Message == "" {
		t.Error("starting without a message")
	}
	close(f.block)
	r.waitState(model.PluginHealthy)

	f.answer("login required", http.StatusUnauthorized)
	r.waitState(model.PluginError)
	if got := r.row().Message; got != "auth" {
		t.Errorf("message = %q, want the category", got)
	}
	f.answer("down", http.StatusBadGateway)
	eventually(t, "the category to follow", func() bool { return r.row().Message == "http_502" })
	f.answer("[]", 0)
	r.waitState(model.PluginHealthy)
	if got := r.row().Message; got != "" {
		t.Errorf("message after recovery = %q", got)
	}
}

func TestACanaryNeverReachesViewers(t *testing.T) {
	const password = "hunter2&\"pw"
	f := newFakeGatus(t)
	f.answer("login at "+f.URL+"/login as dash with "+password, http.StatusUnauthorized)
	c := config(f.URL)
	c.Auth.Basic = pluginconf.BasicAuth{Username: "dash", Password: secret.New(password, "")}
	reg := secret.NewRegistry()
	reg.Add(c.Auth.Basic.Password)
	r := newRig(t, c, reg.ScrubLogLine)
	r.waitState(model.PluginError)
	eventually(t, "the failure to be logged", func() bool { return strings.Contains(r.log.String(), "upstream source failed") })

	data, srcs, ok := r.status()
	if !ok {
		t.Fatal("nothing published on a failing source")
	}
	if len(data.Endpoints) != 0 {
		t.Errorf("endpoints = %+v", data.Endpoints)
	}
	if len(srcs) != 1 || srcs[0].OK || srcs[0].Error != "auth" {
		t.Errorf("sources = %+v", srcs)
	}
	wire, _ := json.Marshal(struct {
		Data model.StatusData
		Srcs []model.SourceStatus
		Info []model.PluginInfo
		Cons []model.SourceStatus
	}{data, srcs, r.sup.Infos(), r.sup.Contributors()[0].Sources()})
	host := strings.TrimPrefix(f.URL, "http://")
	for _, leak := range []string{host, f.URL, "login", "hunter2", "dash with"} {
		if strings.Contains(string(wire), leak) {
			t.Errorf("a viewer-visible payload contains %q: %s", leak, wire)
		}
	}
	log := r.log.String()
	if strings.Contains(log, "hunter2") || strings.Contains(log, password) {
		t.Errorf("the log has the password: %s", log)
	}
	if !strings.Contains(log, "category=auth") {
		t.Errorf("the log has no category: %s", log)
	}
	if n := strings.Count(log, "upstream source failed"); n != 1 {
		t.Errorf("logged %d times for one unchanged failure, want once", n)
	}
}

func TestCredentialsAreSentAndRotate(t *testing.T) {
	for name, tc := range map[string]struct {
		auth func(secret.Value) pluginconf.Auth
		want func(token string) string
	}{
		"bearer": {
			func(v secret.Value) pluginconf.Auth { return pluginconf.Auth{BearerToken: v} },
			func(tok string) string { return "Bearer " + tok },
		},
		"basic": {
			func(v secret.Value) pluginconf.Auth {
				return pluginconf.Auth{Basic: pluginconf.BasicAuth{Username: "dash", Password: v}}
			},
			func(tok string) string {
				r, _ := http.NewRequest("GET", "/", nil)
				r.SetBasicAuth("dash", tok)
				return r.Header.Get("Authorization")
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeGatus(t)
			v := secret.New("first", "")
			c := config(f.URL)
			c.Auth = tc.auth(v)
			newRig(t, c, nil).waitState(model.PluginHealthy)
			if _, got := f.lastQuery(); got != tc.want("first") {
				t.Errorf("Authorization = %q, want %q", got, tc.want("first"))
			}
			v.Update("second")
			eventually(t, "the rotated secret to be sent", func() bool { _, got := f.lastQuery(); return got == tc.want("second") })
		})
	}
	t.Run("none", func(t *testing.T) {
		f := newFakeGatus(t)
		newRig(t, config(f.URL), nil).waitState(model.PluginHealthy)
		if _, got := f.lastQuery(); got != "" {
			t.Errorf("Authorization = %q without credentials", got)
		}
	})
}

// A bearer token that arrives after the authorizer was built (a k8s: reference read later) is
// sent from the next request on: the authorizer decides per request.
func TestAuthorizerSendsATokenThatArrivesLater(t *testing.T) {
	token, err := secret.NewK8sRefs(nil).Handle("k8s:status/gatus-credentials#token")
	if err != nil {
		t.Fatal(err)
	}
	authz := authorize(pluginconf.Auth{BearerToken: token})
	header := func() string {
		r, _ := http.NewRequest(http.MethodGet, "http://gatus.example.com/", nil)
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
	const ref = "k8s:status/gatus-credentials#token"
	refs := secret.NewK8sRefs(nil)
	token, err := refs.Handle(ref)
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeGatus(t)
	c := config(f.URL)
	c.Auth = pluginconf.Auth{BearerToken: token}
	r := newRig(t, c, nil)
	refs.OnFill(r.sup.Reload)
	if st := r.row().State; st != model.PluginUnconfigured && st != model.PluginDisabled {
		t.Fatalf("state = %s while the token is pending", st)
	}
	if r.sup.Running(pluginID) || f.hits.Load() != 0 {
		t.Fatalf("the plugin ran with its token pending: %d requests", f.hits.Load())
	}
	refs.Fill(ref, "from-the-secret")
	eventually(t, "the filled token to be sent", func() bool { _, got := f.lastQuery(); return got == "Bearer from-the-secret" })
}

func TestTLSNeedsTheCAOrAskingToSkipVerification(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("[]")) }))
	t.Cleanup(tlsSrv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	r := newRig(t, config(tlsSrv.URL), nil)
	r.waitState(model.PluginError)
	if got := r.row().Message; got != "tls" {
		t.Errorf("without the CA: message = %q, want tls", got)
	}
	c := config(tlsSrv.URL)
	c.TLS.CAFile = caFile
	r.reconfigure(c)
	r.waitState(model.PluginHealthy)
	c = config(tlsSrv.URL)
	c.TLS.InsecureSkipVerify = true
	r.reconfigure(c)
	r.waitState(model.PluginHealthy)
}

func TestAttentionThroughTheSupervisor(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now,
		check{"infrastructure", "UniFi", "unifi.example.com", []bool{true, false, false}},
		check{"apps", "Grafana", "grafana.example.com", []bool{true, false}},
		check{"apps", "Plex", "plex.example.com", []bool{true, true}},
	), 0)
	for name, critical := range map[string][]string{"empty": {}, "exact": {"infrastructure"}, "other case": {"Infrastructure"}} {
		t.Run(name, func(t *testing.T) {
			c := config(f.URL)
			c.CriticalGroups = critical
			r := newRig(t, c, nil)
			r.waitState(model.PluginHealthy)
			sev := map[string]model.Severity{}
			var unifi model.AttentionItem
			for _, it := range r.attention() {
				sev[it.ID] = it.Severity
				if it.ID == "gatus:infrastructure_unifi" {
					unifi = it
				}
			}
			wantUnifi := model.SeverityWarn
			if len(critical) > 0 {
				wantUnifi = model.SeverityCrit
			}
			if len(sev) != 2 || sev["gatus:infrastructure_unifi"] != wantUnifi || sev["gatus:apps_grafana"] != model.SeverityWarn {
				t.Errorf("items = %v, want unifi %s and grafana warn", sev, wantUnifi)
			}
			if want := now.Add(-time.Minute); !unifi.Since.Equal(want) {
				t.Errorf("since = %v, want the first failure of the streak, %v", unifi.Since, want)
			}
		})
	}
}

func TestRefs(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now,
		check{"apps", "Grafana", "grafana.example.com", []bool{true}},
		check{"infrastructure", "UniFi", "unifi.example.com", []bool{false}},
	), 0)
	r := newRig(t, config(f.URL), nil)
	r.waitState(model.PluginHealthy)
	data, _, _ := r.status()
	plugintest.AssertRefs(t, r.sup, pluginID, data, model.Endpoint{})
	for _, it := range r.attention() {
		plugintest.AssertRefs(t, r.sup, pluginID, it)
		if !strings.HasPrefix(it.Ref, "gatus:Endpoint/") {
			t.Errorf("attention ref = %q", it.Ref)
		}
	}
	if len(data.Endpoints) != 2 || data.Endpoints[0].Ref != "gatus:Endpoint/apps_grafana" {
		t.Errorf("endpoints = %+v", data.Endpoints)
	}
}

// The service the Manager's apps and vms builders read: it follows a poll through the exported
// interface, health dots included.
func TestServiceFollowsThePoll(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now,
		check{"apps", "Grafana", "Grafana.Example.com", []bool{true}},
		check{"infrastructure", "rpi-control SSH", "192.0.2.50", []bool{true}},
	), 0)
	c := config(f.URL)
	c.CriticalGroups = []string{"infrastructure"}
	r := newRig(t, c, nil)
	r.waitState(model.PluginHealthy)
	es := r.endpoints()
	if es == nil {
		t.Fatal("no service")
	}
	if state, known := es.ByHost("grafana.example.com"); !known || state != model.HealthOK {
		t.Errorf("ByHost = %s, %v", state, known)
	}
	if up, known := es.ByName("RPI-CONTROL ssh"); !known || !up {
		t.Errorf("ByName = %v, %v", up, known)
	}
	rev := es.Revision()
	if rev == 0 {
		t.Fatal("Revision is 0 after a poll")
	}

	f.answer(gatusJSON(now,
		check{"apps", "Grafana", "Grafana.Example.com", []bool{true, false}},
		check{"infrastructure", "rpi-control SSH", "192.0.2.50", []bool{true}},
	), 0)
	eventually(t, "Revision to move on a health flip", func() bool { return es.Revision() > rev })
	if state, _ := es.ByHost("grafana.example.com"); state != model.HealthWarn {
		t.Errorf("ByHost after the flip = %s", state)
	}
	if up, known := es.ByName("rpi-control ssh"); !known || !up {
		t.Errorf("ByName after the flip = %v, %v", up, known)
	}
}

// Revision test (a), M1: Gatus configured but down at start still finishes its first poll.
func TestFirstPollThatFailsOpensTheGate(t *testing.T) {
	f := newFakeGatus(t)
	f.answer("boom", http.StatusInternalServerError)
	r := newRig(t, config(f.URL), nil)
	r.waitState(model.PluginError)
	es := r.endpoints()
	if es == nil {
		t.Fatal("an erroring instance exports no service")
	}
	if es.Revision() == 0 {
		t.Fatal("Revision is 0 although the first poll has finished")
	}
	srcs := es.SourceStatuses()
	if len(srcs) != 1 || srcs[0].Name != "gatus" || srcs[0].OK || srcs[0].Error != "http_500" {
		t.Errorf("SourceStatuses = %+v", srcs)
	}
	if _, known := es.ByHost("x.example.com"); known {
		t.Error("an unknown host is known")
	}
	if data, published, ok := r.status(); !ok || len(data.Endpoints) != 0 || len(published) != 1 || published[0].Error != "http_500" {
		t.Errorf("published %+v %+v %v", data, published, ok)
	}
}

func TestRevisionIsZeroWhileTheFirstPollIsInFlight(t *testing.T) {
	f := newFakeGatus(t)
	f.block = make(chan struct{})
	r := newRig(t, config(f.URL), nil)
	eventually(t, "the first request", func() bool { return f.hits.Load() > 0 })
	es := r.endpoints()
	if es == nil {
		t.Fatal("a starting instance exports no service")
	}
	if es.Revision() != 0 {
		t.Errorf("Revision = %d before the first poll finished", es.Revision())
	}
	if srcs := es.SourceStatuses(); len(srcs) != 1 || srcs[0].OK || srcs[0].Error != notPolled {
		t.Errorf("SourceStatuses = %+v", srcs)
	}
	close(f.block)
	eventually(t, "Revision", func() bool { return es.Revision() > 0 })
}

// Revision test (b), M1: a Reload builds a new service whose revisions are above every earlier
// one, whether the first poll succeeds or fails.
func TestReloadNeverLowersOrRepeatsARevision(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now, check{"apps", "Grafana", "grafana.example.com", []bool{true}}), 0)
	r := newRig(t, config(f.URL), nil)
	r.waitState(model.PluginHealthy)

	prev := r.endpoints()
	seen := prev.Revision()
	for i := range 4 {
		if i == 2 {
			f.answer("down", http.StatusServiceUnavailable) // the successor's first poll fails
		}
		c := config(f.URL)
		c.CriticalGroups = []string{fmt.Sprint("group", i)} // a change of the section restarts the instance
		r.reconfigure(c)
		var next svc.EndpointHealth
		eventually(t, "a new service", func() bool {
			next = r.endpoints()
			return next != nil && next != prev
		})
		eventually(t, "its first poll", func() bool { return next.Revision() > 0 })
		if next.Revision() <= seen {
			t.Fatalf("reload %d: Revision %d is not above the predecessor's %d", i, next.Revision(), seen)
		}
		prev, seen = next, next.Revision()
	}
	if got := r.endpoints().SourceStatuses(); len(got) != 1 || got[0].OK {
		t.Errorf("the last service should see the failing source: %+v", got)
	}
}

func TestStopCancelsAPollInFlight(t *testing.T) {
	f := newFakeGatus(t)
	f.block = make(chan struct{}) // never released: only the client giving up ends the request
	r := newRig(t, config(f.URL), nil)
	eventually(t, "the request", func() bool { return f.hits.Load() > 0 })
	begin := time.Now()
	r.sup.Shutdown(context.Background())
	if d := time.Since(begin); d > 700*time.Millisecond {
		t.Errorf("Shutdown took %v with a poll in flight", d)
	}
	if ev, ok := r.hub.Last("gatus.status"); ok {
		t.Errorf("a cancelled poll published: %+v", ev)
	}
}

// Visibility, rule (a): a topic with a stored event reaches a subscriber that opened while the
// plugin was hidden, after the core.plugins event that shows it.
func TestTopicsReachAnOpenStream(t *testing.T) {
	f := newFakeGatus(t)
	f.answer(gatusJSON(now, check{"apps", "Grafana", "grafana.example.com", []bool{true}}), 0)
	plugintest.AssertTopicsReachOpenStream(t, factory,
		plugin.InstanceConfig{Config: &pluginconf.Gatus{PageSize: 30}},
		plugin.InstanceConfig{Config: &pluginconf.Gatus{URL: f.URL, PageSize: 30}})
}

func TestDefaultFactoryPolls30Seconds(t *testing.T) {
	if p := New().(*Plugin); p.interval != 30*time.Second {
		t.Errorf("interval = %v", p.interval)
	}
}
