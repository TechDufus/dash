package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
)

// auditClock is a settable clock for the audit log's records and denial rate limit.
type auditClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *auditClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *auditClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newAuditClock() *auditClock { return &auditClock{t: fixedNow} }

func openAudit(t *testing.T, dir string, now func() time.Time) *audit.Log {
	t.Helper()
	l, err := audit.Open(dir, audit.Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// captureSlog is captureAudit without the time attribute, so output can be compared as bytes.
func captureSlog(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// legacyAudit is the helper this package had before the audit log existed (actions.go).
func legacyAudit(user, action, kind, ns, name string, err error) {
	result, msg := "ok", ""
	if err != nil {
		result, msg = "error", err.Error()
	}
	slog.Info("audit", "user", user, "action", action, "kind", kind,
		"namespace", ns, "name", name, "result", result, "error", msg)
}

func auditRecords(t *testing.T, dir string) []audit.Record {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []audit.Record
	for line := range bytes.SplitSeq(bytes.TrimSpace(b), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// actionCase is one state-changing request together with the audit target it must record.
type actionCase struct {
	name, path, method string
	body               any
	action, kind, ns   string
	target             string // target name of the outcome
}

func actionCases() []actionCase {
	return []actionCase{
		{"restart", "/api/actions/restart", "POST", model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}, "restart", "Deployment", "apps", "web"},
		{"reconcile", "/api/actions/reconcile", "POST", model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "app"}, "reconcile", "Kustomization", "flux-system", "app"},
		{"silence create", "/api/silences", "POST", silenceBody(nil), "silence.create", "Silence", "", "sil-1234"},
		{"silence expire", "/api/silences/sil-1234", "DELETE", nil, "silence.expire", "Silence", "", "sil-1234"},
	}
}

// serverWithFakes returns a server whose Kubernetes, Flux and Alertmanager clients record
// every call, so a test can assert none happened.
func serverWithFakes(mod func(*Deps)) (*Server, *fake.Clientset, *dynfake.FakeDynamicClient, *fakeAM) {
	kc := fake.NewClientset(workloadObjects()...)
	dyn := dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), nil)
	am := &fakeAM{}
	s := newServer(func(d *Deps) {
		d.Kube, d.Dyn, d.AM = kc, dyn, am
		if mod != nil {
			mod(d)
		}
	})
	return s, kc, dyn, am
}

func TestActionsWriteIntentThenOutcome(t *testing.T) {
	for _, mode := range []struct {
		name      string
		dev, demo bool
	}{{"proxy", false, false}, {"dev", true, false}, {"demo", false, true}} {
		t.Run(mode.name, func(t *testing.T) {
			dir := t.TempDir()
			al := openAudit(t, dir, newAuditClock().now)
			s, _, _, _ := serverWithFakes(func(d *Deps) {
				d.Audit = al
				d.Cfg.Dev, d.Cfg.Demo = mode.dev, mode.demo
			})
			for _, tc := range actionCases() {
				req{method: tc.method, path: tc.path, body: tc.body}.do(s.Handler())
			}

			recs := auditRecords(t, dir)
			if len(recs) != 2*len(actionCases()) {
				t.Fatalf("records = %d, want %d: %+v", len(recs), 2*len(actionCases()), recs)
			}
			for i, tc := range actionCases() {
				in, out := recs[2*i], recs[2*i+1]
				if in.Kind != audit.KindIntent || out.Kind != audit.KindOutcome || in.ID == "" || in.ID != out.ID {
					t.Fatalf("%s: kinds %s/%s ids %q/%q", tc.name, in.Kind, out.Kind, in.ID, out.ID)
				}
				if in.Action != tc.action || out.Action != tc.action {
					t.Errorf("%s: actions %q/%q", tc.name, in.Action, out.Action)
				}
				if in.Target.Kind != tc.kind || in.Target.Namespace != tc.ns || out.Target.Kind != tc.kind || out.Target.Namespace != tc.ns {
					t.Errorf("%s: targets %+v/%+v", tc.name, in.Target, out.Target)
				}
				if out.Target.Name != tc.target {
					t.Errorf("%s: outcome target name %q, want %q", tc.name, out.Target.Name, tc.target)
				}
				wantActor := audit.Actor{Type: mode.name, User: "alice", Surface: "ui"}
				for _, r := range []audit.Record{in, out} {
					if r.Actor != wantActor || r.AuthMode != mode.name || r.Peer != "192.0.2.1" {
						t.Errorf("%s: actor %+v mode %q peer %q", tc.name, r.Actor, r.AuthMode, r.Peer)
					}
				}
				if in.Result != "" {
					t.Errorf("%s: intent carries result %q", tc.name, in.Result)
				}
			}
			// restart and the silences succeed; the missing Flux object fails.
			for i, want := range []string{audit.ResultOK, audit.ResultError, audit.ResultOK, audit.ResultOK} {
				if got := recs[2*i+1].Result; got != want {
					t.Errorf("%s: result %q, want %q (%s)", actionCases()[i].name, got, want, recs[2*i+1].Error)
				}
			}
			if rep, err := audit.Verify(dir); err != nil || !rep.OK() || len(rep.Dangling) != 0 {
				t.Errorf("Verify = %+v, %v", rep, err)
			}
		})
	}
}

// The structured "audit" slog record is what log dashboards read; the audit log must emit it
// exactly as the old helper did, for successes and failures, with and without a file.
func TestAuditSlogMatchesLegacyHelper(t *testing.T) {
	for _, withFile := range []bool{true, false} {
		name := map[bool]string{true: "with file", false: "slog only"}[withFile]
		t.Run(name, func(t *testing.T) {
			dir := ""
			if withFile {
				dir = t.TempDir()
			}
			al := openAudit(t, dir, nil)
			got := captureSlog(t)
			s, _, _, am := serverWithFakes(func(d *Deps) { d.Audit = al })
			h := s.Handler()
			boom := errors.New("boom")

			want := &syncBuf{}
			replay := func(f func()) {
				cur := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(want, &slog.HandlerOptions{
					ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
						if len(groups) == 0 && a.Key == slog.TimeKey {
							return slog.Attr{}
						}
						return a
					},
				})))
				f()
				slog.SetDefault(cur)
			}

			do := func(r req) *httptest.ResponseRecorder { return r.do(h) }
			do(req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "apps", Name: "web"}})
			replay(func() { legacyAudit("alice", "restart", "Deployment", "apps", "web", nil) })

			do(req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "ghost", Name: "web"}})
			replay(func() {
				legacyAudit("alice", "restart", "Deployment", "ghost", "web", errors.New(`namespaces "ghost" not found`))
			})

			do(req{method: "POST", path: "/api/actions/restart", body: model.RestartRequest{Kind: "Deployment", Namespace: "platform", Name: "web"}})
			replay(func() {
				legacyAudit("alice", "restart", "Deployment", "platform", "web", errors.New("type the workload name to confirm"))
			})

			w := do(req{method: "POST", path: "/api/actions/reconcile", body: model.ReconcileRequest{Kind: "Kustomization", Namespace: "flux-system", Name: "ghost"}})
			replay(func() {
				legacyAudit("alice", "reconcile", "Kustomization", "flux-system", "ghost", errors.New(decodeErr(t, w)))
			})

			do(req{method: "POST", path: "/api/silences", body: silenceBody(nil)})
			replay(func() { legacyAudit("alice", "silence.create", "Silence", "", "sil-1234", nil) })

			am.err = boom
			do(req{method: "POST", path: "/api/silences", body: silenceBody(nil)})
			replay(func() { legacyAudit("alice", "silence.create", "Silence", "", "HighTemp", boom) })

			do(req{method: "DELETE", path: "/api/silences/abc"})
			replay(func() { legacyAudit("alice", "silence.expire", "Silence", "", "abc", boom) })

			am.err = nil
			do(req{method: "DELETE", path: "/api/silences/abc"})
			replay(func() { legacyAudit("alice", "silence.expire", "Silence", "", "abc", nil) })

			// Other records (the WARN naming an upstream failure) are not the audit record's business.
			var gotAudit bytes.Buffer
			for line := range bytes.SplitSeq([]byte(got.String()), []byte("\n")) {
				if bytes.Contains(line, []byte(`"msg":"audit"`)) {
					gotAudit.Write(line)
					gotAudit.WriteByte('\n')
				}
			}
			if gotAudit.String() != want.String() {
				t.Errorf("slog output differs from the old helper\n got: %s\nwant: %s", &gotAudit, want)
			}
		})
	}
}

