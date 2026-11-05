package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

// cutSecret is a registered secret the cut tests place across a cut.
const cutSecret = "client-secret-canary-55aa-0123456789"

func cutRegistry() *secret.Registry {
	reg := secret.NewRegistry()
	reg.Add(secret.New(cutSecret, ""))
	return reg
}

// straddlers are what a cut must not leave half of: a registered secret and a token shape.
var straddlers = map[string]string{"registered secret": cutSecret, "token": "dash_pat_AbC123-xyz_0123456789"}

// noHalf fails when got keeps the first 8 bytes of span: the start of a secret cut in half.
func noHalf(t *testing.T, what, got, span string) {
	t.Helper()
	if strings.Contains(got, span[:8]) {
		t.Errorf("%s keeps the start of a cut secret: %q", what, got)
	}
}

// clip never returns invalid UTF-8, whether or not it cuts, and still cuts to its bound
// without splitting a character: a stream target comes from the client's URL and is written
// into the audit record as is.
func TestClipReturnsValidUTF8(t *testing.T) {
	s := newServer(func(d *Deps) { d.Secrets = cutRegistry() })
	for name, tc := range map[string]struct {
		in   string
		n    int
		want string
	}{
		"plain, short":            {"media", 8, "media"},
		"invalid byte, not cut":   {"me\xffdia", 16, "me\uFFFDdia"},
		"invalid byte, then cut":  {"\xffabcdefghijkl", 8, "\uFFFDabcde"},
		"cut inside a character":  {"aéééé", 4, "aé"},
		"truncated rune at end":   {"abc\xc3", 16, "abc\uFFFD"},
		"invalid byte at the cut": {"abcdefg\xffhij", 8, "abcdefg"},
	} {
		got := s.clip(tc.in, tc.n)
		if !utf8.ValidString(got) || got != tc.want {
			t.Errorf("%s: clip(%q, %d) = %q, want %q", name, tc.in, tc.n, got, tc.want)
		}
	}
}

// A refused request's line is cut to maxAuditAction before the audit log scrubs it: a secret that
// straddles the cut is redacted whole, plain text is cut as before, and the log's scrubber only
// ever sees the cut text.
func TestRefusedRequestLineKeepsNoPartOfASecret(t *testing.T) {
	const prefix = "POST /api/"
	for name, span := range straddlers {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := newServer(func(d *Deps) { d.Audit = openAudit(t, dir, newAuditClock().now); d.Secrets = cutRegistry() })
			pad := strings.Repeat("a", maxAuditAction-len(prefix)-8)
			req{method: "POST", path: "/api/" + pad + span + "/more", header: map[string]string{"X-Dash-Csrf": ""}}.do(s.Handler())
			recs := auditRecords(t, dir)
			if len(recs) != 1 || recs[0].Action != prefix+pad+"[redacted]" {
				t.Fatalf("records = %+v, want the action %q", recs, prefix+pad+"[redacted]")
			}
			noHalf(t, "the action", recs[0].Action, span)
		})
	}
	t.Run("plain", func(t *testing.T) {
		dir := t.TempDir()
		var mu sync.Mutex
		longest := 0
		reg := cutRegistry()
		al, err := audit.Open(dir, audit.Options{Now: newAuditClock().now, Scrub: func(s string) string {
			mu.Lock()
			longest = max(longest, len(s))
			mu.Unlock()
			return reg.ScrubLogLine(s)
		}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { al.Close() })
		s := newServer(func(d *Deps) { d.Audit = al; d.Secrets = reg })
		long := "/api/" + strings.Repeat("a/dash_pat_b/", 5000)
		req{method: "POST", path: long, header: map[string]string{"X-Dash-Csrf": ""}}.do(s.Handler())
		recs := auditRecords(t, dir)
		if len(recs) != 1 || !strings.HasPrefix(recs[0].Action, prefix+"a/[redacted]/a/") {
			t.Fatalf("records = %+v", recs)
		}
		mu.Lock()
		defer mu.Unlock()
		if longest > maxAuditAction+len("[redacted]") {
			t.Errorf("the audit scrubber was handed %d bytes, want at most the cut", longest)
		}

		dir = t.TempDir()
		s = newServer(func(d *Deps) { d.Audit = openAudit(t, dir, newAuditClock().now); d.Secrets = reg })
		long = "/api/" + strings.Repeat("é", 3000)
		req{method: "POST", path: long, header: map[string]string{"X-Dash-Csrf": ""}}.do(s.Handler())
		if recs := auditRecords(t, dir); len(recs) != 1 || recs[0].Action != strings.ToValidUTF8(("POST " + long)[:maxAuditAction], "\uFFFD") {
			t.Errorf("plain line recorded as %+v, want the first %d bytes", recs, maxAuditAction)
		}
	})
}

