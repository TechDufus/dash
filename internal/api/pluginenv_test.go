package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/upstream"
)

// The harness is how a test drives the generic behaviour of the API (the error envelope, panic
// recovery, the request id, the metrics route label, the audit shape, secret scrubbing, body
// limits, the host check, CSRF, the stream caps) through the real chain: request id, host check,
// CSRF, auth, mux, the core's route guard, the supervisor's router. No legacy route is involved,
// so the tests keep their subject when a legacy handler is deleted.
//
// It runs two fake plugins on a real Supervisor and hub:
//
//	aa   the action fixture of newActionEnv (restart, scale, ping) plus silence.create and
//	     silence.expire, whose audit target is only known after the action (plugin.Outcome)
//	rt   GET /items (query ns, q), GET /boom, and the stream GET /tail (query ns, pod; scope
//	     "logs", audit target Pod/<ns>/<pod>), all targeting the namespace of ?ns
//
// What each route does is the hook the test sets on the harness before it sends a request
// (items, tail, act); the defaults answer an empty list, hold the stream open until the client
// leaves and answer nil.
type harness struct {
	*actionEnv
	rt *plugintest.Fake
	m  *obs.Metrics

	// items is GET /items after its checks: the body (nil = an empty array) or the error that
	// goes through plugin.Fail.
	items func(r *http.Request) (any, error)
	// tail is the StreamHandler of GET /tail after its checks. It must call s.Begin() itself
	// unless it wants to fail before the stream starts. nil begins and waits for ctx.
	tail plugin.StreamHandler
	// act is what Act of plugin aa answers; nil gives the defaults (silence.create answers an
	// Outcome with the created id, everything else nil).
	act func(action string, in any) (any, error)

	srvOnce sync.Once
	srv     *httptest.Server
}

// harnessOpts are the settings newHarness starts from.
type harnessOpts struct {
	aa, rt plugin.InstanceConfig
	scrub  func(string) string
	routes func(h *harness, r plugin.Routes) // replaces the routes of rt
}

// harnessRT sets the settings of plugin rt (its Name is kept).
func harnessRT(ic plugin.InstanceConfig) func(*harnessOpts) {
	return func(o *harnessOpts) { ic.Name = "rt"; o.rt = ic }
}

// harnessAA sets the settings of plugin aa (its Name is kept).
func harnessAA(ic plugin.InstanceConfig) func(*harnessOpts) {
	return func(o *harnessOpts) { ic.Name = "aa"; o.aa = ic }
}

// harnessScrub sets Host.ScrubLog of every plugin.
func harnessScrub(f func(string) string) func(*harnessOpts) {
	return func(o *harnessOpts) { o.scrub = f }
}

// harnessRoutes replaces the routes of rt.
func harnessRoutes(f func(h *harness, r plugin.Routes)) func(*harnessOpts) {
	return func(o *harnessOpts) { o.routes = f }
}

// silenceIn and expireIn are the inputs of the actions of aa that mimic what a silence plugin
// does: create learns its audit target (the id) only by acting.
type silenceIn struct {
	Alertname string `json:"alertname" dash:"required"`
	Comment   string `json:"comment,omitempty"`
}

type expireIn struct {
	ID string `json:"id" dash:"required"`
}

// silenceID is the id the default silence.create answers with.
const silenceID = "sil-1234"

// harnessActions are the actions aa has besides the action fixture's.
func harnessActions() []plugin.ActionSpec {
	return []plugin.ActionSpec{
		{
			Name: "silence.create", Title: "Silence",
			Input:  func() any { return &silenceIn{} },
			Target: func(in any) (string, string, string) { return "Silence", "", in.(*silenceIn).Alertname },
		},
		{
			Name: "silence.expire", Title: "Expire", Idempotent: true,
			Input:  func() any { return &expireIn{} },
			Target: func(in any) (string, string, string) { return "Silence", "", in.(*expireIn).ID },
		},
	}
}

// nsQuery is the namespace a request of rt targets.
func nsQuery(r *http.Request) string { return firstOf(r, "ns", "namespace") }

// podTarget is the audit target of an open of /tail.
func podTarget(r *http.Request) (string, string, string) {
	return "Pod", nsQuery(r), r.URL.Query().Get("pod")
}

// newHarness starts the two plugins and serves them with metrics on.
func newHarness(t *testing.T, mods ...func(*harnessOpts)) *harness {
	t.Helper()
	o := harnessOpts{aa: plugin.InstanceConfig{Name: "aa", ActionsEnabled: true}, rt: plugin.InstanceConfig{Name: "rt"}}
	for _, m := range mods {
		m(&o)
	}
	h := &harness{actionEnv: &actionEnv{}}
	fn := func(action string, in any) (any, error) {
		if h.act != nil {
			return h.act(action, in)
		}
		if action == "silence.create" {
			return plugin.Outcome{Body: map[string]string{"silenceId": silenceID}, Target: &plugin.ActionTarget{Kind: "Silence", Name: silenceID}}, nil
		}
		return nil, nil
	}
	aa := h.actionEnv.fixture(fn, harnessActions())
	h.rt = plugintest.New("rt")
	h.rt.M.Name = "Routes"
	h.rt.RoutesFn = func(r plugin.Routes) {
		if o.routes != nil {
			o.routes(h, r)
			return
		}
		h.stdRoutes(r)
	}
	var opt func(*plugin.Options)
	if o.scrub != nil {
		opt = func(po *plugin.Options) { po.ScrubLog = o.scrub }
	}
	h.actionEnv.pluginEnv = newPluginEnvOpt(t, opt, []plugin.InstanceConfig{o.aa, o.rt}, aa, h.rt)
	h.m = obs.New("test", RoutePatterns(), h.sup.Infos)
	h.Server.d.Metrics = h.m
	h.eventually(t, "both plugins to run", func() bool { return h.sup.Running("aa") && h.sup.Running("rt") })
	return h
}

