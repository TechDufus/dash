package api

// The victorialogs plugin through the real chain (request id, host check, CSRF, auth, mux, the
// core's route guard, the supervisor's router) against a fake VictoriaLogs: the namespace policy,
// the audit of a tail open, the secret canary, the field mapping and the routes appearing
// without a restart. The plugin's own logic (resume, query parameters) is tested in its package.

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/victorialogs"
	"github.com/TechDufus/dash/internal/secret"
)

type vlFactory struct{}

func (vlFactory) Factory() plugin.Factory { return victorialogs.New }

const (
	vlLogsPath = "/api/v1/plugins/victorialogs/logs"
	vlTailPath = "/api/v1/plugins/victorialogs/logs/tail"
	vlRecord1  = `{"_msg":"first","_time":"2026-10-30T21:39:16.5Z","kubernetes.pod_name":"p1","kubernetes.container_name":"c1","kubernetes.pod_namespace":"media"}`
	vlRecord2  = `{"_msg":"second","_time":"2026-10-30T21:39:17.5Z","kubernetes.pod_name":"p1","kubernetes.container_name":"c1","kubernetes.pod_namespace":"media"}`
)

// fakeVLogs is the VictoriaLogs a test plugin talks to: newest first on a query, in order on a
// tail, which then stays open until the client leaves.
type fakeVLogs struct {
	*httptest.Server
	mu      sync.Mutex
	records []string // what a query answers, newest first, and a tail sends in this order
	status  int      // a non-zero status answers every request with it
	queries []url.Values
	auths   []string // the Authorization header of every request
	tails   int
}

func newFakeVLogs(t *testing.T, records ...string) *fakeVLogs {
	return startFakeVLogs(t, false, records)
}

// startFakeVLogs serves over TLS (a certificate nobody trusts but the test's CA file) when tls.
func startFakeVLogs(t *testing.T, tls bool, records []string) *fakeVLogs {
	t.Helper()
	f := &fakeVLogs{records: records}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query())
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		status, recs := f.status, append([]string(nil), f.records...)
		if r.URL.Path == "/select/logsql/tail" {
			f.tails++
		}
		f.mu.Unlock()
		if status != 0 {
			http.Error(w, "no internal-host.example.com here", status)
			return
		}
		for _, rec := range recs {
			fmt.Fprintln(w, rec)
		}
		if r.URL.Path == "/select/logsql/tail" {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}
	})
	if tls {
		f.Server = httptest.NewUnstartedServer(h)
		f.Server.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshakes of clients that refuse it
		f.Server.StartTLS()
	} else {
		f.Server = httptest.NewServer(h)
	}
	t.Cleanup(f.Close)
	return f
}

// lastAuth is the Authorization header of the newest request.
func (f *fakeVLogs) lastAuth() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auths) == 0 {
		return "<no request>"
	}
	return f.auths[len(f.auths)-1]
}

// last is the parameters of the newest request.
func (f *fakeVLogs) last() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		return nil
	}
	return f.queries[len(f.queries)-1]
}

// vlEnv is the plugin running on a supervisor behind the API.
type vlEnv struct {
	*harness
	up *fakeVLogs
}

// newVLEnv starts the plugin with settings from mod on top of the defaults, pointed at up unless
// mod sets a URL. Secrets known to reg are scrubbed by Host.ScrubLog.
func newVLEnv(t *testing.T, up *fakeVLogs, reg *secret.Registry, mod func(*pluginconf.VictoriaLogs, *plugin.InstanceConfig)) *vlEnv {
	t.Helper()
	cfg := pluginconf.DefaultVictoriaLogs()
	cfg.URL = up.URL
	ic := plugin.InstanceConfig{Name: "victorialogs"}
	if mod != nil {
		mod(&cfg, &ic)
	}
	ic.Config = &cfg
	var opt func(*plugin.Options)
	if reg != nil {
		opt = func(o *plugin.Options) { o.ScrubLog = reg.ScrubLogLine }
	}
	env := newPluginEnvOpt(t, opt, []plugin.InstanceConfig{ic}, vlFactory{})
	env.Server.d.Metrics = obs.New("test", RoutePatterns(), env.sup.Infos)
	return &vlEnv{harness: &harness{actionEnv: &actionEnv{pluginEnv: env}}, up: up}
}

