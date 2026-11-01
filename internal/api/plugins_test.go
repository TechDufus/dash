package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// pluginEnv is a Server over a real hub and a real plugin.Supervisor running fake plugins.
type pluginEnv struct {
	*Server
	sup *plugin.Supervisor
	set *plugintest.Settings
	hub *hub.Hub
	dir string // the audit log's directory
	clk *auditClock
}

// factoryOf is what newPluginEnv registers: a *plugintest.Fake, or a wrapper around one.
type factoryOf interface{ Factory() plugin.Factory }

// newPluginEnv registers the plugins with the supervisor, starts it with ics as the plugin
// settings, and serves it through an api.Server whose clock and audit log follow clk.
func newPluginEnv(t *testing.T, ics []plugin.InstanceConfig, fakes ...factoryOf) *pluginEnv {
	t.Helper()
	return newPluginEnvOpt(t, nil, ics, fakes...)
}

// newPluginEnvOpt is newPluginEnv with the supervisor's Options adjusted by opt (the core
// callbacks a test wants: ScrubLog).
func newPluginEnvOpt(t *testing.T, opt func(*plugin.Options), ics []plugin.InstanceConfig, fakes ...factoryOf) *pluginEnv {
	t.Helper()
	h := hub.New()
	set := &plugintest.Settings{}
	set.Set(ics...)
	o := plugin.Options{
		Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond, StopTimeout: time.Second,
		Log: slog.New(slog.DiscardHandler),
	}
	if opt != nil {
		opt(&o)
	}
	sup := plugin.NewSupervisor(o)
	for _, f := range fakes {
		if err := sup.Add(f.Factory()); err != nil {
			t.Fatalf("Add %v: %v", f, err)
		}
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })

	e := &pluginEnv{sup: sup, set: set, hub: h, dir: t.TempDir(), clk: newAuditClock()}
	e.Server = newServer(func(d *Deps) {
		d.Hub, d.Plugins, d.Audit = h, sup, openAudit(t, e.dir, e.clk.now)
	})
	e.Server.now = e.clk.now
	return e
}

func (e *pluginEnv) eventually(t *testing.T, what string, cond func() bool) {
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

// infos is GET /api/v1/plugins.
func (e *pluginEnv) infos(t *testing.T) map[string]model.PluginInfo {
	t.Helper()
	w := req{path: "/api/v1/plugins"}.do(e.Handler())
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/plugins = %d %s", w.Code, w.Body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var list []model.PluginInfo
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("body %s: %v", w.Body, err)
	}
	out := map[string]model.PluginInfo{}
	for _, pi := range list {
		out[pi.ID] = pi
	}
	return out
}

func TestPluginsListShowsEveryState(t *testing.T) {
	errStart := plugintest.New("err")
	errStart.StartFn = func(context.Context, plugin.Host, any) error { return context.DeadlineExceeded }
	unconfigured := plugintest.New("unc")
	unconfigured.ConfiguredFn = func(any) bool { return false }

	e := newPluginEnv(t,
		[]plugin.InstanceConfig{
			{Name: "dis", Enabled: new(false)},
			{Name: "unc", Enabled: new(true)},
		},
		plugintest.New("dis"),
		unconfigured,
		plugintest.New("blk", plugintest.Requires("dis")),
		plugintest.New("una", plugintest.Status(plugin.StateUnavailable, "upstream down")),
		plugintest.New("sta", plugintest.Status(plugin.StateStarting, "warming up")),
		plugintest.New("hea"),
		plugintest.New("deg", plugintest.Status(plugin.StateDegraded, "one source failing")),
		errStart,
	)
	want := map[string]model.PluginState{
		"dis": model.PluginDisabled, "unc": model.PluginUnconfigured, "blk": model.PluginBlocked,
		"una": model.PluginUnavailable, "sta": model.PluginStarting, "hea": model.PluginHealthy,
		"deg": model.PluginDegraded, "err": model.PluginError,
	}
	e.eventually(t, "every state", func() bool {
		got := e.infos(t)
		for id, st := range want {
			if got[id].State != st {
				return false
			}
		}
		return len(got) == len(want)
	})
	got := e.infos(t)
	if got["deg"].Message != "one source failing" || got["hea"].Name != "hea" || got["hea"].Version != "1.0.0" {
		t.Errorf("rows = %+v", got)
	}

	// Without plugins the answer is an empty array, not null. (newServer registers the fake
	// prometheus plugin next to Deps.Prom, so a server with none has no Prom either.)
	noPlugins := newServer(func(d *Deps) { d.Prom = nil })
	if w := (req{path: "/api/v1/plugins"}.do(noPlugins.Handler())); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("no plugins = %d %q, want 200 []", w.Code, w.Body)
	}
}

