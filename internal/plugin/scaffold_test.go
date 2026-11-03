package plugin_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

func TestRefRoundTrip(t *testing.T) {
	for _, c := range []struct {
		plugin    plugin.ID
		kind      string
		id        []string
		want      string
		wantKind  string
		wantIDStr string
	}{
		{"kubernetes", "Pod", []string{"media", "app-0"}, "kubernetes:Pod/media/app-0", "Pod", "media/app-0"},
		{"proxmox", "VM", []string{"101"}, "proxmox:VM/101", "VM", "101"},
		{"alertmanager", "Alert", []string{"a1b2c3"}, "alertmanager:Alert/a1b2c3", "Alert", "a1b2c3"},
		{"gatus", "Endpoint", []string{"infra_unifi"}, "gatus:Endpoint/infra_unifi", "Endpoint", "infra_unifi"},
	} {
		got := plugin.Ref(c.plugin, c.kind, c.id...)
		if got != c.want {
			t.Errorf("Ref = %q, want %q", got, c.want)
		}
		p, kind, id, ok := plugin.ParseRef(got)
		if !ok || p != c.plugin || kind != c.wantKind || id != c.wantIDStr {
			t.Errorf("ParseRef(%q) = %q %q %q %v", got, p, kind, id, ok)
		}
	}
}

func TestParseRefRejects(t *testing.T) {
	for _, s := range []string{
		"", "nothing", "kubernetes:", "kubernetes:Pod", "kubernetes:Pod/", ":Pod/x", "Core:Pod/x", "core:Pod/x",
		"a-b:Pod/x", "kubernetes:/x", "kubernetes:9Pod/x", "kubernetes:Po d/x",
	} {
		if p, k, id, ok := plugin.ParseRef(s); ok {
			t.Errorf("ParseRef(%q) accepted: %q %q %q", s, p, k, id)
		}
	}
}

func TestLinksResolveRunningProvidersInRegistryOrder(t *testing.T) {
	mk := func(id plugin.ID, kind string, href string) *plugintest.Fake {
		f := plugintest.New(id)
		f.LinksFn = func(req plugin.LinkRequest) []model.Link {
			if req.Kind != "Pod" {
				return nil
			}
			return []model.Link{{Label: "open " + string(id), Href: href + req.Namespace + "/" + req.Name, Kind: model.LinkKind(kind)}}
		}
		return f
	}
	off := false
	boom := plugintest.New("bm")
	boom.LinksFn = func(plugin.LinkRequest) []model.Link { panic("links boom") }
	noKind := plugintest.New("nk")
	noKind.LinksFn = func(plugin.LinkRequest) []model.Link { return []model.Link{{Label: "x", Href: "https://x.example"}} }
	e := newEnv(t, nil,
		mk("aa", "aa", "https://a.example/").Factory(),
		mk("bb", "bb", "https://b.example/").Factory(),
		mk("cc", "cc", "https://c.example/").Factory(),
		boom.Factory(), noKind.Factory(),
	)
	e.set.Set(plugin.InstanceConfig{Name: "bb", Enabled: &off})
	e.run(t)

	got := e.sup.Links().Resolve(plugin.LinkRequest{Kind: "Pod", Namespace: "media", Name: "app-0"})
	var hrefs []string
	for _, l := range got {
		hrefs = append(hrefs, l.Href)
	}
	want := []string{"https://a.example/media/app-0", "https://c.example/media/app-0", "https://x.example"}
	if !slices.Equal(hrefs, want) {
		t.Fatalf("links = %v, want %v (bb is disabled; bm panicked and is skipped)", hrefs, want)
	}
	if got[2].Kind != "nk" {
		t.Errorf("a link with no kind gets the provider's id, got %q", got[2].Kind)
	}
	if none := e.sup.Links().Resolve(plugin.LinkRequest{Kind: "Nothing"}); none == nil || len(none) != 1 { // nk always answers
		t.Errorf("Resolve = %#v", none)
	}
	// With no provider at all the answer is empty and not nil.
	if r := plugin.NoLinks.Resolve(plugin.LinkRequest{Kind: "Pod"}); r == nil || len(r) != 0 {
		t.Errorf("NoLinks.Resolve = %#v, want an empty non-nil slice", r)
	}
}

