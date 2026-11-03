package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// quiet drops the slog side, for tests that write many records and only read them back.
var quiet = slog.New(slog.DiscardHandler)

// onDisk is what Recent must list from dir for ring: every line of audit.jsonl and then of
// audit.jsonl.1, .2, …, newest first, decoded with Prev cleared, with each line's SHA-256.
func onDisk(t *testing.T, dir string, ring Ring) (recs []Record, sums []string) {
	t.Helper()
	nums, err := rotatedNumbers(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{FileName}
	for _, n := range nums {
		names = append(names, FileName+"."+strconv.Itoa(n))
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		for _, line := range slices.Backward(readLines(t, path)) {
			rec := decode(t, line)
			if (rec.Kind == KindDenial) != (ring == RingUnauth) {
				continue
			}
			rec.Prev = ""
			recs = append(recs, rec)
			sums = append(sums, hashLine(line))
		}
	}
	return recs, sums
}

func summary(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = fmt.Sprintf("%s %s %s", r.Kind, r.Action, r.Target.Name)
	}
	return out
}

func sameRecords(t *testing.T, what string, got, want []Record) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", what, summary(got), summary(want))
	}
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestRecentReadsRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 700})
	for i := range 30 {
		rec := restart("alice")
		rec.Target.Name = fmt.Sprintf("app-%d", i)
		p, err := l.Intent(rec)
		if err != nil {
			t.Fatal(err)
		}
		p.Outcome(nil)
		l.Denial(fmt.Sprintf("192.0.2.%d", i), "csrf", Record{Action: "POST /api/v1/x", Target: Target{Name: fmt.Sprintf("d-%d", i)}})
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".5")); err != nil {
		t.Fatalf("no %s.5 (%v): the test must reach the oldest rotated file", FileName, err)
	}
	wantMain, _ := onDisk(t, dir, RingMain)
	wantUnauth, _ := onDisk(t, dir, RingUnauth)
	if len(wantMain) < 6 || len(wantUnauth) < 3 {
		t.Fatalf("only %d and %d records kept; the test needs several in each ring", len(wantMain), len(wantUnauth))
	}
	// A crash mid-write leaves a line without its newline. Its writer never got an
	// acknowledgement, so it is not a record, though it decodes and names the last line as prev.
	torn, err := json.Marshal(Record{Kind: KindEvent, Action: "auth.login", Result: ResultOK, Prev: l.last})
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, filepath.Join(dir, FileName), torn)

	for ring, want := range map[Ring][]Record{RingMain: wantMain, RingUnauth: wantUnauth} {
		got, next, source := l.Recent(1000, "", ring)
		if source != SourceFile || next != "" {
			t.Errorf("%s: source %q, next %q; want %q and no next page", ring, source, next, SourceFile)
		}
		sameRecords(t, string(ring), got, want)
	}
}

func TestRecentRingsWhenNoDataDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"no dir":    "",
		"missing":   filepath.Join(t.TempDir(), "nope"),
		"not a dir": file,
	}
	if os.Geteuid() != 0 {
		ro := t.TempDir()
		if err := os.Chmod(ro, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(ro, 0o700) })
		cases["read-only"] = ro
	}
	for name, dir := range cases {
		t.Run(name, func(t *testing.T) {
			l := open(t, dir, Options{Logger: quiet})
			if l.Enabled() {
				t.Fatal("Enabled() = true")
			}
			for i := range 300 { // 600 records for a ring of 500
				rec := restart("alice")
				rec.Target.Name = fmt.Sprintf("app-%d", i)
				p, err := l.Intent(rec)
				if err != nil {
					t.Fatal(err)
				}
				p.Outcome(nil)
			}
			for i := range 250 { // 250 for a ring of 200
				l.Denial(fmt.Sprintf("192.0.2.%d", i), "csrf", Record{Action: "POST /api/v1/x", Target: Target{Name: fmt.Sprintf("d-%d", i)}})
			}

			recs, next, source := l.Recent(1000, "", RingMain)
			if source != SourceMemory || next != "" || len(recs) != 500 {
				t.Fatalf("main: %d records, source %q, next %q; want the latest 500 from memory", len(recs), source, next)
			}
			for i, r := range recs { // newest first: the outcome of app-299, its intent, …, the intent of app-50
				kind := KindOutcome
				if i%2 == 1 {
					kind = KindIntent
				}
				if want := fmt.Sprintf("app-%d", 299-i/2); r.Kind != kind || r.Target.Name != want {
					t.Fatalf("main[%d] = %s %s, want %s %s", i, r.Kind, r.Target.Name, kind, want)
				}
			}

			recs, next, source = l.Recent(1000, "", RingUnauth)
			if source != SourceMemory || next != "" || len(recs) != 200 {
				t.Fatalf("unauth: %d records, source %q, next %q; want the latest 200 from memory", len(recs), source, next)
			}
			for i, r := range recs {
				if want := fmt.Sprintf("d-%d", 249-i); r.Kind != KindDenial || r.Target.Name != want {
					t.Fatalf("unauth[%d] = %s %s, want the denial %s", i, r.Kind, r.Target.Name, want)
				}
			}
		})
	}

	t.Run("nil log", func(t *testing.T) {
		var l *Log
		if recs, next, source := l.Recent(10, "", RingMain); recs != nil || next != "" || source != SourceMemory {
			t.Errorf("Recent = %v, %q, %q; want nothing from memory", recs, next, source)
		}
	})
}