func (e *vlEnv) waitRunning(t *testing.T) {
	t.Helper()
	e.eventually(t, "victorialogs to run", func() bool { return e.sup.Running("victorialogs") })
}

func TestVictoriaLogsQueryThroughTheAPI(t *testing.T) {
	up := newFakeVLogs(t, vlRecord2, vlRecord1)
	e := newVLEnv(t, up, nil, nil)
	e.waitRunning(t)

	w := e.get(vlLogsPath + "?ns=media&pod=p1&since=1h&q=err")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET logs = %d %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var lines []model.LogLine
	if err := json.Unmarshal(w.Body.Bytes(), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Msg != "first" || lines[1].Msg != "second" || lines[0].Pod != "p1" || lines[0].Container != "c1" {
		t.Errorf("lines = %+v, want first then second", lines)
	}
	q := up.last()
	if want := `_time:1h kubernetes.pod_namespace:="media" kubernetes.pod_name:="p1" "err" | sort by (_time desc)`; q.Get("query") != want || q.Get("limit") != "500" {
		t.Errorf("upstream request = %v, want query %s and limit 500", q, want)
	}

	t.Run("always an array", func(t *testing.T) {
		up.mu.Lock()
		up.records = nil
		up.mu.Unlock()
		if w := e.get(vlLogsPath + "?namespace=media"); w.Code != 200 || w.Body.String() != "[]\n" {
			t.Errorf("empty result = %d %q", w.Code, w.Body)
		}
	})
	t.Run("an invalid query is 400", func(t *testing.T) {
		w := e.get(vlLogsPath + "?ns=Media")
		var body model.APIError
		if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusBadRequest || err != nil || body.Code != model.ErrCodeInvalidInput {
			t.Errorf("bad namespace = %d %s", w.Code, w.Body)
		}
		if w := e.get(vlLogsPath); w.Code != http.StatusBadRequest { // the namespace is missing
			t.Errorf("no namespace = %d", w.Code)
		}
	})
	t.Run("an upstream failure is a 502 with no host name in it", func(t *testing.T) {
		up.mu.Lock()
		up.status = http.StatusInternalServerError
		up.mu.Unlock()
		w := e.get(vlLogsPath + "?ns=media")
		if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "internal-host.example.com") || strings.Contains(w.Body.String(), up.URL) {
			t.Errorf("upstream 500 = %d %s", w.Code, w.Body)
		}
		var body model.APIError
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Code != model.ErrCodeUpstreamUnavailable || body.RequestID == "" {
			t.Errorf("envelope = %+v (%v)", body, err)
		}
	})
}

func TestVictoriaLogsCustomFieldsReachTheUpstream(t *testing.T) {
	rec := `{"_msg":"hello","_time":"2026-10-30T21:39:17.5Z","k8s.ns":"media","k8s.pod":"web-1","ctr":"main","node":"n1"}`
	up := newFakeVLogs(t, rec)
	e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, _ *plugin.InstanceConfig) {
		c.Fields = pluginconf.LogFields{Namespace: "k8s.ns", Pod: "k8s.pod", Container: "ctr"}
		c.Limit = 25
	})
	e.waitRunning(t)

	w := e.get(vlLogsPath + "?ns=media&pod=web-1&container=main")
	if w.Code != 200 {
		t.Fatalf("code = %d %s", w.Code, w.Body)
	}
	q := up.last()
	if want := `_time:15m k8s.ns:="media" k8s.pod:="web-1" ctr:="main" | sort by (_time desc)`; q.Get("query") != want || q.Get("limit") != "25" {
		t.Errorf("upstream request = %v, want query %s and limit 25", q, want)
	}
	var lines []model.LogLine
	if err := json.Unmarshal(w.Body.Bytes(), &lines); err != nil || len(lines) != 1 || lines[0].Pod != "web-1" || lines[0].Container != "main" || lines[0].Msg != "hello node=n1" {
		t.Errorf("lines = %+v (%v)", lines, err)
	}

	s := e.openStream(t, vlTailPath+"?ns=media&pod=web-1&container=main", "alice", nil)
	s.expect("retry: 3000")
	s.expectContaining(`"pod":"web-1"`)
	if want := `k8s.ns:="media" k8s.pod:="web-1" ctr:="main"`; up.last().Get("query") != want {
		t.Errorf("tail query = %s, want %s", up.last().Get("query"), want)
	}
}