// When the audit file cannot be written the action must not happen: nothing changes without
// a durable record of the intent.
func TestIntentWriteFailureBlocksTheAction(t *testing.T) {
	for _, tc := range actionCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			// MaxSize 1 makes the next write rotate the file, and rotating inside a directory
			// that no longer exists fails: a write error on a writable data dir.
			al, err := audit.Open(dir, audit.Options{MaxSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { al.Close() })
			al.Event(audit.Record{Action: "seed"})
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}

			s, kc, dyn, am := serverWithFakes(func(d *Deps) { d.Audit = al })
			w := req{method: tc.method, path: tc.path, body: tc.body}.do(s.Handler())
			if w.Code != http.StatusServiceUnavailable || decodeErr(t, w) != "audit log unavailable" {
				t.Fatalf("code = %d body = %s, want 503 audit log unavailable", w.Code, w.Body)
			}
			if n := len(kc.Actions()) + len(dyn.Actions()); n != 0 || am.created != 0 || am.expiredID != "" {
				t.Errorf("upstream called: kube %d, flux %d, silences created %d, expired %q", len(kc.Actions()), len(dyn.Actions()), am.created, am.expiredID)
			}
		})
	}
}

func TestDenialsAreAuditedOncePerPeerAndMinute(t *testing.T) {
	csrfFail := req{method: "POST", path: "/api/actions/restart", header: map[string]string{"X-Dash-Csrf": ""}}
	noUser := req{path: "/api/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}
	capped := func(s *Server) { s.events, s.tails = newStreamLimiter(0, 10), newStreamLimiter(0, 10) }

	for _, tc := range []struct {
		name   string
		req    req
		reason string
		code   int
		mod    func(*Server)
	}{
		{"csrf", csrfFail, "csrf", 403, nil},
		{"unauthenticated", noUser, "unauthenticated", 401, nil},
		{"event stream cap", req{path: "/api/stream"}, "stream_limit", 429, capped},
		{"log tail cap", req{path: "/api/logs/tail?namespace=apps&pod=web"}, "stream_limit", 429, capped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clk := newAuditClock()
			s, _, _, _ := serverWithFakes(func(d *Deps) { d.Audit = openAudit(t, dir, clk.now) })
			if tc.mod != nil {
				tc.mod(s)
			}
			h := s.Handler()

			if w := tc.req.do(h); w.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", w.Code, tc.code, w.Body)
			}
			recs := auditRecords(t, dir)
			if len(recs) != 1 {
				t.Fatalf("records after one denial = %d: %+v", len(recs), recs)
			}
			r := recs[0]
			if r.Kind != audit.KindDenial || r.Reason != tc.reason || r.Peer != "192.0.2.1" || r.AuthMode != "proxy" {
				t.Errorf("denial = %+v", r)
			}

			tc.req.do(h) // same peer, same reason, same minute
			if n := len(auditRecords(t, dir)); n != 1 {
				t.Fatalf("a repeat within a minute wrote a record: %d", n)
			}
			clk.advance(time.Minute + time.Second)
			tc.req.do(h)
			if n := len(auditRecords(t, dir)); n != 2 {
				t.Errorf("a denial after the window left %d records, want 2", n)
			}
		})
	}
}

// A path the client chooses must not let one denial write an unbounded record.
func TestDenialRecordCapsTheRequestLine(t *testing.T) {
	dir := t.TempDir()
	s, _, _, _ := serverWithFakes(func(d *Deps) { d.Audit = openAudit(t, dir, newAuditClock().now) })
	long := "/api/" + string(bytes.Repeat([]byte("a"), 5000))
	req{method: "POST", path: long, header: map[string]string{"X-Dash-Csrf": ""}}.do(s.Handler())
	recs := auditRecords(t, dir)
	if len(recs) != 1 || len(recs[0].Action) != maxAuditAction {
		t.Fatalf("records = %+v", recs)
	}
}