func TestUnauthFloodCannotEvictMainRing(t *testing.T) {
	l := open(t, "", Options{Logger: quiet})
	p, err := l.Intent(restart("alice"))
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(nil)
	l.Event(Record{Action: "auth.login", Actor: Actor{User: "alice"}, Target: Target{Kind: "user", Name: "alice"}})
	// Failed logins (as the login routes record them: a denial naming the attempt in the target)
	// and refused requests, each from its own peer so the per-peer limit lets every one through.
	const flood = 5000
	for i := range flood {
		if i%2 == 0 {
			l.Denial(fmt.Sprintf("peer-%d", i), "login_failed", Record{Action: "auth.login.failed", Target: Target{Kind: "user", Name: fmt.Sprintf("guess-%d", i)}})
		} else {
			l.Denial(fmt.Sprintf("peer-%d", i), "unauthenticated", Record{Action: "GET /api/v1/me", Target: Target{Name: fmt.Sprintf("guess-%d", i)}})
		}
	}

	recs, next, _ := l.Recent(500, "", RingMain)
	if got, want := summary(recs), []string{"event auth.login alice", "outcome kubernetes.restart app-0", "intent kubernetes.restart app-0"}; next != "" || !slices.Equal(got, want) {
		t.Errorf("main after the flood = %q (next %q), want %q", got, next, want)
	}
	recs, _, _ = l.Recent(500, "", RingUnauth)
	if len(recs) != 200 || recs[0].Target.Name != fmt.Sprintf("guess-%d", flood-1) {
		t.Errorf("unauth holds %d records, newest %v; want the latest 200 denials", len(recs), summary(recs[:min(1, len(recs))]))
	}
}

func TestRecentNeverReturnsPrevOrValues(t *testing.T) {
	const secret = "s3cr3t-canary-value"
	scrub := func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") }
	hash := regexp.MustCompile(`[0-9a-f]{64}`)
	for name, dir := range map[string]string{"file": t.TempDir(), "memory": ""} {
		t.Run(name, func(t *testing.T) {
			l := open(t, dir, Options{Scrub: scrub})
			p, err := l.Intent(Record{Action: "settings.set", Fields: []string{"plugins.grafana.token"}, Source: "ui", Actor: Actor{User: "alice"}})
			if err != nil {
				t.Fatal(err)
			}
			p.Outcome(fmt.Errorf("upstream refused token %s", secret))
			l.Event(Record{Action: "auth.logout", Result: ResultError, Error: "ending the sessions failed: " + secret, Actor: Actor{User: "alice"}})
			l.Event(Record{Action: "auth.login", Actor: Actor{User: "alice"}, Prev: hashLine([]byte("a caller's prev"))})
			l.Denial("192.0.2.1", "csrf", Record{Action: "POST /api/v1/settings", Error: secret})

			var errs []string
			for _, ring := range []Ring{RingMain, RingUnauth} {
				recs, _, _ := l.Recent(100, "", ring)
				if len(recs) == 0 {
					t.Fatalf("%s: no records", ring)
				}
				for _, r := range recs {
					if r.Prev != "" {
						t.Errorf("%s: %s %s carries prev %q", ring, r.Kind, r.Action, r.Prev)
					}
					errs = append(errs, r.Error)
				}
				b, err := json.Marshal(recs)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(b), secret) {
					t.Errorf("%s: the scrubbed value is listed: %s", ring, b)
				}
				if h := hash.Find(b); h != nil {
					t.Errorf("%s: a chain hash %s is listed: %s", ring, h, b)
				}
			}
			slices.Sort(errs)
			want := []string{"", "", "[redacted]", "ending the sessions failed: [redacted]", "upstream refused token [redacted]"}
			if !slices.Equal(errs, want) {
				t.Errorf("errors listed %q, want the scrubbed %q", errs, want)
			}
		})
	}
}