// TestHostLinksAreTheSupervisorsSnapshot checks that a plugin reaches the providers through its Host.
func TestHostLinksReachProviders(t *testing.T) {
	provider := plugintest.New("gr")
	provider.LinksFn = func(req plugin.LinkRequest) []model.Link {
		return []model.Link{{Label: "g", Href: "https://g.example/" + req.Kind}}
	}
	consumer := plugintest.New("co", plugintest.Uses("gr"))
	e := start(t, provider.Factory(), consumer.Factory())
	_ = e
	got := consumer.Host().Links().Resolve(plugin.LinkRequest{Kind: "Alerts"})
	if len(got) != 1 || got[0].Href != "https://g.example/Alerts" {
		t.Fatalf("Host.Links = %+v", got)
	}
}

func TestSupervisorServiceNeedsNoDeclaredDependency(t *testing.T) {
	type svc struct{ n int }
	provider := plugintest.New("pv")
	provider.ServiceVal = &svc{n: 7}
	stranger := plugintest.New("st") // declares no dependency on pv
	e := start(t, provider.Factory(), stranger.Factory())
	if v, ok := e.sup.Service("pv"); !ok || v.(*svc).n != 7 {
		t.Fatalf("Service(pv) = %v %v", v, ok)
	}
	if _, ok := stranger.Host().Service("pv"); ok {
		t.Error("Host.Service answered for a plugin that did not declare the dependency")
	}
	if _, ok := e.sup.Service("st"); ok {
		t.Error("a plugin that is not a Provider has no service")
	}
	if _, ok := e.sup.Service("ghost"); ok {
		t.Error("an unknown instance has no service")
	}
	e.set.Set(plugin.InstanceConfig{Name: "pv", Enabled: new(false)})
	e.reloadUntil(t, "pv to stop", func() bool { _, ok := e.sup.Service("pv"); return !ok })
}

type countingRT struct {
	id    plugin.ID
	inner http.RoundTripper
	calls *atomic.Int32
}

func (c countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	r.Header.Set("X-Wrapped-For", string(c.id))
	return c.inner.RoundTrip(r)
}

