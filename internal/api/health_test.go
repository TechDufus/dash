package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// healthServer is a Server whose readiness comes from a real, running plugin.Supervisor.
// The supervisor stops with the test. Unless a test sets Settings, every plugin is enabled.
func healthServer(t *testing.T, mod func(*plugin.Options), set *plugintest.Settings, fs ...plugin.Factory) (*Server, *plugin.Supervisor) {
	t.Helper()
	if set == nil {
		set = &plugintest.Settings{}
	}
	o := plugin.Options{
		Publisher: &plugintest.Hub{}, Settings: set, StatusEvery: 5 * time.Millisecond,
		StopTimeout: time.Second, Log: slog.New(slog.DiscardHandler),
	}
	if mod != nil {
		mod(&o)
	}
	sup := plugin.NewSupervisor(o)
	if err := sup.Add(fs...); err != nil {
		t.Fatalf("Add: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return newServer(func(d *Deps) { d.Health = sup }), sup
}

// readyz asks the health handler for /readyz from the TCP peer remote.
func readyz(h http.Handler, query, remote string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/readyz"+query, nil)
	r.RemoteAddr = remote
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const (
	loopbackPeer4 = "127.0.0.1:41000"
	loopbackPeer6 = "[::1]:41000"
	kubeletPeer   = "192.0.2.7:41000"
)

func TestProbeBodiesUnchanged(t *testing.T) {
	s := newServer(nil)
	h := s.HealthHandler()

	w := readyz(h, "", kubeletPeer)
	if w.Code != 503 || w.Body.String() != "not ready" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("before serving: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}
	s.MarkServing()
	w = readyz(h, "", kubeletPeer)
	if w.Code != 200 || w.Body.String() != "ok" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("serving: %d %q %q", w.Code, w.Body, w.Header().Get("Content-Type"))
	}

	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "ok" {
		t.Errorf("/healthz: %d %q", w.Code, w.Body)
	}
}

func TestHealthHandlerServesOnlyProbes(t *testing.T) {
	h := newServer(nil).HealthHandler()
	for _, path := range []string{"/api/v1/me", "/", "/metrics"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("%s: code = %d, want 404", path, w.Code)
		}
	}
}

func TestNotReadyBeforeServing(t *testing.T) {
	s, sup := healthServer(t, nil, nil, plugintest.New("aa").Factory())
	h := s.HealthHandler()
	if !sup.Ready() {
		t.Fatal("the supervisor itself is not ready; the test would prove nothing")
	}
	if w := readyz(h, "", kubeletPeer); w.Code != 503 || w.Body.String() != "not ready" {
		t.Errorf("plugins ready, listeners not bound: %d %q", w.Code, w.Body)
	}
	s.MarkServing()
	if w := readyz(h, "", kubeletPeer); w.Code != 200 {
		t.Errorf("after MarkServing: %d %q", w.Code, w.Body)
	}
}

func TestNotReadyWhilePluginsHaveNotStarted(t *testing.T) {
	// A supervisor that never ran has not returned from any Start.
	sup := plugin.NewSupervisor(plugin.Options{})
	s := newServer(func(d *Deps) { d.Health = sup })
	s.MarkServing()
	if w := readyz(s.HealthHandler(), "", kubeletPeer); w.Code != 503 {
		t.Errorf("code = %d, want 503 before the supervisor ran", w.Code)
	}
}

func TestReadyWithAPluginInError(t *testing.T) {
	broken := plugintest.New("broken")
	broken.StartFn = func(context.Context, plugin.Host, any) error { return errors.New("no") }
	s, sup := healthServer(t, nil, nil,
		broken.Factory(),
		plugintest.New("down", plugintest.Status(plugin.StateError, "upstream refused")).Factory(),
		plugintest.New("fine").Factory(),
	)
	s.MarkServing()
	if w := readyz(s.HealthHandler(), "", kubeletPeer); w.Code != 200 || w.Body.String() != "ok" {
		t.Errorf("a failing plugin made dash unready: %d %q (infos %+v)", w.Code, w.Body, sup.Infos())
	}
}

func TestReadyRequiresWaitsThenReadyWithWarning(t *testing.T) {
	var logs syncBuf
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); defer mu.Unlock(); now = now.Add(d) }

	neverSyncs := plugintest.New("kubernetes", plugintest.Status(plugin.StateStarting, ""))
	s, _ := healthServer(t, func(o *plugin.Options) {
		o.ReadyRequires = []plugin.ID{"kubernetes"}
		o.ReadyTimeout = time.Minute
		o.Wall = clock
		o.Log = slog.New(slog.NewTextHandler(&logs, nil))
	}, nil, neverSyncs.Factory(), plugintest.New("other").Factory())
	s.MarkServing()
	h := s.HealthHandler()

	if w := readyz(h, "", kubeletPeer); w.Code != 503 {
		t.Fatalf("ready while a required plugin is still starting: %d", w.Code)
	}
	advance(59 * time.Second)
	if w := readyz(h, "", kubeletPeer); w.Code != 503 {
		t.Fatalf("ready before readyTimeout: %d", w.Code)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("warned before the timeout:\n%s", logs.String())
	}
	advance(2 * time.Second)
	if w := readyz(h, "", kubeletPeer); w.Code != 200 {
		t.Fatalf("not ready after readyTimeout: %d", w.Code)
	}
	readyz(h, "", kubeletPeer)
	out := logs.String()
	if n := strings.Count(out, "level=WARN"); n != 1 || !strings.Contains(out, "plugins=[kubernetes]") {
		t.Errorf("want one WARN naming kubernetes, got %d:\n%s", n, out)
	}
}