// A query for a denied namespace and a tail for it both answer 403, and the denial is audited
// once per peer and minute. The tail is refused before it takes a slot, so a full limiter does
// not turn it into a 429.
func TestVictoriaLogsDeniedNamespaceIs403OnBothRoutes(t *testing.T) {
	up := newFakeVLogs(t, vlRecord1)
	e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, ic *plugin.InstanceConfig) {
		c.DenyNamespaces = []string{"kube-system"}
		ic.ReadDenyNamespaces = c.ReadDenyConfig() // what cmd/dash does from the section
	})
	e.waitRunning(t)
	e.tails = newStreamLimiter(0, 0) // every slot taken, for everybody

	w := req{path: vlLogsPath + "?ns=kube-system&pod=coredns", peer: "[::1]:41000"}.do(e.Handler())
	var body model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusForbidden || err != nil || body.Code != model.ErrCodeForbidden {
		t.Fatalf("denied query = %d %s", w.Code, w.Body)
	}
	for i := range 2 {
		if s := e.openStream(t, vlTailPath+"?namespace=kube-system&pod=coredns", "alice", nil); s.status() != http.StatusForbidden {
			t.Fatalf("denied tail #%d = %d, want 403 (not the 429 of the full limiter)", i, s.status())
		}
	}
	up.mu.Lock()
	n := len(up.queries)
	up.mu.Unlock()
	if n != 0 {
		t.Errorf("the upstream saw %d requests for a denied namespace", n)
	}
	// One record per peer and minute: the query came from the IPv6 loopback, the tails from the
	// IPv4 loopback client, and the second tail is the same peer in the same minute.
	recs := e.audit(t)
	if len(recs) != 2 {
		t.Fatalf("records = %+v, want one denial per peer", recs)
	}
	var lines []string
	for _, r := range recs {
		if r.Kind != audit.KindDenial || r.Reason != obs.AuthNamespaceDenied.String() {
			t.Errorf("record = %+v, want a namespace_denied denial", r)
		}
		if r.Plugin != "victorialogs" {
			t.Errorf("record plugin = %q, want victorialogs", r.Plugin)
		}
		lines = append(lines, r.Action)
	}
	// The whole request line, not the path left after the router cut the plugin's prefix.
	slices.Sort(lines)
	if want := []string{"GET " + vlLogsPath, "GET " + vlTailPath}; !slices.Equal(lines, want) {
		t.Errorf("record actions = %q, want %q", lines, want)
	}
	if v, _ := seriesValue(t, e.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "namespace_denied"}); v != 3 {
		t.Errorf("namespace_denied counted %v, want 3 (the log keeps one a minute, the metric every refusal)", v)
	}
	if v, _ := seriesValue(t, e.scrape(t), "dash_auth_failures_total", map[string]string{"reason": "stream_limit"}); v != 0 {
		t.Errorf("stream_limit counted %v, the denial must come before the slot", v)
	}
	e.clk.advance(time.Minute + time.Second)
	e.openStream(t, vlTailPath+"?namespace=kube-system&pod=coredns", "alice", nil)
	if n := len(e.audit(t)); n != 3 {
		t.Errorf("records after the window = %d, want 3", n)
	}

	// An allowed namespace is served by the query and meets the full limiter on the tail.
	if w := e.get(vlLogsPath + "?ns=media"); w.Code != 200 {
		t.Errorf("allowed namespace = %d", w.Code)
	}
	if got := e.openStream(t, vlTailPath+"?ns=media&pod=p1", "alice", nil).status(); got != http.StatusTooManyRequests {
		t.Errorf("allowed tail with no slot = %d, want 429", got)
	}
}