func TestTransportWrapsPluginClientsOnly(t *testing.T) {
	var calls atomic.Int32
	var saw atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { saw.Store(r.Header.Get("X-Wrapped-For")) }))
	defer srv.Close()
	f := plugintest.New("aa")
	e := newEnv(t, func(o *plugin.Options) {
		o.Transport = func(id plugin.ID, rt http.RoundTripper) http.RoundTripper {
			return countingRT{id, rt, &calls}
		}
	}, f.Factory()).run(t)
	_ = e
	hc, err := f.Host().HTTPClient(plugin.HTTPConfig{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if calls.Load() != 1 || saw.Load() != "aa" {
		t.Errorf("the wrapped transport saw %d calls for %v, want 1 for aa", calls.Load(), saw.Load())
	}
}

func TestScrubLogUsesTheOption(t *testing.T) {
	f := plugintest.New("aa")
	e := newEnv(t, func(o *plugin.Options) {
		o.ScrubLog = func(s string) string { return strings.ReplaceAll(s, "hunter2", "[redacted]") }
	}, f.Factory()).run(t)
	if got := f.Host().ScrubLog("password hunter2 failed"); got != "password [redacted] failed" {
		t.Errorf("ScrubLog = %q", got)
	}
	if got := e.sup.HostFor(plugin.Core).ScrubLog("hunter2"); got != "[redacted]" {
		t.Errorf("core ScrubLog = %q", got)
	}
	plain := plugintest.New("bb")
	start(t, plain.Factory())
	if got := plain.Host().ScrubLog("hunter2"); got != "hunter2" {
		t.Errorf("without the option the line is unchanged, got %q", got)
	}
}

func TestIconBaseURLUsesTheOption(t *testing.T) {
	const base = "https://cdn.example.com/icons/png"
	f := plugintest.New("aa")
	e := newEnv(t, func(o *plugin.Options) { o.IconBase = base }, f.Factory()).run(t)
	if got := f.Host().IconBaseURL(); got != base {
		t.Errorf("IconBaseURL = %q, want %q", got, base)
	}
	if got := e.sup.HostFor(plugin.Core).IconBaseURL(); got != base {
		t.Errorf("core IconBaseURL = %q, want %q", got, base)
	}
	plain := plugintest.New("bb")
	start(t, plain.Factory())
	if got := plain.Host().IconBaseURL(); got != "" {
		t.Errorf("without the option IconBaseURL = %q, want empty", got)
	}
	hub := &plugintest.Hub{}
	if got := plugintest.AllRunningWithIconBase(hub, base).HostFor("aa").IconBaseURL(); got != base {
		t.Errorf("plugintest IconBaseURL = %q, want %q", got, base)
	}
	if got := plugintest.AllRunning(hub).HostFor("aa").IconBaseURL(); got != "" {
		t.Errorf("plugintest default IconBaseURL = %q, want empty", got)
	}
}

func TestDemoModeRunsTheTwin(t *testing.T) {
	cfgFn := plugintest.Config(func() any { return &cfg{} })
	real := plugintest.New("aa", cfgFn, plugintest.Topics("t"), func(f *plugintest.Fake) {
		f.M.Actions = []plugin.ActionSpec{{Name: "go", Title: "Go"}}
		f.ConfiguredFn = func(any) bool { return false } // the real plugin has no upstream in demo mode
	})
	twin := plugintest.New("aa", cfgFn, plugintest.Topics("t"))
	twin.StartFn = func(_ context.Context, h plugin.Host, _ any) error {
		h.Publish("t", map[string]int{"demo": 1}, nil)
		return nil
	}
	twin.ActionFn = func(context.Context, string, auth.Principal, any) (any, error) { return "twin", nil }
	plain := plugintest.New("bb", plugintest.Topics("t")) // configured: ConfiguredFn is nil

	off := false
	mod := func(o *plugin.Options) { o.Demo = true }
	e := newEnv(t, mod, plugintest.WithDemo(real, twin), plain.Factory())
	on := true
	e.set.Set(plugin.InstanceConfig{Name: "bb", Enabled: &on}) // explicitly enabled, too
	e.run(t)

	if twin.Starts() != 1 || real.Starts() != 0 {
		t.Fatalf("starts: twin %d, real %d; want the twin only", twin.Starts(), real.Starts())
	}
	if st, _ := e.state(t, "aa"); st != plugin.StateHealthy {
		t.Errorf("aa = %s, want healthy", st)
	}
	ref, ok := e.sup.Action("aa", "go")
	if !ok {
		t.Fatal("the twin's action is not mounted: demo mode forces actions.enabled")
	}
	if out, err := ref.Run(context.Background(), auth.Principal{Role: auth.RoleAdmin}, nil); err != nil || out != "twin" {
		t.Errorf("action = %v, %v", out, err)
	}
	if ev, ok := e.hub.Last("aa.t"); !ok || ev.Data.(map[string]int)["demo"] != 1 {
		t.Errorf("the twin's topic was not published: %+v", ev)
	}
	// A plugin without a twin stays off in demo mode however it is configured: it never contacts
	// its upstream.
	if st, msg := e.state(t, "bb"); st != plugin.StateDisabled || msg != "no demo data" {
		t.Errorf("a plugin without a twin = %s (%s), want disabled (no demo data)", st, msg)
	}
	if plain.Starts() != 0 {
		t.Errorf("the plugin without a twin was started %d times in demo mode", plain.Starts())
	}

	// Settings can still switch a twin off, so demo mode shows pages appearing and disappearing.
	e.set.Set(plugin.InstanceConfig{Name: "aa", Enabled: &off})
	e.reloadUntil(t, "the twin to stop", func() bool { st, _ := e.state(t, "aa"); return st == plugin.StateDisabled })

	// Without demo mode the same registry runs the real plugin.
	real2 := plugintest.New("aa", cfgFn)
	twin2 := plugintest.New("aa", cfgFn)
	start(t, plugintest.WithDemo(real2, twin2))
	if real2.Starts() != 1 || twin2.Starts() != 0 {
		t.Errorf("without Options.Demo: real %d, twin %d starts", real2.Starts(), twin2.Starts())
	}
}

func TestInfoCarriesMinRole(t *testing.T) {
	f := plugintest.New("aa", func(f *plugintest.Fake) {
		f.M.Actions = []plugin.ActionSpec{
			{Name: "a", Title: "A"}, // MinRole defaults to admin
			{Name: "v", Title: "V", MinRole: auth.RoleViewer},
		}
	})
	f.ActionFn = func(context.Context, string, auth.Principal, any) (any, error) { return nil, nil }
	e := newEnv(t, nil, f.Factory())
	e.set.Set(plugin.InstanceConfig{Name: "aa", ActionsEnabled: true})
	e.run(t)
	acts := e.info(t, "aa").Actions
	if len(acts) != 2 || acts[0].MinRole != "admin" || acts[1].MinRole != "viewer" {
		t.Fatalf("actions = %+v", acts)
	}
}

func TestContributorsCarrySourceStatuses(t *testing.T) {
	healthy := plugintest.New("hh", func(f *plugintest.Fake) {
		f.AttentionFn = func(time.Time) []model.AttentionItem { return nil }
		f.StatusFn = func() plugin.Status {
			return plugin.Status{State: plugin.StateHealthy, Sources: []model.SourceStatus{{Name: "alertmanager", OK: true}}}
		}
	})
	var failing atomic.Bool
	failing.Store(false)
	flaky := plugintest.New("fl", func(f *plugintest.Fake) {
		f.AttentionFn = func(time.Time) []model.AttentionItem { return nil }
		f.StatusFn = func() plugin.Status {
			if failing.Load() {
				return plugin.Status{State: plugin.StateError, Sources: []model.SourceStatus{{Name: "gatus", OK: false, Error: "unreachable"}}}
			}
			return plugin.Status{State: plugin.StateHealthy}
		}
	})
	e := start(t, healthy.Factory(), flaky.Factory())
	cs := e.sup.Contributors()
	if len(cs) != 2 || cs[0].Sources == nil || cs[1].Sources == nil {
		t.Fatalf("contributors = %+v", cs)
	}
	if got := cs[0].Sources(); len(got) != 1 || got[0].Name != "alertmanager" || !got[0].OK {
		t.Errorf("sources = %+v", got)
	}
	if got := cs[1].Sources(); len(got) != 0 {
		t.Errorf("a healthy plugin with no sources reports %+v", got)
	}
	failing.Store(true)
	if got := cs[1].Sources(); len(got) != 1 || got[0].Error != "unreachable" {
		t.Errorf("Sources reads Status now, got %+v", got)
	}
}

func routeServer(t *testing.T, f *plugintest.Fake, cfgs ...plugin.InstanceConfig) *env {
	t.Helper()
	e := newEnv(t, nil, f.Factory())
	e.set.Set(cfgs...)
	return e.run(t)
}

func do(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestRouteGuardSeesPathValuesDenyListsAndStreams(t *testing.T) {
	var (
		served  []plugin.MountedRoute
		nexts   []bool
		gotNS   []string
		handled int
	)
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/pods/{ns}/{name}", func(w http.ResponseWriter, req *http.Request) {
			handled++
			_, _ = w.Write([]byte("plain " + req.PathValue("ns")))
		}, plugin.RouteSpec{Summary: "pod", Namespace: func(r *http.Request) string { return r.PathValue("ns") }})
		r.Stream("/logs/tail", func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { return nil },
			plugin.StreamSpec{Summary: "tail", Scope: "logs", Namespace: func(r *http.Request) string { return r.URL.Query().Get("ns") }})
	}
	e := routeServer(t, f, plugin.InstanceConfig{Name: "aa", ReadDenyNamespaces: []string{"secret"}})
	guard := plugin.RouteGuard{Serve: func(w http.ResponseWriter, r *http.Request, mr plugin.MountedRoute, next http.HandlerFunc) {
		served = append(served, mr)
		nexts = append(nexts, next != nil)
		ns := mr.Info.Spec.Namespace(r)
		gotNS = append(gotNS, ns)
		if slices.Contains(mr.ReadDenyNamespaces, ns) {
			plugin.WriteError(w, http.StatusForbidden, model.ErrCodeForbidden, "denied")
			return
		}
		if next != nil {
			next(w, r)
		}
	}}
	h, ok := e.sup.Router("aa", guard)
	if !ok {
		t.Fatal("no router")
	}
	if rec := do(h, "/api/v1/plugins/aa/pods/media/app-0"); rec.Code != 200 || rec.Body.String() != "plain media" {
		t.Errorf("allowed = %d %q", rec.Code, rec.Body.String())
	}
	if rec := do(h, "/api/v1/plugins/aa/pods/secret/x"); rec.Code != 403 || handled != 1 {
		t.Errorf("denied = %d, handler ran %d times", rec.Code, handled)
	}
	do(h, "/api/v1/plugins/aa/logs/tail?ns=media")
	if !slices.Equal(gotNS, []string{"media", "secret", "media"}) {
		t.Errorf("Namespace saw %v: the path values and the query must be set when the guard runs", gotNS)
	}
	if len(served) != 3 || served[0].Instance != "aa" || served[0].Info.Pattern != "/pods/{ns}/{name}" || !slices.Equal(served[0].ReadDenyNamespaces, []string{"secret"}) {
		t.Errorf("mounted route = %+v", served)
	}
	if !nexts[0] || nexts[2] {
		t.Errorf("next is the plugin's handler for a GET route and nil for a stream, got %v", nexts)
	}
	if st := served[2].Info.Stream; st == nil || st.Scope != "logs" || served[2].Info.StreamHandler == nil || served[2].Info.Handler != nil {
		t.Errorf("stream route info = %+v", served[2].Info)
	}
	// The deny list follows a reconfiguration.
	e.set.Set(plugin.InstanceConfig{Name: "aa", ReadDenyNamespaces: []string{"other"}})
	e.reloadUntil(t, "restart", func() bool { return f.Starts() == 2 })
	served = nil
	eventually(t, "the new deny list", func() bool {
		served = nil
		do(h, "/api/v1/plugins/aa/pods/media/x")
		return len(served) == 1 && slices.Equal(served[0].ReadDenyNamespaces, []string{"other"})
	})
}

