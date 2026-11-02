package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

func TestRequestIDIsGeneratedForEveryResponse(t *testing.T) {
	h := newServer(nil).Handler()
	seen := map[string]bool{}
	for _, r := range []req{{path: "/api/v1/me"}, {path: "/api/v1/me"}, {path: "/api/nope"}, {path: "/api/v1/me", host: "evil.example"}, {path: "/"}} {
		id := r.do(h).Header().Get("X-Request-Id")
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) || seen[id] {
			t.Errorf("%s (host %q): id %q is not a fresh 128-bit hex value", r.path, r.host, id)
		}
		seen[id] = true
	}
}

// A proxy's id is believed from a loopback peer only (the trusted-proxy default) and only when
// it is a short token; anything else is replaced.
func TestRequestIDHonouredOnlyFromLoopbackAndValidated(t *testing.T) {
	h := newServer(nil).Handler()
	do := func(remote, in string) string {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.Host, r.RemoteAddr = testHost, remote
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		if in != "" {
			r.Header.Set("X-Request-Id", in)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Header().Get("X-Request-Id")
	}
	for _, tc := range []struct {
		name, remote, in string
		kept             bool
	}{
		{"loopback v4", "127.0.0.1:4000", "edge-7f3a.01_ok", true},
		{"loopback v6", "[::1]:4000", "abc", true},
		{"the longest accepted", "127.0.0.1:4000", strings.Repeat("a", 64), true},
		{"another peer", "192.0.2.7:4000", "edge-7f3a", false},
		{"too long", "127.0.0.1:4000", strings.Repeat("a", 65), false},
		{"a space", "127.0.0.1:4000", "has space", false},
		{"a newline-ish character", "127.0.0.1:4000", "id;rm", false},
		{"non-ASCII", "127.0.0.1:4000", "idé", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := do(tc.remote, tc.in)
			if tc.kept && got != tc.in {
				t.Errorf("id = %q, want the proxy's %q", got, tc.in)
			}
			if !tc.kept && (got == tc.in || len(got) != 32) {
				t.Errorf("id = %q, want a generated one instead of %q", got, tc.in)
			}
		})
	}
	// The id of a request carried by a loopback peer reaches its error body.
	r := httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	r.Host, r.RemoteAddr = testHost, "127.0.0.1:1"
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")
	r.Header.Set("X-Request-Id", "from-the-proxy")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if e := apiError(t, w); e.RequestID != "from-the-proxy" {
		t.Errorf("error requestId = %q", e.RequestID)
	}
}

// The id is in the header, the error body, the audit record and the log line of the failure.
func TestRequestIDRoundTrip(t *testing.T) {
	logs := captureSlog(t)
	dir := t.TempDir()
	clk := newAuditClock()
	pl := newHarness(t)
	pl.act = func(string, any) (any, error) {
		return nil, errors.New(`Post "http://alertmanager.internal:9093/api/v2/silences": connection refused`)
	}
	s, _, _ := serverWithFakes(func(d *Deps) { pl.attach(d); d.Audit = openAudit(t, dir, clk.now) })
	h := s.Handler()

	// An upstream failure: header, body and the WARN line name the same id, and only the log
	// names the upstream.
	w := req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.create", body: silenceIn{Alertname: "HighTemp"}}.do(h)
	e := wantError(t, w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable)
	id := w.Header().Get("X-Request-Id")
	if id == "" || e.RequestID != id {
		t.Fatalf("requestId %q, header %q", e.RequestID, id)
	}
	if !strings.Contains(logs.String(), `"requestId":"`+id+`"`) || strings.Contains(w.Body.String(), "alertmanager.internal") {
		t.Errorf("log %s\nbody %s", logs, w.Body)
	}

	// The audit records of that action and of a refusal carry it too.
	recs := auditRecords(t, dir)
	if len(recs) != 2 || recs[0].RequestID != id || recs[1].RequestID != id {
		t.Errorf("records of the action = %+v, want requestId %q", recs, id)
	}
	w = req{method: "POST", path: "/api/v1/plugins/aa/actions/silence.create", header: map[string]string{"X-Dash-Csrf": ""}}.do(h)
	denied := w.Header().Get("X-Request-Id")
	last := auditRecords(t, dir)
	if got := last[len(last)-1]; got.Kind != audit.KindDenial || got.RequestID != denied || denied == "" || denied == id {
		t.Errorf("denial record = %+v, want requestId %q", got, denied)
	}
}