func TestVictoriaLogsTailOpenIsAuditedAsALogsTail(t *testing.T) {
	up := newFakeVLogs(t, vlRecord1)
	e := newVLEnv(t, up, nil, nil)
	e.waitRunning(t)

	s := e.openStream(t, vlTailPath+"?ns=media&pod=p1&container=c1", "alice", map[string]string{"X-Request-Id": "tail-1"})
	s.expect("retry: 3000")
	s.expect("event: line")
	s.expectContaining(`"msg":"first"`)

	recs := e.audit(t)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want exactly one for the open", recs)
	}
	got := recs[0]
	if got.Kind != audit.KindEvent || got.Action != "logs.tail" || got.Plugin != "victorialogs" ||
		got.Target != (audit.Target{Kind: "Pod", Namespace: "media", Name: "p1"}) || got.RequestID != "tail-1" || got.Actor.User != "alice" {
		t.Errorf("record = %+v", got)
	}
	if v, ok := seriesValue(t, e.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); !ok || v != 1 {
		t.Errorf("open tails = %v (present %v), want 1", v, ok)
	}

	// A reconnect asks the upstream to replay from its resume point.
	id := "c1=" + time.Date(2026, 10, 30, 21, 39, 16, 0, time.UTC).Format(time.RFC3339Nano)
	s2 := e.openStream(t, vlTailPath+"?ns=media&pod=p1&container=c1", "alice", map[string]string{"Last-Event-ID": id})
	s2.expect("retry: 3000")
	e.eventually(t, "the second tail to reach the upstream", func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.tails == 2
	})
	if so := up.last().Get("start_offset"); so != "60s" { // the id is older than the one-minute cap
		t.Errorf("a reconnect sent start_offset %q: %v", so, up.last())
	}
}

// Log lines are application output: whatever a pod prints, a credential never reaches the
// browser, even one with the characters json.Marshal escapes. Host.ScrubLog scrubs the text
// before the payload is encoded.
func TestVictoriaLogsScrubSecretsOnQueryAndTail(t *testing.T) {
	const canary = `s3&cr"et<token>`
	reg := secret.NewRegistry()
	reg.Add(secret.New(canary, ""))
	rec := func(msg string) string {
		b, _ := json.Marshal(map[string]string{"_msg": msg, "_time": "2026-10-30T21:39:17.5Z", "kubernetes.pod_name": "p1", "kubernetes.container_name": "c1"})
		return string(b)
	}
	up := newFakeVLogs(t, rec("token="+canary+" end"), rec("setup dash_setup_Zm9vYmFyYmF6 end"), rec("plain line stays"))
	e := newVLEnv(t, up, reg, nil)
	e.waitRunning(t)

	forbidden := []string{canary, `s3\u0026cr\"et\u003ctoken\u003e`, "dash_setup_Zm9v"}
	check := func(t *testing.T, where, out string) {
		t.Helper()
		for _, f := range forbidden {
			if strings.Contains(out, f) {
				t.Errorf("%s carries %q:\n%s", where, f, out)
			}
		}
		if !strings.Contains(out, "plain line stays") || strings.Count(out, "[redacted]") < 2 {
			t.Errorf("%s: want the secret lines redacted and the plain one kept:\n%s", where, out)
		}
	}
	w := e.get(vlLogsPath + "?ns=media&pod=p1")
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	check(t, "logs", w.Body.String())

	s := e.openStream(t, vlTailPath+"?ns=media&pod=p1", "alice", nil)
	var out strings.Builder
	deadline := time.After(3 * time.Second)
	for seen := 0; seen < 3; {
		select {
		case l, ok := <-s.lines:
			if !ok {
				t.Fatal("tail ended early")
			}
			out.WriteString(l + "\n")
			if strings.HasPrefix(l, "data: ") {
				seen++
			}
		case <-deadline:
			t.Fatalf("only %d tail lines arrived:\n%s", seen, out.String())
		}
	}
	check(t, "tail", out.String())
}