func TestPluginRoutesMountUnderPluginPrefix(t *testing.T) {
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/vms/{vmid}", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"vmid": r.PathValue("vmid")})
		}, plugin.RouteSpec{Summary: "one vm", Scope: "read"})
	}
	e := newPluginEnv(t, nil, f)
	h := e.Handler()

	w := req{path: "/api/v1/plugins/aa/vms/101"}.do(h)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"vmid":"101"}` {
		t.Fatalf("GET plugin route = %d %s", w.Code, w.Body)
	}
	// The instance spelled with percent-escapes is the same instance, and a miss under it gets
	// the JSON envelope like any other (not the plain-text 404 of http.StripPrefix).
	w = req{path: "/api/v1/plugins/%61a/vms/101"}.do(h)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"vmid":"101"}` {
		t.Fatalf("GET plugin route with an escaped instance = %d %s", w.Code, w.Body)
	}
	w = req{path: "/api/v1/plugins/%61a/nope"}.do(h)
	if e := apiError(t, w); w.Code != http.StatusNotFound || e.Code != model.ErrCodeNotFound {
		t.Errorf("miss under an escaped instance = %d %s", w.Code, w.Body)
	}

	// Only GET (and HEAD) is mounted, and only what the plugin registered.
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/plugins/aa/vms/101"},
		{"DELETE", "/api/v1/plugins/aa/vms/101"},
		{"GET", "/api/v1/plugins/aa/nope"},
		{"GET", "/api/v1/plugins/aa"},
		{"GET", "/api/v1/plugins/ghost/vms/101"},
		{"GET", "/api/v1/plugins/BAD!/vms/101"},
	} {
		w := req{method: tc.method, path: tc.path}.do(h)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, w.Code)
			continue
		}
		var e model.APIError
		if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != model.ErrCodeNotFound {
			t.Errorf("%s %s: body %s is not a not_found envelope (%v)", tc.method, tc.path, w.Body, err)
		}
	}

	// The routes belong to the running instance: once it stops there are none.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: new(false)})
	e.sup.Reload()
	e.eventually(t, "the instance to stop", func() bool { return !e.sup.Running("aa") })
	if w := (req{path: "/api/v1/plugins/aa/vms/101"}.do(h)); w.Code != http.StatusNotFound {
		t.Errorf("stopped instance answered %d", w.Code)
	}
}

func TestSeriesFromPluginBeatsLegacy(t *testing.T) {
	f := plugintest.New("hw")
	f.M.Series = []plugin.SeriesSpec{
		{Name: "host.cpu", Expr: func(a plugin.SeriesArgs) (string, error) { return `plugin_cpu{node="` + a.HostNodename + `"}`, nil }},
		{Name: "hw.broken", Expr: func(plugin.SeriesArgs) (string, error) { return "", context.Canceled }},
	}
	prom := &fakeProm{}
	e := newPluginEnv(t, nil, f)
	e.d.Prom = prom
	h := e.Handler()

	if w := (req{path: "/api/v1/series?name=host.cpu&range=1h"}.do(h)); w.Code != http.StatusOK {
		t.Fatalf("series = %d %s", w.Code, w.Body)
	}
	if want := `plugin_cpu{node="` + e.d.Cfg.HostNodename + `"}`; prom.expr != want {
		t.Errorf("expr = %q, want the plugin's %q", prom.expr, want)
	}
	if w := (req{path: "/api/v1/series?name=hw.broken&range=1h"}.do(h)); w.Code != http.StatusBadRequest {
		t.Errorf("a plugin's series error = %d, want 400", w.Code)
	}

	// Stopped, the plugin's series is gone and the built-in one answers again.
	e.set.Set(plugin.InstanceConfig{Name: "hw", Enabled: new(false)})
	e.sup.Reload()
	e.eventually(t, "the instance to stop", func() bool { return !e.sup.Running("hw") })
	prom.expr = ""
	if w := (req{path: "/api/v1/series?name=host.cpu&range=1h"}.do(h)); w.Code != http.StatusOK {
		t.Fatalf("series = %d %s", w.Code, w.Body)
	}
	if prom.expr == "" || strings.HasPrefix(prom.expr, "plugin_cpu") {
		t.Errorf("expr = %q, want the built-in query", prom.expr)
	}
}

