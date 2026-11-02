package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
)

// probeLines are the "principal seen" lines buf holds, decoded.
func probeLines(t *testing.T, buf *syncBuf) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["msg"] == "principal seen" {
			out = append(out, m)
		}
	}
	return out
}

func probeLogger(buf *syncBuf) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

func TestPrincipalSeenLogsOncePerUserAndStops(t *testing.T) {
	buf := &syncBuf{}
	pr := &principalProbe{log: probeLogger(buf)}
	var wg sync.WaitGroup
	// 300 distinct users, each seen by four goroutines at once: one line per user up to the cap.
	for i := range 300 {
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				pr.see(auth.Principal{Kind: auth.KindProxy, User: fmt.Sprintf("user-%03d", i), Role: auth.RoleAdmin, Via: "X-Forwarded-User"}, "127.0.0.1")
			}()
		}
	}
	wg.Wait()

	lines := probeLines(t, buf)
	if len(lines) != maxProbeNames {
		t.Fatalf("%d lines, want %d (the cap)", len(lines), maxProbeNames)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		u := l["user"].(string)
		if seen[u] {
			t.Errorf("user %q logged twice", u)
		}
		seen[u] = true
	}

	// After the cap the probe is silent, for a new name and for one it already logged.
	before := len(buf.String())
	pr.see(auth.Principal{Kind: auth.KindProxy, User: "latecomer", Role: auth.RoleAdmin}, "127.0.0.1")
	for u := range seen {
		pr.see(auth.Principal{Kind: auth.KindProxy, User: u, Role: auth.RoleAdmin}, "127.0.0.1")
	}
	if len(buf.String()) != before {
		t.Errorf("the probe logged after its cap: %s", buf.String()[before:])
	}
}

func TestPrincipalSeenCapsTheLoggedName(t *testing.T) {
	buf := &syncBuf{}
	pr := &principalProbe{log: probeLogger(buf)}
	long := strings.Repeat("€", 50) // 150 bytes: the 128-byte cut lands inside the 43rd rune
	pr.see(auth.Principal{Kind: auth.KindProxy, User: long, Role: auth.RoleAdmin}, "127.0.0.1")
	pr.see(auth.Principal{Kind: auth.KindProxy, User: long + "x", Role: auth.RoleAdmin}, "127.0.0.1")
	lines := probeLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1 (both names share their first 128 bytes)", len(lines))
	}
	if u := lines[0]["user"].(string); len(u) > maxProbeUser || u != strings.Repeat("€", 42) {
		t.Errorf("logged user = %q (%d bytes)", u, len(u))
	}
}

// A name the probe remembers must not be a view into the header value it came from: a 64 KiB
// header would otherwise stay in memory for each of the 256 names (the cap's point is a bound of
// 256 names of 128 bytes). The check is on the stored keys' addresses, not on allocation counts.
func TestPrincipalSeenKeepsNoPartOfTheHeaderValue(t *testing.T) {
	inputs := []string{
		"n" + strings.Repeat("x", 100<<10),                 // cut at 128 bytes
		strings.TrimSpace("\u00a0\u00a0padded-name\u00a0"), // trimmed: a substring of the padded value
	}
	pr := &principalProbe{log: probeLogger(&syncBuf{})}
	for _, in := range inputs {
		pr.see(auth.Principal{Kind: auth.KindProxy, User: in, Role: auth.RoleAdmin}, "127.0.0.1")
	}
	stored := 0
	pr.seen.Range(func(k, _ any) bool {
		stored++
		key := uintptr(unsafe.Pointer(unsafe.StringData(k.(string))))
		for _, in := range inputs {
			start := uintptr(unsafe.Pointer(unsafe.StringData(in)))
			if key >= start && key < start+uintptr(len(in)) {
				t.Errorf("stored name %q points into a %d-byte header value", k, len(in))
			}
		}
		return true
	})
	if stored != len(inputs) {
		t.Fatalf("%d names stored, want %d", stored, len(inputs))
	}
}

// The line names the user, the kind, the role, the TCP peer and what carried the identity, and
// carries nothing else a client sent.
func TestPrincipalSeenCarriesPeerAndVia(t *testing.T) {
	const canary = "CANARY-EMAIL-VALUE"
	for _, tc := range []struct {
		name     string
		cfg      func(*config.Config)
		r        req
		wantUser string
		wantKind string
		wantVia  string
	}{
		{"the subject header", nil,
			req{path: "/api/v1/me", peer: "127.0.0.1:41000", header: map[string]string{"X-Forwarded-Preferred-Username": "", "X-Forwarded-User": "dash-baseline", "X-Forwarded-Email": canary}},
			"dash-baseline", "proxy", "X-Forwarded-User"},
		{"the preferred username wins over the subject", nil,
			req{path: "/api/v1/me", peer: "[::1]:41000", header: map[string]string{"X-Forwarded-Preferred-Username": "dash-verify", "X-Forwarded-User": "sub-123", "X-Forwarded-Email": canary}},
			"dash-verify", "proxy", "X-Forwarded-Preferred-Username"},
		{"dev signs in a fixed user", func(c *config.Config) { c.Dev = true },
			req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}},
			"dev", "dev", "fixed"},
		{"dev with a header names the header", func(c *config.Config) { c.Dev = true },
			req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": "bob-from-header"}},
			"bob-from-header", "dev", "X-Forwarded-Preferred-Username"},
		{"demo names the header the demo wrapper injects", func(c *config.Config) { c.Demo = true },
			req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": "", "X-Forwarded-User": "demo"}},
			"demo", "demo", "X-Forwarded-User"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := &syncBuf{}
			s := newServer(func(d *Deps) {
				if tc.cfg != nil {
					tc.cfg(&d.Cfg)
				}
			})
			s.probe.log = probeLogger(buf)
			h := s.Handler()
			for range 2 { // the second request is the same user: still one line
				if w := tc.r.do(h); w.Code != 200 {
					t.Fatalf("status = %d %s", w.Code, w.Body)
				}
			}
			lines := probeLines(t, buf)
			if len(lines) != 1 {
				t.Fatalf("lines = %v, want one", lines)
			}
			l := lines[0]
			wantPeer := "127.0.0.1"
			if strings.HasPrefix(tc.r.peer, "[::1]") {
				wantPeer = "::1"
			}
			if tc.r.peer == "" {
				wantPeer = "127.0.0.1" // testPeer
			}
			if l["user"] != tc.wantUser || l["kind"] != tc.wantKind || l["role"] != "admin" || l["peer"] != wantPeer || l["via"] != tc.wantVia || l["level"] != "INFO" {
				t.Errorf("line = %v, want user=%s kind=%s role=admin peer=%s via=%s INFO", l, tc.wantUser, tc.wantKind, wantPeer, tc.wantVia)
			}
			if strings.Contains(buf.String(), canary) {
				t.Errorf("the line carries a header value besides the name: %s", buf.String())
			}
		})
	}
}

// An unauthenticated request has no user to report.
func TestPrincipalSeenIgnoresUnauthenticatedRequests(t *testing.T) {
	buf := &syncBuf{}
	s := newServer(nil)
	s.probe.log = probeLogger(buf)
	if w := (req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}).do(s.Handler()); w.Code != 401 {
		t.Fatalf("status = %d", w.Code)
	}
	if len(buf.String()) != 0 {
		t.Errorf("logged %s", buf.String())
	}
}
