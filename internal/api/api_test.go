package api

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/topics"
)

const testOrigin = "https://dash.example.com"

// testHost is the Host header requests carry unless a test sets another.
const testHost = "dash.example.com"

var fixedNow = time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)

// fakeProm is the svc.Querier of the fake "prometheus" plugin: it records the last range query.
type fakeProm struct {
	expr       string
	start, end time.Time
	step       time.Duration
	err        error
	calls      int
}

func (f *fakeProm) Instant(context.Context, string) ([]svc.Sample, error) { return nil, f.err }

func (f *fakeProm) Range(_ context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error) {
	f.calls++
	f.expr, f.start, f.end, f.step = expr, start, end, step
	return nil, f.err
}

type fakeData struct {
	nodes  map[string]string
	podErr error
}

func (f fakeData) PodDetail(_ context.Context, ns, name string) (model.PodDetail, error) {
	if name == "missing" {
		return model.PodDetail{}, topics.ErrNotFound
	}
	return model.PodDetail{}, f.podErr
}

func (f fakeData) NodeIP(name string) (string, bool) {
	ip, ok := f.nodes[name]
	return ip, ok
}

// promPlugin is a running supervisor with one fake plugin, "prometheus", whose service is q: the
// series handler reads its querier from Plugins.Service("prometheus"). It publishes into a
// recorder, so nothing reaches the hub the test serves. A test that wants no plugins sets
// Deps.Plugins to emptyPlugins(); one that runs plugins of its own sets Deps.Plugins, and
// adds a prometheus fake of its own when it wants series.
func promPlugin(q svc.Querier) PluginsAPI {
	f := plugintest.New("prometheus")
	f.ServiceVal = q
	return runSup(f)
}

// emptyPlugins is a running supervisor with no plugin: /api/v1/plugins answers [].
func emptyPlugins() PluginsAPI { return runSup() }

func runSup(fakes ...*plugintest.Fake) PluginsAPI {
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher: &plugintest.Hub{}, Settings: &plugintest.Settings{}, StatusEvery: time.Hour,
		StopTimeout: time.Second, Log: slog.New(slog.DiscardHandler),
	})
	for _, f := range fakes {
		if err := sup.Add(f.Factory()); err != nil {
			panic(err)
		}
	}
	if err := sup.Run(context.Background()); err != nil { // the supervisor lives as long as the test binary
		panic(err)
	}
	return sup
}

// newServer builds a Server with fakes; mod may adjust the deps before construction.
func newServer(mod func(*Deps)) *Server {
	d := Deps{
		Cfg:     config.Config{PublicURL: testOrigin + "/", HostNodename: "pve-730", Limits: config.Defaults().Limits, Conventions: config.Conventions{ProtectedLabel: protectedLabel}},
		Version: "test",
		Hub:     hub.New(),
		Data:    fakeData{nodes: map[string]string{"cp1": "10.0.0.5"}},
	}
	if mod != nil {
		mod(&d)
	}
	if d.Plugins == nil {
		d.Plugins = promPlugin(&fakeProm{})
	}
	s := New(d)
	s.now = func() time.Time { return fixedNow }
	return s
}

// req is one test request; Origin/CSRF headers are added for writes unless raw.
type req struct {
	method, path string
	host         string // Host header; defaults to testHost
	body         any
	header       map[string]string
	raw          bool // do not add CSRF headers
}

func (r req) do(h http.Handler) *httptest.ResponseRecorder {
	if r.method == "" {
		r.method = http.MethodGet
	}
	var rd *bytes.Reader
	switch b := r.body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case string:
		rd = bytes.NewReader([]byte(b))
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	hr := httptest.NewRequest(r.method, r.path, rd)
	hr.Host = cmp.Or(r.host, testHost)
	hr.Header.Set("X-Forwarded-Preferred-Username", "alice")
	if !r.raw && r.method != http.MethodGet {
		hr.Header.Set("X-Dash-Csrf", "1")
		hr.Header.Set("Origin", testOrigin)
	}
	for k, v := range r.header {
		if v == "" {
			hr.Header.Del(k)
		} else {
			hr.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, hr)
	return w
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q is not an APIError: %v", w.Body.String(), err)
	}
	return e.Error
}