// itemsData is the payload of the fake plugin's topic.
type itemsData struct {
	Items []string `json:"items"`
}

// The G2 acceptance: a plugin publishes a topic, shows up in /api/v1/plugins, and disappears
// when disabled, all without a reload of the page (the open stream carries every change).
func TestPluginAppearsAndDisappearsLive(t *testing.T) {
	f := plugintest.New("fake", plugintest.Typed("items", itemsData{}))
	f.M.UI.Nav = []model.NavItem{{Path: "/fake", Label: "Fake", Title: "Fake items", Icon: "box"}}
	f.StartFn = func(_ context.Context, h plugin.Host, _ any) error {
		h.Publish("items", itemsData{Items: []string{"a", "b"}}, []model.SourceStatus{})
		return nil
	}
	e := newPluginEnv(t, nil, f)
	e.heartbeat = 20 * time.Millisecond
	h := e.Handler()

	// The snapshot of a stream opened once both topics exist lists core.plugins first (the
	// hub's order), then the plugin's topic.
	e.eventually(t, "core.plugins to list the plugin", func() bool {
		ev, ok := e.hub.Latest("", model.TopicPlugins)
		return ok && strings.Contains(string(ev.Data), `"state":"healthy"`)
	})
	_, lines := sseClient(t, h, "/api/v1/stream")
	expect(t, lines, "event: core.plugins")
	expectContaining(t, lines, `"id":"fake"`)
	expect(t, lines, "event: fake.items")
	expectContaining(t, lines, `"items":["a","b"]`)

	e.eventually(t, "the plugin to be healthy", func() bool { return e.infos(t)["fake"].State == model.PluginHealthy })
	pi := e.infos(t)["fake"]
	if !slices.Equal(pi.Topics, []string{"fake.items"}) || len(pi.Nav) != 1 || pi.Nav[0].Path != "/fake" || pi.Nav[0].Icon != "box" {
		t.Errorf("row = %+v", pi)
	}
	if w := (req{path: "/api/v1/topics/fake.items"}.do(h)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items":["a","b"]`) {
		t.Fatalf("snapshot = %d %s", w.Code, w.Body)
	}

	// Disabled: the open stream learns it, new streams and the snapshot no longer have it.
	e.set.Set(plugin.InstanceConfig{Name: "fake", Enabled: new(false)})
	e.sup.Reload()
	expectContaining(t, lines, `"state":"disabled"`)
	if got := e.infos(t)["fake"].State; got != model.PluginDisabled {
		t.Errorf("state after disabling = %q", got)
	}
	if w := (req{path: "/api/v1/topics/fake.items"}.do(h)); w.Code != http.StatusNotFound {
		t.Errorf("snapshot of a disabled plugin = %d, want 404", w.Code)
	}
	_, fresh := sseClient(t, h, "/api/v1/stream")
	for _, l := range untilPing(t, fresh) {
		if l == "event: fake.items" {
			t.Fatal("a new stream carries the disabled plugin's topic")
		}
	}

	// Enabled again: it reappears on the same open stream.
	e.set.Set(plugin.InstanceConfig{Name: "fake", Enabled: new(true)})
	e.sup.Reload()
	expect(t, lines, "event: fake.items")
	expectContaining(t, lines, `"items":["a","b"]`)
	e.eventually(t, "the plugin to be healthy again", func() bool { return e.infos(t)["fake"].State == model.PluginHealthy })
	if w := (req{path: "/api/v1/topics/fake.items"}.do(h)); w.Code != http.StatusOK {
		t.Errorf("snapshot after re-enabling = %d", w.Code)
	}
}

// untilPing returns what the stream sends before its first heartbeat.
func untilPing(t *testing.T, lines <-chan string) []string {
	t.Helper()
	var got []string
	timeout := time.After(3 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream ended before a heartbeat")
			}
			if l == ": ping" {
				return got
			}
			got = append(got, l)
		case <-timeout:
			t.Fatal("no heartbeat")
		}
	}
}