func cacheControl(t *testing.T, h http.Handler, r req) string {
	t.Helper()
	return r.do(h).Header().Get("Cache-Control")
}

// L.8: /api/v1 answers are no-store, a topic snapshot is private and revalidated, a stream is
// no-cache, and the legacy routes are untouched.
func TestCacheControlNoStoreOnV1AndPrivateNoCacheOnSnapshots(t *testing.T) {
	s := newServer(nil)
	if _, err := s.d.Hub.Publish("cluster", map[string]string{"a": "1"}, []model.SourceStatus{}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	for _, path := range []string{"/api/v1/me", "/api/v1/plugins", "/api/v1/series?name=sr.cpu&range=1h", "/api/v1/topics/missing", "/api/v1/nope", "/api/v1/series?range=bad"} {
		if got := cacheControl(t, h, req{path: path}); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
	// Errors too, whatever the path under /api/v1.
	if got := cacheControl(t, h, req{method: "POST", path: "/api/v1/plugins/ghost/actions/x"}); got != "no-store" {
		t.Errorf("a refused action: Cache-Control = %q", got)
	}

	w := req{path: "/api/v1/topics/cluster"}.do(h)
	etag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, no-cache" || etag == "" {
		t.Fatalf("snapshot = %d, Cache-Control %q, ETag %q", w.Code, w.Header().Get("Cache-Control"), etag)
	}
	w = req{path: "/api/v1/topics/cluster", header: map[string]string{"If-None-Match": etag}}.do(h)
	if w.Code != http.StatusNotModified || w.Header().Get("Cache-Control") != "private, no-cache" {
		t.Errorf("revalidation = %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}

	resp, _ := sseClient(t, h, "/api/v1/stream")
	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("stream: Cache-Control = %q, want no-cache", got)
	}

	// What is not under /api/v1 keeps what it had: no header of its own.
	for _, path := range []string{"/api/me", "/api/nope"} {
		if got := cacheControl(t, h, req{path: path}); got != "" {
			t.Errorf("legacy %s: Cache-Control = %q, want none", path, got)
		}
	}
}

func TestMeCapabilitiesAndScopes(t *testing.T) {
	t.Run("no plugins", func(t *testing.T) {
		w := req{path: "/api/v1/me"}.do(newServer(nil).Handler())
		var me model.Me
		decodeJSON(t, w, &me)
		want := model.Me{User: "alice", Role: "admin", AuthMode: "proxy", Audience: "api", Version: "test", Scopes: []string{"read", "logs"}}
		if !equalMe(me, want) {
			t.Errorf("me = %+v, want %+v", me, want)
		}
	})
	t.Run("dev mode", func(t *testing.T) {
		s := newServer(func(d *Deps) { d.Cfg = config.Config{Dev: true, PublicURL: testOrigin} })
		var me model.Me
		decodeJSON(t, req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(s.Handler()), &me)
		if me.User != "dev" || me.AuthMode != "dev" {
			t.Errorf("me = %+v", me)
		}
	})
	t.Run("a plugin with mounted actions", func(t *testing.T) {
		e := newActionEnv(t, nil)
		var me model.Me
		decodeJSON(t, req{path: "/api/v1/me"}.do(e.Handler()), &me)
		want := model.Me{User: "alice", Role: "admin", AuthMode: "proxy", Audience: "api", Version: "test",
			Scopes: []string{"read", "logs", "actions:aa"}, Capabilities: model.MeCapabilities{Actions: true}}
		if !equalMe(me, want) {
			t.Errorf("me = %+v, want %+v", me, want)
		}
	})
	t.Run("a plugin whose actions are not enabled", func(t *testing.T) {
		e := newActionEnvWith(t, nil, nil)
		var me model.Me
		decodeJSON(t, req{path: "/api/v1/me"}.do(e.Handler()), &me)
		if me.Capabilities.Actions || len(me.Scopes) != 2 {
			t.Errorf("me = %+v", me)
		}
	})
}

func equalMe(a, b model.Me) bool {
	return a.User == b.User && a.Role == b.Role && a.AuthMode == b.AuthMode && a.Audience == b.Audience &&
		a.Version == b.Version && a.Capabilities == b.Capabilities && a.UI == b.UI && strings.Join(a.Scopes, ",") == strings.Join(b.Scopes, ",")
}
