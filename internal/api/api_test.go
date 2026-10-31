package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

const testOrigin = "https://dash.example.com"

var fixedNow = time.Date(2026, 10, 30, 12, 0, 0, 123456789, time.UTC)

type fakeProm struct {
	expr       string
	start, end time.Time
	step       time.Duration
	err        error
}

func (f *fakeProm) Range(_ context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error) {
	f.expr, f.start, f.end, f.step = expr, start, end, step
	return nil, f.err
}

type fakeAM struct {
	spec      upstream.SilenceSpec
	created   int
	expiredID string
	err       error
}

func (f *fakeAM) CreateSilence(_ context.Context, s upstream.SilenceSpec) (string, error) {
	f.spec = s
	f.created++
	return "sil-1234", f.err
}

func (f *fakeAM) ExpireSilence(_ context.Context, id string) error {
	f.expiredID = id
	if f.err != nil {
		return f.err
	}
	if id == "bad" {
		return upstream.ErrInvalid
	}
	return nil
}

type fakeLogs struct {
	lines []model.LogLine
	q     upstream.LogQuery
	err   error
	tail  func(emit func(model.LogLine) error) error
}

func (f *fakeLogs) Query(_ context.Context, q upstream.LogQuery) ([]model.LogLine, error) {
	f.q = q
	return f.lines, f.err
}

func (f *fakeLogs) Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error {
	f.q = q
	if f.tail != nil {
		return f.tail(emit)
	}
	<-ctx.Done()
	return nil
}

type fakeData struct {
	nodes map[string]string
	vmErr error
}

func (f fakeData) PodDetail(_ context.Context, ns, name string) (model.PodDetail, error) {
	if name == "missing" {
		return model.PodDetail{}, topics.ErrNotFound
	}
	return model.PodDetail{}, nil
}

func (f fakeData) NodeIP(name string) (string, bool) {
	ip, ok := f.nodes[name]
	return ip, ok
}

func (f fakeData) VMDetail(_ context.Context, vmid int) (model.VMDetail, error) {
	return model.VMDetail{}, f.vmErr
}

// newServer builds a Server with fakes; mod may adjust the deps before construction.
func newServer(mod func(*Deps)) *Server {
	d := Deps{
		Cfg:     config.Config{PublicURL: testOrigin + "/", HostNodename: "pve-730", Conventions: config.Load(func(string) string { return "" }).Conventions},
		Version: "test",
		Hub:     hub.New(),
		Prom:    &fakeProm{},
		AM:      &fakeAM{},
		VLogs:   &fakeLogs{},
		Data:    fakeData{nodes: map[string]string{"cp1": "10.0.0.5"}},
		Ready:   func() bool { return true },
	}
	if mod != nil {
		mod(&d)
	}
	s := New(d)
	s.now = func() time.Time { return fixedNow }
	return s
}

