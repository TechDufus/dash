package prometheus_test

import (
	"context"
	"encoding/pem"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

const matrix = `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"job":"x"},"values":[[1793396000,"1"],[1793396060,"2"]]}]}}`
const vector = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"x"},"value":[1793396394,"1"]}]}}`

// recorder is an upstream that records the requests it gets and answers an empty-ish vector.
type recorder struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newRecorder(t *testing.T, tls bool) *recorder {
	t.Helper()
	r := &recorder{}
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.mu.Unlock()
		if strings.HasSuffix(req.URL.Path, "/query_range") {
			io.WriteString(w, matrix)
			return
		}
		io.WriteString(w, vector)
	})
	if tls {
		r.Server = httptest.NewUnstartedServer(h)
		r.Server.Config.ErrorLog = log.New(io.Discard, "", 0) // the client without the CA aborts handshakes
		r.Server.StartTLS()
	} else {
		r.Server = httptest.NewServer(h)
	}
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) last(t *testing.T) *http.Request {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		t.Fatal("the upstream got no request")
	}
	return r.reqs[len(r.reqs)-1]
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

// env is a running supervisor with the prometheus plugin.
type env struct {
	sup *plugin.Supervisor
	set *plugintest.Settings
}

func start(t *testing.T, cfg *pluginconf.Prometheus, demo bool) *env {
	t.Helper()
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: prometheus.ID, Type: prometheus.ID, Config: cfg})
	h := hub.New()
	sup := plugin.NewSupervisor(plugin.Options{
		Publisher:   h,
		Settings:    set,
		StatusEvery: 10 * time.Millisecond,
		StopTimeout: time.Second,
		Log:         slog.New(slog.DiscardHandler),
		Demo:        demo,
	})
	if err := sup.Add(prometheus.New); err != nil {
		t.Fatal(err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	if err := sup.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); sup.Shutdown(context.Background()) })
	return &env{sup, set}
}

// querier waits for the running plugin's service.
func (e *env) querier(t *testing.T) svc.Querier {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if s, ok := e.sup.Service(prometheus.ID); ok {
			q, ok := s.(svc.Querier)
			if !ok {
				t.Fatalf("Service is %T, want a svc.Querier", s)
			}
			return q
		}
		if time.Now().After(deadline) {
			state, msg := e.row(t)
			t.Fatalf("the plugin never ran: state %s, %q", state, msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) row(t *testing.T) (state, msg string) {
	t.Helper()
	for _, pi := range e.sup.Infos() {
		if pi.ID == string(prometheus.ID) {
			return string(pi.State), pi.Message
		}
	}
	t.Fatal("prometheus is not registered")
	return
}

// waitState waits for the row to reach want: a Reload is applied asynchronously.
func (e *env) waitState(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, _ := e.row(t)
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want %s", state, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManifest(t *testing.T) {
	m := prometheus.New().Manifest()
	if m.ID != "prometheus" || m.Name != "Prometheus-compatible" || m.Version == "" || m.Description == "" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Topics) != 0 || len(m.Actions) != 0 || len(m.Series) != 0 || len(m.Requires) != 0 || len(m.Uses) != 0 {
		t.Errorf("a source plugin declares no topics, actions, series or dependencies: %+v", m)
	}
	if _, ok := m.Config().(*pluginconf.Prometheus); !ok {
		t.Errorf("Config() is %T", m.Config())
	}
}

func TestConfiguredAndValidate(t *testing.T) {
	p := prometheus.New().(interface {
		plugin.Configured
		plugin.Validator
	})
	if p.Configured(&pluginconf.Prometheus{}) || !p.Configured(&pluginconf.Prometheus{URL: "http://x"}) {
		t.Error("Configured is not URL != \"\"")
	}
	if p.Configured("not a section") {
		t.Error("a foreign config is configured")
	}
	bad := map[string]*pluginconf.Prometheus{
		"basic without password": {Auth: pluginconf.Auth{Basic: pluginconf.BasicAuth{Username: "u"}}},
		"basic and bearer": {Auth: pluginconf.Auth{
			Basic: pluginconf.BasicAuth{Username: "u", Password: secret.New("p", "")}, BearerToken: secret.New("t", "")}},
		"header name":   {Headers: map[string]string{"bad name": "v"}},
		"Authorization": {Headers: map[string]string{"Authorization": "x"}},
		"Proxy-Auth":    {Headers: map[string]string{"proxy-authorization": "x"}},
		"Cookie":        {Headers: map[string]string{"Cookie": "x"}},
		"Host":          {Headers: map[string]string{"host": "x"}},
	}
	for name, c := range bad {
		if err := p.Validate(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := &pluginconf.Prometheus{URL: "http://x", Headers: map[string]string{"X-Scope-OrgID": "tenant"},
		Auth: pluginconf.Auth{BearerToken: secret.New("t", "")}}
	if err := p.Validate(good); err != nil {
		t.Errorf("a valid section: %v", err)
	}
	if err := p.Validate("not a section"); err == nil {
		t.Error("a foreign config validated")
	}
}

func TestStatusIsStartingUntilStarted(t *testing.T) {
	p := prometheus.New()
	if st := p.Status(); st.State != plugin.StateStarting {
		t.Errorf("before Start: %+v", st)
	}
	if s, ok := p.(plugin.Provider); !ok || s.Service() != nil {
		t.Error("a plugin that has not started must export no service")
	}
	up := newRecorder(t, false)
	e := start(t, &pluginconf.Prometheus{URL: up.URL}, false)
	e.querier(t)
	if state, _ := e.row(t); state != "healthy" {
		t.Errorf("state = %s, want healthy", state)
	}
}

func TestDisabledWithoutURLAndUnconfiguredWhenEnabled(t *testing.T) {
	e := start(t, &pluginconf.Prometheus{}, false)
	if state, _ := e.row(t); state != "disabled" {
		t.Errorf("no URL: state = %s, want disabled", state)
	}
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Error("a disabled plugin exports a service")
	}
	yes := true
	e.set.Set(plugin.InstanceConfig{Name: prometheus.ID, Type: prometheus.ID, Enabled: &yes, Config: &pluginconf.Prometheus{}})
	e.sup.Reload()
	e.waitState(t, "unconfigured")
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Error("an unconfigured plugin exports a service")
	}
}

func TestInvalidSectionIsUnconfigured(t *testing.T) {
	up := newRecorder(t, false)
	e := start(t, &pluginconf.Prometheus{URL: up.URL, Headers: map[string]string{"Authorization": "x"}}, false)
	if state, _ := e.row(t); state != "unconfigured" {
		t.Errorf("state = %s, want unconfigured", state)
	}
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Error("an invalid plugin exports a service")
	}
}

// Visibility: rule (a). The plugin has no topic, so AssertTopicsReachOpenStream checks that the
// row turns visible; the second half is what the series route depends on: the service
// appears without a restart.
func TestRowTurningVisibleStartsServing(t *testing.T) {
	up := newRecorder(t, false)
	plugintest.AssertTopicsReachOpenStream(t, prometheus.New,
		plugin.InstanceConfig{Config: &pluginconf.Prometheus{}},
		plugin.InstanceConfig{Config: &pluginconf.Prometheus{URL: up.URL}})

	e := start(t, &pluginconf.Prometheus{}, false)
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Fatal("served while disabled")
	}
	e.set.Set(plugin.InstanceConfig{Name: prometheus.ID, Type: prometheus.ID, Config: &pluginconf.Prometheus{URL: up.URL}})
	e.sup.Reload()
	got, err := e.querier(t).Instant(context.Background(), "up")
	if err != nil || len(got) != 1 || got[0].Labels["job"] != "x" || got[0].Value != 1 {
		t.Fatalf("Instant = %+v, %v", got, err)
	}
	e.set.Set(plugin.InstanceConfig{Name: prometheus.ID, Type: prometheus.ID, Config: &pluginconf.Prometheus{}})
	e.sup.Reload()
	e.waitState(t, "disabled")
	if _, ok := e.sup.Service(prometheus.ID); ok {
		t.Error("still served after the URL was removed")
	}
}

func TestBasicAuth(t *testing.T) {
	up := newRecorder(t, false)
	cfg := &pluginconf.Prometheus{URL: up.URL, Auth: pluginconf.Auth{
		Basic: pluginconf.BasicAuth{Username: "reader", Password: secret.New("s3cret", "")}}}
	if _, err := start(t, cfg, false).querier(t).Instant(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	u, p, ok := up.last(t).BasicAuth()
	if !ok || u != "reader" || p != "s3cret" {
		t.Errorf("basic auth = %q %q %v", u, p, ok)
	}
}

func TestBearerAndHeaders(t *testing.T) {
	up := newRecorder(t, false)
	cfg := &pluginconf.Prometheus{URL: up.URL,
		Auth:    pluginconf.Auth{BearerToken: secret.New("tok", "")},
		Headers: map[string]string{"X-Scope-OrgID": "tenant-1"}}
	if _, err := start(t, cfg, false).querier(t).Range(context.Background(), "up", time.Unix(1793396000, 0), time.Unix(1793396600, 0), time.Minute); err != nil {
		t.Fatal(err)
	}
	r := up.last(t)
	if got := r.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("X-Scope-OrgID"); got != "tenant-1" {
		t.Errorf("X-Scope-OrgID = %q", got)
	}
}

func TestNoCredentialsWhenNoneConfigured(t *testing.T) {
	up := newRecorder(t, false)
	if _, err := start(t, &pluginconf.Prometheus{URL: up.URL}, false).querier(t).Instant(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	if got := up.last(t).Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

// A rotated secret file is applied to the next query of the running instance.
func TestAuthorizePicksUpSecretUpdate(t *testing.T) {
	up := newRecorder(t, false)
	tok := secret.New("one", "file:/run/secrets/token")
	q := start(t, &pluginconf.Prometheus{URL: up.URL, Auth: pluginconf.Auth{BearerToken: tok}}, false).querier(t)
	if _, err := q.Instant(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	if got := up.last(t).Header.Get("Authorization"); got != "Bearer one" {
		t.Fatalf("Authorization = %q", got)
	}
	tok.Update("two")
	if _, err := q.Instant(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	if got := up.last(t).Header.Get("Authorization"); got != "Bearer two" {
		t.Errorf("after rotation: Authorization = %q, want Bearer two", got)
	}
}

func TestCustomCA(t *testing.T) {
	up := newRecorder(t, true)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := start(t, &pluginconf.Prometheus{URL: up.URL}, false).querier(t).Instant(context.Background(), "up")
	if got := upstream.Category(err); got != upstream.CategoryTLS {
		t.Errorf("without the CA: category %q (err %v), want tls", got, err)
	}
	if _, err := start(t, &pluginconf.Prometheus{URL: up.URL, TLS: pluginconf.TLS{CAFile: ca}}, false).querier(t).Instant(context.Background(), "up"); err != nil {
		t.Errorf("with the CA: %v", err)
	}
	if _, err := start(t, &pluginconf.Prometheus{URL: up.URL, TLS: pluginconf.TLS{InsecureSkipVerify: true}}, false).querier(t).Instant(context.Background(), "up"); err != nil {
		t.Errorf("insecureSkipVerify: %v", err)
	}
}

func TestUnreadableCAFileIsAnError(t *testing.T) {
	up := newRecorder(t, false)
	e := start(t, &pluginconf.Prometheus{URL: up.URL, TLS: pluginconf.TLS{CAFile: filepath.Join(t.TempDir(), "missing.pem")}}, false)
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, msg := e.row(t)
		if state == "error" {
			if strings.Contains(msg, up.URL) {
				t.Errorf("the message leaks the URL: %q", msg)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %s, want error", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTimeoutComesFromTheSection(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) }) // runs first: Close waits for the handler
	q := start(t, &pluginconf.Prometheus{URL: slow.URL, Timeout: pluginconf.Duration(60 * time.Millisecond)}, false).querier(t)
	begin := time.Now()
	_, err := q.Instant(context.Background(), "up")
	if got := upstream.Category(err); got != upstream.CategoryTimeout {
		t.Errorf("category %q (err %v), want timeout", got, err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Errorf("the query took %v: the section's timeout was ignored", time.Since(begin))
	}
}

// The client keeps the framework's redirect policy: a redirect to another host is not followed,
// so credentials never leave for it.
func TestRedirectToAnotherHostIsRefused(t *testing.T) {
	other := newRecorder(t, false)
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/v1/query", http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	cfg := &pluginconf.Prometheus{URL: redir.URL, Auth: pluginconf.Auth{BearerToken: secret.New("tok", "")}}
	if _, err := start(t, cfg, false).querier(t).Instant(context.Background(), "up"); err == nil {
		t.Error("the redirect was followed")
	}
	if n := other.count(); n != 0 {
		t.Errorf("the other host got %d requests", n)
	}
}

func TestUpstreamErrorsKeepTheirCategory(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer down.Close()
	_, err := start(t, &pluginconf.Prometheus{URL: down.URL}, false).querier(t).Instant(context.Background(), "up")
	if got := upstream.Category(err); got != upstream.CategoryAuth {
		t.Errorf("category %q, want auth", got)
	}
}