// A refused plugin route's line (denyRoute) is cut the same way.
func TestDeniedPluginRouteLineKeepsNoPartOfASecret(t *testing.T) {
	const prefix = "GET /api/v1/plugins/rt/ns/"
	for name, span := range straddlers {
		t.Run(name, func(t *testing.T) {
			ns := strings.Repeat("n", maxAuditAction-len(prefix)-8) + span
			h := newHarness(t, harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{ns}}))
			h.d.Secrets = cutRegistry()
			if w := h.get("/api/v1/plugins/rt/ns/" + ns + "/items"); w.Code != http.StatusForbidden {
				t.Fatalf("denied namespace = %d, want 403", w.Code)
			}
			recs := h.audit(t)
			want := prefix + ns[:maxAuditAction-len(prefix)-8] + "[redacted]"
			if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Action != want {
				t.Fatalf("records = %+v, want one denial %q", recs, want)
			}
			noHalf(t, "the action", recs[0].Action, span)
		})
	}
	t.Run("plain", func(t *testing.T) {
		ns := strings.Repeat("n", 300)
		h := newHarness(t, harnessRT(plugin.InstanceConfig{ReadDenyNamespaces: []string{ns}}))
		h.d.Secrets = cutRegistry()
		h.get("/api/v1/plugins/rt/ns/" + ns + "/items")
		if recs := h.audit(t); len(recs) != 1 || recs[0].Action != (prefix + ns)[:maxAuditAction] {
			t.Errorf("records = %+v", recs)
		}
	})
}

// A stream's audit target comes from the client: each text is cut to maxAuditTarget without
// leaving half of a secret.
func TestStreamOpenTargetKeepsNoPartOfASecret(t *testing.T) {
	h := newHarness(t)
	h.d.Secrets = cutRegistry()
	h.tails = newStreamLimiter(1000, 1000)
	h.tail = beginOnly
	pad := strings.Repeat("p", maxAuditTarget-8)
	users := map[string]string{"registered secret": "alice", "token": "bob"}
	for name, span := range straddlers {
		if w := h.getAs("/api/v1/plugins/rt/tail?ns=media&pod="+url.QueryEscape(pad+span), users[name], nil); w.Code != http.StatusOK {
			t.Fatalf("%s: open = %d", name, w.Code)
		}
	}
	if w := h.getAs("/api/v1/plugins/rt/tail?ns=media&pod="+strings.Repeat("q", 300), "carol", nil); w.Code != http.StatusOK {
		t.Fatalf("plain open = %d", w.Code)
	}
	opens := recordsOf(h.audit(t), audit.KindEvent)
	if len(opens) != 3 {
		t.Fatalf("open records = %+v", opens)
	}
	for _, r := range opens {
		want := pad + "[redacted]"
		if r.Actor.User == "carol" {
			want = strings.Repeat("q", maxAuditTarget)
		}
		if r.Target.Name != want {
			t.Errorf("%s's target name = %q, want %q", r.Actor.User, r.Target.Name, want)
		}
		for _, span := range straddlers {
			noHalf(t, "the target", r.Target.Name, span)
		}
	}
}

// An attempted login name is cut to maxAuditName without leaving half of a secret pasted into it.
func TestAttemptedLoginNameKeepsNoPartOfASecret(t *testing.T) {
	pad := strings.Repeat("n", maxAuditName-8)
	for name, span := range straddlers {
		t.Run(name, func(t *testing.T) {
			e := newLocalEnv(t, localOpts{storedAs: localAdmin, secrets: cutRegistry()})
			wantCode(t, e.login(pad+span, localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
			got := e.records(auditLoginFailed)
			if len(got) != 1 || got[0].Target.Name != pad+"[redacted]" {
				t.Fatalf("records = %+v, want the name %q", got, pad+"[redacted]")
			}
			noHalf(t, "the name", got[0].Target.Name, span)
		})
	}
	t.Run("plain", func(t *testing.T) {
		e := newLocalEnv(t, localOpts{storedAs: localAdmin, secrets: cutRegistry()})
		long := strings.Repeat("n", 100) + strings.Repeat("é", 200)
		wantCode(t, e.login(long, localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
		if got := e.records(auditLoginFailed); len(got) != 1 || got[0].Target.Name != long[:maxAuditName] {
			t.Errorf("records = %+v, want the first %d bytes", got, maxAuditName)
		}
	})
}

// The Host of a request for an unknown host is logged cut to 100 bytes, without half a secret.
func TestUnknownHostWarningKeepsNoPartOfASecret(t *testing.T) {
	warned := func(host string) string {
		t.Helper()
		logs := captureSlog(t)
		s := newServer(func(d *Deps) { d.Secrets = cutRegistry() })
		if w := (req{path: "/api/v1/me", host: host}).do(s.Handler()); w.Code != http.StatusMisdirectedRequest {
			t.Fatalf("unknown host = %d, want 421", w.Code)
		}
		var line struct{ Host string }
		if err := json.Unmarshal([]byte(strings.TrimSpace(logs.String())), &line); err != nil {
			t.Fatalf("log %q: %v", logs.String(), err)
		}
		return line.Host
	}
	pad := strings.Repeat("h", 100-8)
	for name, span := range straddlers {
		if got := warned(pad + span); got != pad+"[redacted]" {
			t.Errorf("%s: logged host %q", name, got)
		}
	}
	if got := warned(strings.Repeat("h", 150)); got != strings.Repeat("h", 100) {
		t.Errorf("plain host logged as %q", got)
	}
}