// req is one test request; Origin/CSRF headers are added for writes unless raw.
type req struct {
	method, path string
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

func TestHealthHandler(t *testing.T) {
	ready := false
	s := newServer(func(d *Deps) { d.Ready = func() bool { return ready } })
	h := s.HealthHandler()
	for _, tc := range []struct {
		path  string
		ready bool
		code  int
	}{
		{"/healthz", false, 200},
		{"/healthz", true, 200},
		{"/readyz", false, 503},
		{"/readyz", true, 200},
		{"/api/me", true, 404},
		{"/", true, 404},
	} {
		ready = tc.ready
		w := req{path: tc.path}.do(h)
		if w.Code != tc.code {
			t.Errorf("%s ready=%v: code = %d, want %d", tc.path, tc.ready, w.Code, tc.code)
		}
	}
}

func TestSeriesArgs(t *testing.T) {
	tests := []struct {
		name  string
		query string
		code  int
		want  string // expected PromQL substring
	}{
		{"bad vmid", "name=vm.cpu&range=1h&vmid=abc", 400, ""},
		{"missing vmid", "name=vm.cpu&range=1h", 400, ""},
		{"unknown name", "name=nope&range=1h", 400, ""},
		{"unknown range", "name=host.cpu&range=2y", 400, ""},
		{"unknown node", "name=node.cpu&range=1h&node=ghost", 404, ""},
		{"missing node", "name=node.cpu&range=1h", 400, ""},
		{"bad pod args", "name=pod.cpu&range=1h&ns=Bad_NS&pod=x", 400, ""},
		{"host ok", "name=host.cpu&range=15m", 200, `nodename="pve-730"`},
		{"vm ok", "name=vm.cpu&range=6h&vmid=100", 200, `id="qemu/100"`},
		{"node ok", "name=node.cpu&range=1h&node=cp1", 200, `instance="10.0.0.5:9100"`},
		{"pod ns alias", "name=pod.mem&range=1h&ns=media&pod=plex-0", 200, `namespace="media",pod="plex-0"`},
		{"pod namespace", "name=pod.mem&range=1h&namespace=media&pod=plex-0", 200, `namespace="media",pod="plex-0"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prom := &fakeProm{}
			s := newServer(func(d *Deps) { d.Prom = prom })
			w := req{path: "/api/series?" + tc.query}.do(s.Handler())
			if w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			if tc.code != 200 {
				if decodeErr(t, w) == "" {
					t.Error("empty error message")
				}
				if prom.expr != "" {
					t.Error("Prom must not be called on bad input")
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
		s := newServer(func(d *Deps) { d.Prom = &fakeProm{err: context.DeadlineExceeded} })
		w := req{path: "/api/series?name=host.cpu&range=1h"}.do(s.Handler())
		if w.Code != 502 {
			t.Fatalf("code = %d, want 502", w.Code)
		}
	})
}

func TestDetailsAndLogs(t *testing.T) {
	logs := &fakeLogs{}
	s := newServer(func(d *Deps) { d.VLogs = logs })
	h := s.Handler()

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/vms/100", 200},
		{"/api/vms/abc", 400},
		{"/api/pods/media/plex-0", 200},
		{"/api/pods/media/missing", 404},
		{"/api/pods/Bad_NS/x", 400},
		{"/api/logs?ns=media&pod=plex-0&since=1h&q=err", 200},
		{"/api/me", 200},
	} {
		if w := (req{path: tc.path}).do(h); w.Code != tc.code {
			t.Errorf("%s: code = %d, want %d", tc.path, w.Code, tc.code)
		}
	}
	if want := (upstream.LogQuery{Namespace: "media", Pod: "plex-0", Text: "err", Since: "1h"}); logs.q != want {
		t.Errorf("log query = %+v, want %+v", logs.q, want)
	}

	t.Run("logs are always an array", func(t *testing.T) {
		w := req{path: "/api/logs?namespace=media"}.do(h)
		if w.Body.String() != "[]\n" {
			t.Errorf("body = %q", w.Body.String())
		}
	})
	t.Run("invalid logs query is 400, upstream failure 502", func(t *testing.T) {
		logs.err = upstream.ErrInvalid
		if w := (req{path: "/api/logs?ns=media"}).do(h); w.Code != 400 {
			t.Errorf("code = %d, want 400", w.Code)
		}
		logs.err = context.DeadlineExceeded
		if w := (req{path: "/api/logs?ns=media"}).do(h); w.Code != 502 {
			t.Errorf("code = %d, want 502", w.Code)
		}
	})
	t.Run("tail validates before streaming", func(t *testing.T) {
		w := req{path: "/api/logs/tail?pod=x"}.do(h) // namespace missing
		if w.Code != 400 || w.Header().Get("Content-Type") != "application/json" {
			t.Errorf("code = %d type = %q", w.Code, w.Header().Get("Content-Type"))
		}
	})
}

func TestUIConfig(t *testing.T) {
	get := func(cfg config.Config) model.UIConfig {
		t.Helper()
		w := req{path: "/api/config"}.do(newServer(func(d *Deps) { d.Cfg = cfg }).Handler())
		var got model.UIConfig
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
			t.Fatalf("code %d, body %q", w.Code, w.Body.String())
		}
		return got
	}
	if got := get(config.Config{PublicURL: testOrigin}); got.HostModel != "" || got.Launcher == nil || len(got.Launcher) != 0 {
		t.Errorf("unconfigured: %+v (launcher must be an empty array)", got)
	}
	cfg := config.Config{PublicURL: testOrigin, HostModel: "Dell R730",
		Launcher: []model.LauncherLink{{Name: "Proxmox", Href: "https://pve.example.com", Icon: "proxmox"}}}
	if got := get(cfg); got.HostModel != "Dell R730" || len(got.Launcher) != 1 || got.Launcher[0].Icon != "proxmox" {
		t.Errorf("configured: %+v", got)
	}
}