func TestRoutesWithoutAGuard(t *testing.T) {
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/x", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }, plugin.RouteSpec{})
		r.Stream("/s", func(context.Context, *http.Request, auth.Principal, plugin.Stream) error { return nil }, plugin.StreamSpec{})
	}
	e := routeServer(t, f)
	h, _ := e.sup.Router("aa", plugin.RouteGuard{})
	if rec := do(h, "/api/v1/plugins/aa/x"); rec.Code != 200 {
		t.Errorf("a plain route runs unguarded, got %d", rec.Code)
	}
	rec := do(h, "/api/v1/plugins/aa/s")
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Errorf("a stream needs the core: %d %s", rec.Code, rec.Body.String())
	}
	infos := e.sup.RouteInfos("aa")
	if len(infos) != 2 || infos[1].Stream == nil || infos[1].Pattern != "/s" {
		t.Errorf("RouteInfos = %+v", infos)
	}
}

func TestFailThroughTheGuardAndWithout(t *testing.T) {
	boom := errors.New("dial tcp 192.0.2.7:9090: connection refused")
	f := plugintest.New("aa")
	f.RoutesFn = func(r plugin.Routes) {
		r.Get("/err", func(w http.ResponseWriter, req *http.Request) { plugin.Fail(w, req, boom) }, plugin.RouteSpec{})
		r.Get("/refuse", func(w http.ResponseWriter, req *http.Request) {
			plugin.Fail(w, req, &plugin.ActionError{Status: 404, Code: model.ErrCodeNotFound, Message: "no such pod"})
		}, plugin.RouteSpec{})
	}
	e := routeServer(t, f)

	// Outside the API: a plain 502 that names nothing, and the plugin's own refusal.
	h, _ := e.sup.Router("aa", plugin.RouteGuard{})
	rec := do(h, "/api/v1/plugins/aa/err")
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "192.0.2.7") || !strings.Contains(rec.Body.String(), `"code":"upstream_unavailable"`) {
		t.Errorf("bare Fail = %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(h, "/api/v1/plugins/aa/refuse"); rec.Code != 404 || !strings.Contains(rec.Body.String(), "no such pod") {
		t.Errorf("bare Fail of an ActionError = %d %s", rec.Code, rec.Body.String())
	}

	// Inside: the core's Fail gets the instance and the error.
	var gotID plugin.ID
	var gotErr error
	h, _ = e.sup.Router("aa", plugin.RouteGuard{Fail: func(w http.ResponseWriter, _ *http.Request, id plugin.ID, err error) {
		gotID, gotErr = id, err
		plugin.WriteError(w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable, string(id)+": unreachable")
	}})
	rec = do(h, "/api/v1/plugins/aa/err")
	if gotID != "aa" || !errors.Is(gotErr, boom) || rec.Body.String() == "" || strings.Contains(rec.Body.String(), "192.0.2.7") {
		t.Errorf("guarded Fail: id %q err %v body %s", gotID, gotErr, rec.Body.String())
	}
}

func TestWriteErrorIsTheEnvelopeWithTheRequestID(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("X-Request-Id", "req-1")
	plugin.WriteError(rec, http.StatusTeapot, model.ErrCodeConflict, "nope")
	if rec.Code != http.StatusTeapot || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, content type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{`"error":"nope"`, `"code":"conflict"`, `"requestId":"req-1"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body %s lacks %s", rec.Body.String(), want)
		}
	}
}