func TestReadyRequiresReleasesWhenThePluginSettles(t *testing.T) {
	var mu sync.Mutex
	state := plugin.StateStarting
	syncing := plugintest.New("kubernetes")
	syncing.StatusFn = func() plugin.Status { mu.Lock(); defer mu.Unlock(); return plugin.Status{State: state} }
	s, _ := healthServer(t, func(o *plugin.Options) {
		o.ReadyRequires = []plugin.ID{"kubernetes"}
		o.ReadyTimeout = time.Hour
	}, nil, syncing.Factory())
	s.MarkServing()
	h := s.HealthHandler()

	if w := readyz(h, "", kubeletPeer); w.Code != 503 {
		t.Fatalf("ready before the caches synced: %d", w.Code)
	}
	mu.Lock()
	state = plugin.StateHealthy
	mu.Unlock()
	if w := readyz(h, "", kubeletPeer); w.Code != 200 {
		t.Fatalf("not ready after the caches synced: %d", w.Code)
	}
}

func TestReadyRequiresSettlesOnDisabledPlugin(t *testing.T) {
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "kubernetes", Enabled: new(false)})
	s, sup := healthServer(t, func(o *plugin.Options) {
		o.ReadyRequires = []plugin.ID{"kubernetes"}
		o.ReadyTimeout = time.Hour
	}, set, plugintest.New("kubernetes", plugintest.Status(plugin.StateStarting, "")).Factory())
	s.MarkServing()
	if w := readyz(s.HealthHandler(), "", kubeletPeer); w.Code != 200 {
		t.Errorf("a disabled required plugin held readiness back: %d (infos %+v)", w.Code, sup.Infos())
	}
}

func TestVerboseOnlyForLoopbackPeers(t *testing.T) {
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "off", Enabled: new(false)})
	s, _ := healthServer(t, nil, set,
		plugintest.New("aa").Factory(),
		plugintest.New("bb", plugintest.Status(plugin.StateError, "http_503\nsecond line")).Factory(),
		plugintest.New("off").Factory(),
	)
	h := s.HealthHandler()

	const want = "ok\naa healthy\nbb error http_503 second line\noff disabled"
	s.MarkServing()
	for _, peer := range []string{loopbackPeer4, loopbackPeer6, "127.8.9.10:1"} {
		if w := readyz(h, "?verbose=1", peer); w.Code != 200 || w.Body.String() != want || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("peer %s: %d %q, want %q", peer, w.Code, w.Body, want)
		}
	}

	// Every other peer gets the plain body, with no plugin name in it. Headers that claim a
	// loopback origin change nothing: only the TCP peer counts.
	for _, peer := range []string{kubeletPeer, "10.0.0.5:1", "[2001:db8::1]:1", "not-an-address"} {
		w := readyz(h, "?verbose=1", peer, "X-Forwarded-For", "127.0.0.1", "X-Real-Ip", "127.0.0.1", "Forwarded", "for=127.0.0.1")
		if w.Code != 200 || w.Body.String() != "ok" {
			t.Errorf("peer %s: %d %q, want the plain %q", peer, w.Code, w.Body, "ok")
		}
	}

	// Only verbose=1 asks for the report.
	for _, q := range []string{"", "?verbose=0", "?verbose=true", "?verbose="} {
		if w := readyz(h, q, loopbackPeer4); w.Body.String() != "ok" {
			t.Errorf("query %q: %q, want the plain body", q, w.Body)
		}
	}
}

func TestVerboseReportsAnUnreadyDash(t *testing.T) {
	s, _ := healthServer(t, nil, nil, plugintest.New("aa").Factory())
	// not serving yet
	w := readyz(s.HealthHandler(), "?verbose=1", loopbackPeer4)
	if w.Code != 503 || w.Body.String() != "not ready\naa healthy" {
		t.Errorf("%d %q", w.Code, w.Body)
	}
	if w := readyz(s.HealthHandler(), "?verbose=1", kubeletPeer); w.Body.String() != "not ready" {
		t.Errorf("a remote peer saw %q", w.Body)
	}
}