// syncBuf is a goroutine-safe buffer for slog capture and streaming tests.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestStaticSPA(t *testing.T) {
	s := newServer(nil)
	s.static = fstest.MapFS{
		"index.html":      {Data: []byte("<html>spa</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	}
	h := s.Handler()
	tests := []struct {
		path  string
		code  int
		body  string
		cache string
	}{
		{path: "/", code: 200, body: "<html>spa</html>", cache: "no-cache"},
		{path: "/index.html", code: 200, body: "<html>spa</html>", cache: "no-cache"},
		{path: "/cluster/pods/x", code: 200, body: "<html>spa</html>", cache: "no-cache"},
		{path: "/assets/app-1.js", code: 200, body: "console.log(1)", cache: "public, max-age=31536000, immutable"},
		{path: "/assets/missing.js", code: 404},
		{path: "/favicon.svg", code: 200, body: "<svg/>"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			// static paths need no user header
			w := req{path: tc.path, header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d", w.Code, tc.code)
			}
			if tc.body != "" && w.Body.String() != tc.body {
				t.Errorf("body = %q, want %q", w.Body.String(), tc.body)
			}
			if got := w.Header().Get("Cache-Control"); got != tc.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tc.cache)
			}
		})
	}

	t.Run("unknown api path is JSON 404", func(t *testing.T) {
		w := req{path: "/api/nope"}.do(h)
		if w.Code != 404 || decodeErr(t, w) == "" {
			t.Fatalf("code = %d body = %s", w.Code, w.Body)
		}
	})

	t.Run("dist not built", func(t *testing.T) {
		s := newServer(nil)
		s.static = fstest.MapFS{".gitkeep": {}}
		w := req{path: "/anything"}.do(s.Handler())
		if w.Code != 404 {
			t.Fatalf("code = %d, want 404", w.Code)
		}
	})
}

func TestSeriesArgs(t *testing.T) {
	tests := []struct {
		name  string
		query string
		code  int
		want  string // expected PromQL substring
	}{
		// The vm.* series are the proxmox plugin's: without it running they are unknown (400).
		{"vm series without the plugin", "name=vm.cpu&range=6h&vmid=100", 400, ""},
		{"unknown name", "name=nope&range=1h", 400, ""},
		{"unknown range", "name=node.cpu&range=2y&node=cp1", 400, ""},
		{"unknown node", "name=node.cpu&range=1h&node=ghost", 404, ""},
		{"missing node", "name=node.cpu&range=1h", 400, ""},
		{"bad pod args", "name=pod.cpu&range=1h&ns=Bad_NS&pod=x", 400, ""},
		{"host ok", "name=host.cpu&range=15m", 200, `nodename="pve-730"`},
		{"node ok", "name=node.cpu&range=1h&node=cp1", 200, `instance="10.0.0.5:9100"`},
		{"pod ns alias", "name=pod.mem&range=1h&ns=media&pod=plex-0", 200, `namespace="media",pod="plex-0"`},
		{"pod namespace", "name=pod.mem&range=1h&namespace=media&pod=plex-0", 200, `namespace="media",pod="plex-0"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prom := &fakeProm{}
			s := newServer(func(d *Deps) { d.Plugins = promPlugin(prom) })
			w := req{path: "/api/v1/series?" + tc.query}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.code != 200 {
				if decodeErr(t, w) == "" {
					t.Error("empty error message")
				}
				if prom.calls != 0 {
					t.Error("the querier must not be called on bad input")
				}
				return
			}
			if !bytes.Contains([]byte(prom.expr), []byte(tc.want)) {
				t.Errorf("expr %q lacks %q", prom.expr, tc.want)
			}
			var got model.Series
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			window, step, _ := queries.Range(got.Range)
			if !prom.end.Equal(fixedNow) || !prom.start.Equal(fixedNow.Add(-window)) || prom.step != step {
				t.Errorf("range = %v..%v step %v", prom.start, prom.end, prom.step)
			}
			if got.Series == nil || got.StepSeconds != int(step.Seconds()) {
				t.Errorf("series = %+v", got)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(`"series":[]`)) {
				t.Errorf("series must serialise as [], got %s", w.Body)
			}
		})
	}

	t.Run("upstream failure is 502", func(t *testing.T) {
		s := newServer(func(d *Deps) { d.Plugins = promPlugin(&fakeProm{err: context.DeadlineExceeded}) })
		w := req{path: "/api/v1/series?name=node.cpu&range=1h&node=cp1"}.do(s.Handler())
		if w.Code != 502 {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})
}

func TestDetails(t *testing.T) {
	h := newServer(nil).Handler()

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/pods/media/plex-0", 200},
		{"/api/pods/media/missing", 404},
		{"/api/pods/Bad_NS/x", 400},
		{"/api/v1/me", 200},
	} {
		if w := (req{path: tc.path}).do(h); w.Code != tc.code {
			t.Errorf("%s: code = %d, want %d", tc.path, w.Code, tc.code)
		}
	}
}