// The routes exist while the instance runs: 404 while it is off, 200 after enabling it, with no
// restart, and the row says why.
func TestVictoriaLogsRoutesAppearWhenTheURLIsSet(t *testing.T) {
	up := newFakeVLogs(t, vlRecord1)
	e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, _ *plugin.InstanceConfig) { c.URL = "" })

	e.eventually(t, "the row to read disabled", func() bool { return e.infos(t)["victorialogs"].State == model.PluginDisabled })
	if w := e.get(vlLogsPath + "?ns=media"); w.Code != http.StatusNotFound {
		t.Fatalf("GET logs while off = %d %s, want 404", w.Code, w.Body)
	}
	if s := e.openStream(t, vlTailPath+"?ns=media&pod=p1", "alice", nil); s.status() != http.StatusNotFound {
		t.Fatalf("GET logs/tail while off = %d, want 404", s.status())
	}

	cfg := pluginconf.DefaultVictoriaLogs()
	cfg.URL = up.URL
	e.set.Set(plugin.InstanceConfig{Name: "victorialogs", Config: &cfg})
	e.sup.Reload()
	e.waitRunning(t)
	e.eventually(t, "the logs route", func() bool { return e.get(vlLogsPath+"?ns=media").Code == http.StatusOK })
	if st := e.infos(t)["victorialogs"].State; st != model.PluginHealthy {
		t.Errorf("state = %s, want healthy", st)
	}
}

// A bad tail query is 400 before the core takes a slot or writes the audit record, as it is on
// GET logs: with every slot taken it still answers 400, not 429, and nothing is audited or
// counted as a refusal.
func TestVictoriaLogsBadTailQueryIs400BeforeSlotAndAudit(t *testing.T) {
	up := newFakeVLogs(t, vlRecord1)
	e := newVLEnv(t, up, nil, nil)
	e.waitRunning(t)
	e.tails = newStreamLimiter(0, 0) // every slot taken, for everybody

	for _, path := range []string{
		vlTailPath + "?pod=p1",                   // no namespace
		vlTailPath + "?ns=Media&pod=p1",          // not a namespace name
		vlTailPath + "?ns=media&pod=p1&since=2d", // not a window
	} {
		w := e.get(path)
		var body model.APIError
		if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != http.StatusBadRequest || err != nil || body.Code != model.ErrCodeInvalidInput {
			t.Errorf("GET %s = %d %s, want 400 invalid_input", path, w.Code, w.Body)
		}
		if s := e.openStream(t, path, "alice", nil); s.status() != http.StatusBadRequest {
			t.Errorf("tail %s = %d, want 400 (not the 429 of the full limiter)", path, s.status())
		}
	}
	if recs := e.audit(t); len(recs) != 0 {
		t.Errorf("records = %+v, want none: a refused query is not an open", recs)
	}
	for _, reason := range []string{"stream_limit", "namespace_denied"} {
		if v, _ := seriesValue(t, e.scrape(t), "dash_auth_failures_total", map[string]string{"reason": reason}); v != 0 {
			t.Errorf("%s counted %v, a bad query is no refusal of the policy", reason, v)
		}
	}
	if v, _ := seriesValue(t, e.scrape(t), "dash_sse_streams", map[string]string{"kind": obs.StreamTail}); v != 0 {
		t.Errorf("open tails = %v, want 0", v)
	}
	up.mu.Lock()
	n := len(up.queries)
	up.mu.Unlock()
	if n != 0 {
		t.Errorf("the upstream saw %d requests for bad queries", n)
	}

	// A good query meets the full limiter: validation is not what refused the others.
	if got := e.openStream(t, vlTailPath+"?ns=media&pod=p1", "alice", nil).status(); got != http.StatusTooManyRequests {
		t.Errorf("good tail with no slot = %d, want 429", got)
	}
}