// stdRoutes is the default route set of rt.
func (h *harness) stdRoutes(r plugin.Routes) {
	r.Get("/items", h.serveItems, plugin.RouteSpec{
		Summary: "Items", Scope: "read", Namespace: nsQuery, Response: []string{},
		Query: []plugin.Param{{Name: "ns", Doc: "Namespace."}, {Name: "q", Doc: "Text to look for."}},
	})
	r.Get("/ns/{ns}/items", h.serveItems, plugin.RouteSpec{
		Summary: "Items of one namespace", Scope: "read", Response: []string{},
		Namespace: func(r *http.Request) string { return r.PathValue("ns") },
	})
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("kaboom in a handler") }, plugin.RouteSpec{Summary: "Boom", Scope: "read"})
	r.Stream("/tail", h.serveTail, plugin.StreamSpec{
		Summary: "Tail", Scope: "logs", Target: podTarget, Namespace: nsQuery,
		Query: []plugin.Param{{Name: "ns", Doc: "Namespace.", Required: true}, {Name: "pod", Doc: "Pod.", Required: true}},
	})
}

func (h *harness) serveItems(w http.ResponseWriter, r *http.Request) {
	if h.items == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	body, err := h.items(r)
	if err != nil {
		plugin.Fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (h *harness) serveTail(ctx context.Context, r *http.Request, p auth.Principal, s plugin.Stream) error {
	if nsQuery(r) == "" || r.URL.Query().Get("pod") == "" {
		return fmt.Errorf("%w: a namespace and a pod are required", upstream.ErrInvalid)
	}
	if h.tail != nil {
		return h.tail(ctx, r, p, s)
	}
	if err := s.Begin(); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// get is a GET as alice through the whole chain.
func (h *harness) get(path string) *httptest.ResponseRecorder {
	return req{path: path}.do(h.Handler())
}

// getAs is get as user with more headers.
func (h *harness) getAs(path, user string, header map[string]string) *httptest.ResponseRecorder {
	hd := map[string]string{"X-Forwarded-Preferred-Username": user}
	for k, v := range header {
		hd[k] = v
	}
	return req{path: path, header: hd}.do(h.Handler())
}

// do sends r through the whole chain.
func (h *harness) do(r req) *httptest.ResponseRecorder { return r.do(h.Handler()) }

// audit is every record of the audit log so far.
func (h *harness) audit(t *testing.T) []audit.Record {
	t.Helper()
	return auditRecords(t, h.dir)
}

// scrape is /metrics, parsed.
func (h *harness) scrape(t *testing.T) []seriesOf {
	t.Helper()
	return scrapeMetrics(t, h.Server)
}

// openTails is how many slots of the tail limiter are taken.
func (h *harness) openTails() int {
	h.tails.mu.Lock()
	defer h.tails.mu.Unlock()
	return h.tails.total
}

// attach hands this harness's plugins and hub to a Server built by newServer or serverWithFakes:
// the rest of the test then drives the legacy fakes and the plugins through one chain.
func (h *harness) attach(d *Deps) { d.Hub, d.Plugins = h.hub, h.sup }

// sseStream is one stream opened on a real server.
type sseStream struct {
	t      *testing.T
	resp   *http.Response
	lines  <-chan string
	cancel context.CancelFunc
}

// server is the harness's Server on a loopback listener, started on first use.
func (h *harness) server(t *testing.T) *httptest.Server {
	t.Helper()
	h.srvOnce.Do(func() {
		h.srv = httptest.NewServer(h.Handler())
		t.Cleanup(h.srv.Close)
	})
	return h.srv
}

// openStream GETs path as user with header on a real server and returns the answer as soon as
// its headers are in; the body is read line by line in the background.
func (h *harness) openStream(t *testing.T, path, user string, header map[string]string) *sseStream {
	t.Helper()
	ts := h.server(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
	r.Host = testHost
	r.Header.Set("X-Forwarded-Preferred-Username", user)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return &sseStream{t: t, resp: resp, lines: lines, cancel: cancel}
}

// status is the status code of the answer.
func (s *sseStream) status() int { return s.resp.StatusCode }

// close ends the request the way a browser leaving does.
func (s *sseStream) close() { s.cancel(); _ = s.resp.Body.Close() }

// expect reads lines until one equals want.
func (s *sseStream) expect(want string) { s.t.Helper(); expect(s.t, s.lines, want) }

// expectContaining reads lines until one contains substr.
func (s *sseStream) expectContaining(substr string) {
	s.t.Helper()
	expectContaining(s.t, s.lines, substr)
}

// all reads until the stream ends, which fails after a few seconds, and returns the lines.
func (s *sseStream) all() []string {
	s.t.Helper()
	var out []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-s.lines:
			if !ok {
				return out
			}
			out = append(out, l)
		case <-timeout:
			s.t.Fatalf("the stream is still open; lines so far %q", out)
		}
	}
}

// errorBody reads the JSON error envelope of a refused open; it fails if the body is not one.
func (s *sseStream) errorBody() string {
	s.t.Helper()
	return strings.Join(s.all(), "\n")
}

// sseData decodes every data line of lines as JSON into maps.
func sseData(t *testing.T, lines []string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range lines {
		d, ok := strings.CutPrefix(l, "data: ")
		if !ok {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("data line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}