func TestRecentCursor(t *testing.T) {
	for name, dir := range map[string]string{"file": t.TempDir(), "memory": ""} {
		t.Run(name, func(t *testing.T) {
			// One or two records a file, all kept: the pages cross rotated files.
			l := open(t, dir, Options{MaxSize: 700, Keep: 30})
			for i := range 10 {
				l.Event(Record{Action: fmt.Sprintf("e%d", i)})
				l.Denial(fmt.Sprintf("192.0.2.%d", i), "csrf", Record{Action: fmt.Sprintf("d%d", i)})
			}
			all, next, _ := l.Recent(100, "", RingMain)
			if len(all) != 10 || all[0].Action != "e9" || next != "" {
				t.Fatalf("Recent(100) = %q, next %q; want e9 … e0 and no next page", summary(all), next)
			}

			first, next, _ := l.Recent(3, "", RingMain)
			sameRecords(t, "first page", first, all[:3])
			// The cursor is the hex SHA-256 of the line of the page's oldest record with a file,
			// that record's number in its ring without one.
			want := "8"
			if l.Enabled() {
				_, sums := onDisk(t, dir, RingMain)
				want = sums[2]
			}
			if next != want {
				t.Errorf("first page's next = %q, want %q", next, want)
			}
			// A cursor stays put while records are added.
			l.Event(Record{Action: "e10"})
			l.Denial("192.0.2.99", "csrf", Record{Action: "d10"})
			second, _, _ := l.Recent(3, next, RingMain)
			sameRecords(t, "the page after the cursor, once newer records exist", second, all[3:6])

			// Paging visits every record once; the last page has no next.
			var paged []Record
			cursor := ""
			for range 10 {
				recs, next, _ := l.Recent(3, cursor, RingMain)
				paged = append(paged, recs...)
				if cursor = next; cursor == "" {
					break
				}
			}
			if cursor != "" {
				t.Fatal("pages never end")
			}
			sameRecords(t, "paged", paged[1:], all) // paged[0] is e10
			// A page that ends at the oldest record has no next.
			_, mid, _ := l.Recent(6, "", RingMain) // e10 … e5
			if recs, next, _ := l.Recent(5, mid, RingMain); len(recs) != 5 || recs[4].Action != "e0" || next != "" {
				t.Errorf("the last 5 = %q, next %q; want e4 … e0 and no next", summary(recs), next)
			}

			for _, c := range []string{"nope", "0", "01", "+3", "99", strings.Repeat("0", 64), hashLine([]byte("never written"))} {
				if recs, next, _ := l.Recent(100, c, RingMain); recs != nil || next != "" {
					t.Errorf("unknown cursor %q listed %q, next %q; want nothing", c, summary(recs), next)
				}
			}
			if recs, _, _ := l.Recent(0, "", RingMain); recs != nil {
				t.Errorf("limit 0 listed %q", summary(recs))
			}
			if recs, _, _ := l.Recent(10, "", Ring("other")); recs != nil {
				t.Errorf("an unknown ring listed %q", summary(recs))
			}
		})
	}

	t.Run("forgotten", func(t *testing.T) {
		dir := t.TempDir()
		l := open(t, dir, Options{MaxSize: 1}) // one record a file: six are kept
		l.Event(Record{Action: "e0"})
		l.Event(Record{Action: "e1"})
		_, sums := onDisk(t, dir, RingMain) // e1, e0
		for i := 2; i < 10; i++ {
			l.Event(Record{Action: fmt.Sprintf("e%d", i)})
		}
		if recs, next, _ := l.Recent(100, sums[0], RingMain); recs != nil || next != "" {
			t.Errorf("a rotated-away record's cursor listed %q, next %q", summary(recs), next)
		}

		m := open(t, "", Options{Logger: quiet})
		for i := range 510 { // a ring of 500: the first 10 are dropped
			m.Event(Record{Action: fmt.Sprintf("e%d", i)})
		}
		if recs, next, _ := m.Recent(100, "5", RingMain); recs != nil || next != "" {
			t.Errorf("a dropped record's cursor listed %q, next %q", summary(recs), next)
		}
	})
}