// plugins.victorialogs.limit is the limit of the upstream query and the most lines the answer
// holds, whatever the upstream sends: the newest ones stay.
func TestVictoriaLogsLimitBoundsTheQueryAndTheAnswer(t *testing.T) {
	oldest := `{"_msg":"zeroth","_time":"2026-10-30T21:39:15.5Z","kubernetes.pod_name":"p1","kubernetes.container_name":"c1"}`
	up := newFakeVLogs(t, vlRecord2, vlRecord1, oldest) // the fake answers newest first and ignores the limit
	e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, _ *plugin.InstanceConfig) { c.Limit = 2 })
	e.waitRunning(t)

	w := e.get(vlLogsPath + "?ns=media&pod=p1")
	var lines []model.LogLine
	if err := json.Unmarshal(w.Body.Bytes(), &lines); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET logs = %d %s (%v)", w.Code, w.Body, err)
	}
	if len(lines) != 2 || lines[0].Msg != "first" || lines[1].Msg != "second" {
		t.Errorf("lines = %+v, want the two newest, oldest first", lines)
	}
	if got := up.last().Get("limit"); got != "2" {
		t.Errorf("upstream limit = %q, want 2", got)
	}
}

// plugins.victorialogs.auth reaches every request of the plugin, queries and tails alike.
func TestVictoriaLogsAuthReachesTheUpstream(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth pluginconf.Auth
		want string
	}{
		{"basic", pluginconf.Auth{Basic: pluginconf.BasicAuth{Username: "dash", Password: secret.New("s3cret", "")}}, "Basic " + base64.StdEncoding.EncodeToString([]byte("dash:s3cret"))},
		{"bearer", pluginconf.Auth{BearerToken: secret.New("tok-123", "")}, "Bearer tok-123"},
		{"none", pluginconf.Auth{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := newFakeVLogs(t, vlRecord1)
			e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, _ *plugin.InstanceConfig) { c.Auth = tc.auth })
			e.waitRunning(t)

			if w := e.get(vlLogsPath + "?ns=media&pod=p1"); w.Code != http.StatusOK {
				t.Fatalf("GET logs = %d %s", w.Code, w.Body)
			}
			if got := up.lastAuth(); got != tc.want {
				t.Errorf("query Authorization = %q, want %q", got, tc.want)
			}
			s := e.openStream(t, vlTailPath+"?ns=media&pod=p1", "alice", nil)
			s.expectContaining(`"msg":"first"`)
			if got := up.lastAuth(); got != tc.want {
				t.Errorf("tail Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

// plugins.victorialogs.tls reaches the client: a certificate nobody trusts is refused, and
// caFile or insecureSkipVerify accepts it.
func TestVictoriaLogsTLSReachesTheUpstream(t *testing.T) {
	up := startFakeVLogs(t, true, []string{vlRecord1})
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: up.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tls  pluginconf.TLS
		want int
	}{
		{"unknown authority", pluginconf.TLS{}, http.StatusBadGateway},
		{"caFile", pluginconf.TLS{CAFile: caFile}, http.StatusOK},
		{"insecureSkipVerify", pluginconf.TLS{InsecureSkipVerify: true}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t, secret.NewRegistry()) // the refused handshake's detail goes to the log
			e := newVLEnv(t, up, nil, func(c *pluginconf.VictoriaLogs, _ *plugin.InstanceConfig) { c.TLS = tc.tls })
			e.waitRunning(t)
			w := e.get(vlLogsPath + "?ns=media&pod=p1")
			if w.Code != tc.want {
				t.Fatalf("GET logs = %d %s, want %d", w.Code, w.Body, tc.want)
			}
			if tc.want != http.StatusOK && !strings.Contains(w.Body.String(), "tls") {
				t.Errorf("body %s: want the category tls", w.Body)
			}
		})
	}
}
