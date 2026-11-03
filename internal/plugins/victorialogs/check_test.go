package victorialogs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// The read the check must send, below checkPrefix, a path prefix of the configured URL.
const (
	checkPrefix = "/vlogs"
	checkPath   = "/health"
	checkQuery  = ""
)

// checkConfig is a section for the upstream at url (with checkPrefix) and the credential token.
func checkConfig(url, token string) *pluginconf.VictoriaLogs {
	return &pluginconf.VictoriaLogs{
		URL:   url + checkPrefix + "/",
		Auth:  pluginconf.Auth{BearerToken: secret.New(token, "")},
		TLS:   pluginconf.TLS{InsecureSkipVerify: true},
		Limit: 100,
	}
}

// checkAuth is the Authorization header the upstream must see for token.
func checkAuth(token string) string { return "Bearer " + token }

// notConfigured are configurations the check must answer with not_configured, sending nothing.
var notConfigured = []any{
	nil, "victorialogs", (*pluginconf.VictoriaLogs)(nil), pluginconf.VictoriaLogs{URL: "http://192.0.2.1"},
	&pluginconf.Prometheus{URL: "http://192.0.2.1"}, &pluginconf.VictoriaLogs{},
}

// checkLatency is what every reading of checkEnv's clock advances it by: the latency of one read.
const checkLatency = 250 * time.Millisecond

// checkEnv is a plugintest.CheckEnv whose clock steps by checkLatency at each reading.
func checkEnv(rt http.RoundTripper) *plugintest.CheckEnv {
	var mu sync.Mutex
	t := time.Unix(1793396000, 0)
	return &plugintest.CheckEnv{Transport: rt, Clock: func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now := t
		t = t.Add(checkLatency)
		return now
	}}
}

// checkUpstream answers every request with code and body, recording what it received.
type checkUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newCheckUpstream(t *testing.T, code int, body string) *checkUpstream {
	u := &checkUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.reqs = append(u.reqs, r.Clone(context.Background()))
		u.mu.Unlock()
		w.WriteHeader(code)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *checkUpstream) requests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.reqs...)
}

// errTransport fails every request with err.
type errTransport struct{ err error }

func (e errTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func only(t *testing.T, r plugin.CheckReport) plugin.CheckResult {
	t.Helper()
	if len(r.Checks) != 1 {
		t.Fatalf("report = %+v, want one result", r)
	}
	return r.Checks[0]
}

func TestCheckSendsOneReadAndReturnsCategories(t *testing.T) {
	for code, want := range map[int]string{200: "", 401: "auth", 403: "auth", 404: "http_404", 503: "http_503"} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			up := newCheckUpstream(t, code, `OK`)
			env := checkEnv(nil)
			got := only(t, (&Plugin{}).Check(context.Background(), checkConfig(up.URL, "tok"), env))
			wantRes := plugin.CheckResult{Name: checkName, OK: want == "", Category: want,
				Message: plugin.CheckMessage(want), LatencyMs: int(checkLatency.Milliseconds())}
			if got != wantRes {
				t.Errorf("result = %+v, want %+v", got, wantRes)
			}
			reqs := up.requests()
			if len(reqs) != 1 {
				t.Fatalf("upstream saw %d requests, want 1", len(reqs))
			}
			r := reqs[0]
			if r.Method != http.MethodGet || r.URL.Path != checkPrefix+checkPath || r.URL.RawQuery != checkQuery {
				t.Errorf("read = %s %s, want GET %s%s?%s", r.Method, r.URL, checkPrefix, checkPath, checkQuery)
			}
			if got := r.Header.Get("Authorization"); got != checkAuth("tok") {
				t.Errorf("Authorization = %q, want %q", got, checkAuth("tok"))
			}
			cfgs := env.Configs()
			if len(cfgs) != 1 || !cfgs[0].InsecureSkipVerify || cfgs[0].FromUI {
				t.Errorf("client configs = %+v, want one with the section's TLS settings and FromUI unset", cfgs)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		up := newCheckUpstream(t, 200, "")
		up.Close()
		got := only(t, (&Plugin{}).Check(context.Background(), checkConfig(up.URL, "tok"), checkEnv(nil)))
		if got.OK || got.Category != "unreachable" || got.Message != plugin.CheckMessage("unreachable") {
			t.Errorf("result = %+v, want unreachable", got)
		}
	})

	t.Run("blocked address", func(t *testing.T) {
		env := checkEnv(errTransport{fmt.Errorf("dial refused: %w", plugin.ErrBlockedAddress)})
		got := only(t, (&Plugin{}).Check(context.Background(), checkConfig("http://192.0.2.1", "tok"), env))
		if got.OK || got.Category != "unreachable" || got.Message != "blocked address" {
			t.Errorf("result = %+v, want unreachable with the message \"blocked address\"", got)
		}
	})

	t.Run("wrong configuration", func(t *testing.T) {
		for _, cfg := range notConfigured {
			env := checkEnv(errTransport{errors.New("no request may be sent")})
			got := only(t, (&Plugin{}).Check(context.Background(), cfg, env))
			if got.OK || got.Category != "not_configured" || got.Message != plugin.CheckMessage("not_configured") {
				t.Errorf("Check(%T) = %+v, want not_configured", cfg, got)
			}
			if n := len(env.Configs()); n != 0 {
				t.Errorf("Check(%T) built %d clients, want none", cfg, n)
			}
		}
	})
}

func TestCheckNeverReportsUpstreamURLsOrBodies(t *testing.T) {
	const canary = "canary-7f3a9c"
	for _, code := range []int{200, 401, 500} {
		up := newCheckUpstream(t, code, canary+" body "+canary)
		cfg := checkConfig(up.URL, canary)
		cfg.URL = up.URL + "/" + canary
		got := (&Plugin{}).Check(context.Background(), cfg, checkEnv(nil))
		host := strings.TrimPrefix(up.URL, "http://")
		if s := fmt.Sprintf("%+v", got); strings.Contains(s, canary) || strings.Contains(s, host) {
			t.Errorf("HTTP %d: report %s carries the URL, a header or the body", code, s)
		}
		if reqs := up.requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") != checkAuth(canary) {
			t.Errorf("HTTP %d: the upstream did not see the one canary read", code)
		}
	}
	cfg := checkConfig("http://"+canary+".example", canary)
	got := (&Plugin{}).Check(context.Background(), cfg, checkEnv(errTransport{errors.New("dial " + canary + ": refused")}))
	if s := fmt.Sprintf("%+v", got); strings.Contains(s, canary) {
		t.Errorf("transport error: report %s carries the error's text", s)
	}
}

func TestCheckTimesOutAt10s(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	var deadline time.Time
	env := checkEnv(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, _ = r.Context().Deadline()
		return http.DefaultTransport.RoundTrip(r)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	got := only(t, (&Plugin{}).Check(ctx, checkConfig(srv.URL, "tok"), env))
	if got.OK || got.Category != "timeout" || got.Message != plugin.CheckMessage("timeout") {
		t.Errorf("result = %+v, want timeout", got)
	}
	if cfgs := env.Configs(); len(cfgs) != 1 || cfgs[0].Timeout != plugin.CheckTimeout {
		t.Errorf("client configs = %+v, want one with Timeout %v", cfgs, plugin.CheckTimeout)
	}
	if want, _ := ctx.Deadline(); !deadline.Equal(want) {
		t.Errorf("the read's deadline is %v, want the context's %v", deadline, want)
	}
}