// Recent does not hold up writers, so the log can rotate while it reads. It lists the records
// written before the call, each once and in order.
func TestRecentToleratesRotationMidRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		at     string // the file whose opening triggers the rotations
		writes int    // records written then; each one rotates (MaxSize 1)
	}{
		{"before the active file", FileName, 1},
		{"after the active file", FileName + ".1", 1},
		{"twice, mid-read", FileName + ".2", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l := open(t, dir, Options{MaxSize: 1}) // one record a file
			for i := range 4 {
				l.Event(Record{Action: fmt.Sprintf("e%d", i)})
			}
			want, _ := onDisk(t, dir, RingMain) // e3 … e0
			fired := false
			l.openRead = func(name string) (*os.File, error) {
				if !fired && filepath.Base(name) == tc.at {
					fired = true
					for i := range tc.writes {
						l.Event(Record{Action: fmt.Sprintf("late%d", i)})
					}
				}
				return os.Open(name)
			}
			got, next, _ := l.Recent(100, "", RingMain)
			if !fired {
				t.Fatalf("%s was never opened", tc.at)
			}
			if next != "" {
				t.Errorf("next = %q", next)
			}
			sameRecords(t, "records", got, want)
		})
	}
}

func TestRecentLogsAReadErrorAndListsWhatItRead(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := open(t, dir, Options{MaxSize: 1, Logger: jsonLogger(&buf, slog.LevelInfo)})
	for i := range 3 {
		l.Event(Record{Action: fmt.Sprintf("e%d", i)})
	}
	want, _ := onDisk(t, dir, RingMain)
	l.openRead = func(name string) (*os.File, error) {
		if filepath.Base(name) == FileName+".1" {
			return nil, errors.New("disk on fire")
		}
		return os.Open(name)
	}
	buf.Reset()
	got, next, _ := l.Recent(100, "", RingMain)
	sameRecords(t, "records", got, want[:1])
	if next != "" {
		t.Errorf("next = %q after a failed read", next)
	}
	if out := buf.String(); !strings.Contains(out, `"level":"ERROR","msg":"audit read failed","error":"disk on fire"`) {
		t.Errorf("no ERROR for the failed read: %s", out)
	}
}

// The rings' memory, from a heap sample: every field a client controls at the cap internal/api
// puts on it (a 200-byte request line, 128-byte user and login names), Kubernetes names at
// theirs, a 256-byte error on every outcome and three changed paths. Expected under 1 MiB.
func TestRingsStayUnderOneMiB(t *testing.T) {
	text := func(i, n int) string { return fmt.Sprintf("%0*d", n, i) } // n bytes, its own allocation
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	m := newRings()
	for i := range mainRingSize {
		m.push(Record{
			Time: at, Kind: KindOutcome, ID: text(i, 16), Action: "kubernetes.restart",
			Target: Target{Kind: "Deployment", Namespace: text(i, 63), Name: text(i, 253)},
			Fields: []string{text(i, 40), text(i+1, 40), text(i+2, 40)}, Source: "ui",
			Result: ResultError, Error: text(i, 256),
			Actor: Actor{Type: "session", User: text(i, 128), Surface: "ui"},
			Peer:  text(i, 39), RequestID: text(i, 32), AuthMode: "local", Plugin: text(i, 32),
		})
	}
	for i := range unauthRingSize {
		m.push(Record{
			Time: at, Kind: KindDenial, Action: text(i, 200), Target: Target{Kind: "user", Name: text(i, 128)},
			Actor: Actor{Type: "proxy", User: text(i, 128), Surface: "ui"},
			Peer:  text(i, 39), RequestID: text(i, 32), AuthMode: "proxy", Reason: "login_failed",
		})
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if m.main.n != mainRingSize || m.unauth.n != unauthRingSize {
		t.Fatalf("pushed %d and %d", m.main.n, m.unauth.n)
	}
	runtime.KeepAlive(m)
	used := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("full rings (%d + %d records) hold %d bytes of heap (%.0f KiB)", mainRingSize, unauthRingSize, used, float64(used)/1024)
	if used >= 1<<20 {
		t.Errorf("full rings hold %d bytes, want under 1 MiB", used)
	}
}
