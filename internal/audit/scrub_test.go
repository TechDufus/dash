package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
)

// A denial records the client's request line and a stream or a login attempt names a target the
// client chose; a registered secret or a token in that text must not reach the file, the rings,
// Recent or slog. The log is wired as dash wires it: Scrub is the registry's ScrubLogLine and the
// logger's handler is the redacting one.
func TestRequestTextIsScrubbedBeforeItIsRecorded(t *testing.T) {
	const (
		registered = "reg-canary-7d1e94b2c"
		token      = "dash_pat_k3y1d_Zx9vQm2Lp8Wd"
	)
	reg := secret.NewRegistry()
	reg.AddString(registered)
	leaks := func(where string, b []byte) {
		t.Helper()
		for _, s := range []string{registered, token, "Zx9vQm2Lp8Wd"} {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("%s holds %q: %s", where, s, b)
			}
		}
	}

	for name, dir := range map[string]string{"file": t.TempDir(), "memory": ""} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			l := open(t, dir, Options{
				Logger: slog.New(secret.NewHandler(slog.NewJSONHandler(&out, nil), reg)),
				Scrub:  reg.ScrubLogLine,
			})
			l.Denial("192.0.2.7", "unauthenticated", Record{
				Action:    "GET /api/v1/plugins/grafana/d/" + registered + "/" + token,
				Target:    Target{Kind: "user", Name: token},
				RequestID: registered,
				Actor:     Actor{User: "mallory-" + registered, Surface: "ui"},
			})
			l.Event(Record{
				Action: "logs.tail",
				Target: Target{Kind: "pod-" + token, Namespace: registered, Name: "web-" + token},
				Actor:  Actor{User: "alice", ClientID: "client-" + registered},
			})
			p, err := l.Intent(Record{Action: "kubernetes.restart", Target: Target{Kind: "Deployment", Namespace: "media", Name: registered}})
			if err != nil {
				t.Fatal(err)
			}
			p.Outcome(nil)

			if dir != "" {
				leaks("audit.jsonl", mustRead(t, filepath.Join(dir, FileName)))
			} else {
				l.mu.Lock()
				main, unauth := l.mem.main.recs, l.mem.unauth.recs
				b, err := json.Marshal([][]Record{main, unauth})
				l.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				leaks("the rings", b)
			}

			var got []string
			for _, ring := range []Ring{RingMain, RingUnauth} {
				recs, _, _ := l.Recent(100, "", ring)
				b, err := json.Marshal(recs)
				if err != nil {
					t.Fatal(err)
				}
				leaks("Recent "+string(ring), b)
				for _, r := range recs {
					got = append(got, strings.Join([]string{string(r.Kind), r.Action, r.Target.Kind, r.Target.Namespace, r.Target.Name, r.RequestID, r.Actor.User, r.Actor.ClientID}, "|"))
				}
			}
			want := []string{
				"outcome|kubernetes.restart|Deployment|media|[redacted]|||",
				"intent|kubernetes.restart|Deployment|media|[redacted]|||",
				"event|logs.tail|pod-[redacted]|[redacted]|web-[redacted]||alice|client-[redacted]",
				"denial|GET /api/v1/plugins/grafana/d/[redacted]/[redacted]|user||[redacted]|[redacted]|mallory-[redacted]|",
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("Recent listed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}

			leaks("slog", out.Bytes())
			if c := strings.Count(out.String(), `"msg":"audit"`); c != 3 {
				t.Errorf("slog has %d audit records, want 3 (denial, event, outcome): %s", c, out.Bytes())
			}

			if dir != "" {
				// The chain hashes the scrubbed lines: a reopen continues it.
				l.Close()
				l2, err := Open(dir, Options{Logger: quiet})
				if err != nil {
					t.Fatalf("reopen: %v", err)
				}
				l2.Close()
			}
		})
	}
}
