package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a settable fake time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// jsonLogger returns a logger writing JSON without timestamps, so two loggers can be
// compared byte for byte.
func jsonLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func open(t *testing.T, dir string, opts Options) *Log {
	t.Helper()
	if opts.Now == nil {
		opts.Now = newClock().Now
	}
	if opts.Logger == nil {
		opts.Logger = jsonLogger(&bytes.Buffer{}, slog.LevelDebug)
	}
	l, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func restart(alice string) Record {
	return Record{
		Action: "kubernetes.restart",
		Target: Target{Kind: "Deployment", Namespace: "media", Name: "app-0"},
		Actor:  Actor{Type: "session", User: alice, Surface: "ui"},
	}
}

// legacy is internal/api/actions.go's audit helper before this package, with the
// request's user passed in and the logger injected.
func legacy(lg *slog.Logger, user, action, kind, ns, name string, err error) {
	result, msg := "ok", ""
	if err != nil {
		result, msg = "error", err.Error()
	}
	lg.Info("audit", "user", user, "action", action, "kind", kind,
		"namespace", ns, "name", name, "result", result, "error", msg)
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	if len(b) == 0 {
		return nil
	}
	return bytes.Split(b, []byte("\n"))
}

func decode(t *testing.T, line []byte) Record {
	t.Helper()
	var r Record
	if err := json.Unmarshal(line, &r); err != nil {
		t.Fatalf("line %q: %v", line, err)
	}
	return r
}

func TestOutcomeSlogMatchesLegacyHelper(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  func(t *testing.T) string
		err  error
	}{
		{"file ok", func(t *testing.T) string { return t.TempDir() }, nil},
		{"file error", func(t *testing.T) string { return t.TempDir() }, errors.New("boom: forbidden")},
		{"slog only ok", func(*testing.T) string { return "" }, nil},
		{"slog only error", func(t *testing.T) string { return filepath.Join(t.TempDir(), "missing") }, errors.New("x")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got, want bytes.Buffer
			l := open(t, tc.dir(t), Options{Logger: jsonLogger(&got, slog.LevelInfo)})
			got.Reset() // drop the disabled-file warning, if any

			p, err := l.Intent(restart("alice"))
			if err != nil {
				t.Fatal(err)
			}
			p.Outcome(tc.err)
			legacy(jsonLogger(&want, slog.LevelInfo), "alice", "kubernetes.restart", "Deployment", "media", "app-0", tc.err)

			if got.String() != want.String() {
				t.Errorf("slog output differs\n got: %s\nwant: %s", got.String(), want.String())
			}
		})
	}
}

func TestIntentAndOutcomeLines(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	if !l.Enabled() {
		t.Fatal("Enabled() = false on a writable dir")
	}
	rec := restart("alice")
	rec.Fields = []string{"plugins.prometheus.url"}
	rec.Source = "ui"
	rec.Peer, rec.RequestID, rec.AuthMode = "192.0.2.7", "req-1", "oidc"
	p, err := l.Intent(rec)
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(errors.New("denied by guardrail"))
	p.Outcome(nil) // a second call must not write another record

	path := filepath.Join(dir, FileName)
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("audit.jsonl: %v, %v; want mode 0600", fi, err)
	}
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), bytes.Join(lines, []byte("\n")))
	}
	in, out := decode(t, lines[0]), decode(t, lines[1])
	if in.Kind != KindIntent || out.Kind != KindOutcome {
		t.Errorf("kinds = %s, %s", in.Kind, out.Kind)
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(in.ID) || in.ID != out.ID {
		t.Errorf("ids = %q, %q; want the same 16 hex", in.ID, out.ID)
	}
	if in.Result != "" || out.Result != ResultError || out.Error != "denied by guardrail" {
		t.Errorf("results: intent %q, outcome %q/%q", in.Result, out.Result, out.Error)
	}
	for _, r := range []Record{in, out} {
		if r.Action != rec.Action || r.Target != rec.Target || r.Actor != rec.Actor || r.Source != "ui" ||
			r.Peer != "192.0.2.7" || r.RequestID != "req-1" || r.AuthMode != "oidc" ||
			len(r.Fields) != 1 || r.Fields[0] != "plugins.prometheus.url" {
			t.Errorf("record lost descriptive fields: %+v", r)
		}
	}
	if in.Prev != "" || out.Prev != hashLine(lines[0]) {
		t.Errorf("prev chain: intent %q, outcome %q; want \"\" and sha256(line 1)", in.Prev, out.Prev)
	}
	if !in.Time.Equal(newClock().Now()) {
		t.Errorf("time = %v", in.Time)
	}
	if rep, err := Verify(dir); err != nil || !rep.OK() || len(rep.Dangling) != 0 || rep.Records != 2 {
		t.Errorf("Verify = %+v, %v", rep, err)
	}
}

// A created object's identity is known only after the call; the outcome reports it, the
// intent keeps what was known, and the outcome is still written once.
func TestOutcomeCanReportAnotherTarget(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := open(t, dir, Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
	rec := Record{Action: "silence.create", Target: Target{Kind: "Silence", Name: "HighTemp"}, Actor: Actor{User: "alice"}}
	p, err := l.Intent(rec)
	if err != nil {
		t.Fatal(err)
	}
	p = p.WithTarget(Target{Kind: "Silence", Name: "sil-1234"})
	p.Outcome(nil)
	p.Outcome(errors.New("again"))

	lines := readLines(t, filepath.Join(dir, FileName))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if in, out := decode(t, lines[0]), decode(t, lines[1]); in.Target.Name != "HighTemp" || out.Target.Name != "sil-1234" || in.ID != out.ID {
		t.Errorf("intent %+v, outcome %+v", in, out)
	}
	if !strings.Contains(buf.String(), `"name":"sil-1234"`) {
		t.Errorf("slog record lacks the outcome's target: %s", buf.String())
	}
}

// The slog line is the only audit trail where there is no data directory, so it carries the
// request id an error response quotes: for outcomes, denials and events alike.
func TestSlogLineCarriesTheRequestID(t *testing.T) {
	var buf bytes.Buffer
	l := open(t, t.TempDir(), Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
	rec := Record{Action: "kubernetes.restart", Actor: Actor{User: "alice"}, RequestID: "req-42"}
	p, err := l.Intent(rec)
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(nil)
	l.Denial("192.0.2.7", "csrf", rec)
	l.Event(rec)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	n := 0
	for _, line := range lines {
		if !strings.Contains(line, `"msg":"audit"`) {
			continue
		}
		n++
		if !strings.Contains(line, `"requestId":"req-42"`) {
			t.Errorf("audit slog line has no request id: %s", line)
		}
	}
	if n < 3 {
		t.Errorf("%d audit slog lines, want the outcome, the denial and the event at least: %s", n, buf.String())
	}

	// A record without one adds no empty field.
	buf.Reset()
	l.Event(Record{Action: "login", Actor: Actor{User: "alice"}})
	if strings.Contains(buf.String(), "requestId") {
		t.Errorf("a record with no request id logged one: %s", buf.String())
	}
}

// Without a data directory the slog line is the whole record of an MCP call: it keeps the
// surface, the token, the argument hash, the client and the duration, and adds none of them
// empty.
func TestSlogOnlyLineKeepsTheMCPFields(t *testing.T) {
	var buf bytes.Buffer
	l, err := Open("", Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	l.Event(Record{Action: "mcp tools/call", Actor: Actor{User: "alice", TokenID: "tok-7", Surface: "mcp"},
		Target: Target{Kind: "tool", Name: "dash_get"}, ArgsSHA256: "ab12", Client: "claude-code", DurationMs: 42})
	for _, want := range []string{`"surface":"mcp"`, `"tokenID":"tok-7"`, `"argsSha256":"ab12"`, `"client":"claude-code"`, `"durationMs":42`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the slog line lacks %s: %s", want, buf.String())
		}
	}
	buf.Reset()
	l.Event(Record{Action: "login", Actor: Actor{User: "alice"}})
	for _, absent := range []string{"surface", "tokenID", "argsSha256", "client", "durationMs"} {
		if strings.Contains(buf.String(), absent) {
			t.Errorf("an empty %s was logged: %s", absent, buf.String())
		}
	}
}

func TestRotationKeepsFiveFilesAndChainVerifies(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 600})
	const n = 80
	for i := range n {
		rec := restart("alice")
		rec.Target.Name = fmt.Sprintf("app-%d", i)
		p, err := l.Intent(rec)
		if err != nil {
			t.Fatal(err)
		}
		p.Outcome(nil)
	}
	for _, name := range []string{FileName, FileName + ".1", FileName + ".2", FileName + ".3", FileName + ".4", FileName + ".5"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if fi.Size() > 600 {
			t.Errorf("%s is %d bytes, over the limit", name, fi.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".6")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("audit.jsonl.6 exists (err %v); only 5 rotated files are kept", err)
	}
	rep, err := Verify(dir)
	if err != nil || !rep.OK() {
		t.Fatalf("Verify = %+v, %v", rep, err)
	}
	if len(rep.Files) != 6 || rep.Files[0] != FileName+".5" || rep.Files[5] != FileName {
		t.Errorf("walked %v, want .5 .. .1 then the active file", rep.Files)
	}
	// The first record of each file links to the last line of the next older one.
	older := readLines(t, filepath.Join(dir, FileName+".1"))
	active := readLines(t, filepath.Join(dir, FileName))
	if got := decode(t, active[0]).Prev; got != hashLine(older[len(older)-1]) {
		t.Errorf("active file starts with prev %q, want the hash of .1's last line", got)
	}
	// An outcome never lands in a file older than its intent's, so rotation between the
	// two is not a dangling intent.
	if len(rep.Dangling) != 0 {
		t.Errorf("dangling after rotation: %+v", rep.Dangling)
	}
}

func TestIntentSplitByRotationIsNotDangling(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 1})
	p, err := l.Intent(restart("alice"))
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(nil) // every record rotates: intent and outcome end up in different files
	if _, err := os.Stat(filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(dir)
	if err != nil || !rep.OK() || len(rep.Dangling) != 0 {
		t.Errorf("Verify = %+v, %v", rep, err)
	}
}

func TestReopenContinuesChain(t *testing.T) {
	dir := t.TempDir()
	for range 3 {
		l := open(t, dir, Options{MaxSize: 2500}) // rotates, but keeps all 24 records
		for range 4 {
			p, err := l.Intent(restart("alice"))
			if err != nil {
				t.Fatal(err)
			}
			p.Outcome(nil)
		}
		l.Close()
	}
	rep, err := Verify(dir)
	if err != nil || !rep.OK() || rep.Records != 24 {
		t.Errorf("Verify = %+v, %v; want 24 records chained across restarts and rotation", rep, err)
	}
}

func TestReopenAfterRotationLeftEmptyActiveFile(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	p, _ := l.Intent(restart("alice"))
	p.Outcome(nil)
	l.Close()
	// A crash between rotating and the first write leaves an empty active file.
	if err := os.Rename(filepath.Join(dir, FileName), filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, Options{})
	l.Event(Record{Action: "login", Actor: Actor{User: "alice"}})
	if rep, err := Verify(dir); err != nil || !rep.OK() || rep.Records != 3 {
		t.Errorf("Verify = %+v, %v", rep, err)
	}
}

func TestReopenDropsTornFinalLine(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	p, _ := l.Intent(restart("alice"))
	p.Outcome(nil)
	l.Close()
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"time":"2026-11-01T12:00:00Z","kind":"inte`) // crash mid-write
	f.Close()

	l = open(t, dir, Options{})
	l.Event(Record{Action: "login"})
	if rep, err := Verify(dir); err != nil || !rep.OK() || rep.Records != 3 {
		t.Errorf("Verify = %+v, %v; want the torn line gone and the chain intact", rep, err)
	}
}

func TestSlogOnlyWhenDirNotWritable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":     "",
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
			var buf bytes.Buffer
			l := open(t, dir, Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
			if l.Enabled() {
				t.Fatal("Enabled() = true")
			}
			if !strings.Contains(buf.String(), "audit log file disabled") {
				t.Errorf("no note that the file is disabled: %s", buf.String())
			}
			// Expected configuration, not a fault: INFO, like the settings store.
			if strings.Contains(buf.String(), `"level":"WARN"`) || strings.Contains(buf.String(), `"level":"ERROR"`) {
				t.Errorf("disabled-file message above INFO: %s", buf.String())
			}
			buf.Reset()
			p, err := l.Intent(restart("alice"))
			if err != nil {
				t.Fatalf("Intent error %v; a slog-only log must never fail a request", err)
			}
			p.Outcome(nil)
			l.Event(Record{Action: "login", Actor: Actor{User: "alice"}})
			if n := strings.Count(buf.String(), `"msg":"audit"`); n != 2 {
				t.Errorf("%d audit slog records, want 2: %s", n, buf.String())
			}
			if dir != "" && dir != file {
				if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
					t.Error("audit.jsonl was created")
				}
			}
		})
	}
}

// A writable dir whose log file cannot be opened must stop dash: silently going slog-only
// would make the "no durable record, no change" rule void without anyone noticing.
func TestOpenFailsWhenLogFileCannotBeOpened(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir, Options{Logger: jsonLogger(&bytes.Buffer{}, slog.LevelInfo)})
	if err == nil {
		l.Close()
		t.Fatal("Open succeeded although audit.jsonl cannot be opened")
	}
	if !strings.Contains(err.Error(), FileName) {
		t.Errorf("error does not name the file: %v", err)
	}
}

func TestSecondWriterOnTheSameDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 1}) // every record rotates
	_, err := Open(dir, Options{Logger: jsonLogger(&bytes.Buffer{}, slog.LevelInfo)})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open = %v, want ErrLocked", err)
	}
	if !strings.Contains(err.Error(), dir) || !strings.Contains(err.Error(), "one dash instance") {
		t.Errorf("message does not say what to do: %v", err)
	}

	// The lock is its own file, so the settings store's .lock is untouched, and it lives
	// as long as the log, not as long as one file: rotation must not release it.
	if _, err := os.Stat(filepath.Join(dir, ".audit.lock")); err != nil {
		t.Errorf("no .audit.lock: %v", err)
	}
	l.Event(Record{Action: "login"})
	l.Event(Record{Action: "logout"})
	if _, err := os.Stat(filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatalf("no rotation happened: %v", err)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		t.Errorf("Open after a write = %v, want ErrLocked", err)
	}

	l.Close()
	l2, err := Open(dir, Options{Logger: jsonLogger(&bytes.Buffer{}, slog.LevelInfo)})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	l2.Close()
}

// Creating audit.jsonl must be followed by an fsync of the directory, or a crash can lose
// the file and every record fsynced into it.
func TestCreatingTheLogSyncsTheDirectory(t *testing.T) {
	var synced []string
	orig := syncDir
	syncDir = func(dir string) error { synced = append(synced, dir); return orig(dir) }
	t.Cleanup(func() { syncDir = orig })

	dir := t.TempDir()
	open(t, dir, Options{})
	if len(synced) != 1 || synced[0] != dir {
		t.Errorf("directory syncs after creating the log = %v, want [%s]", synced, dir)
	}
}

func TestNilLogIsSlogOnly(t *testing.T) {
	var l *Log
	if l.Enabled() {
		t.Fatal("nil log enabled")
	}
	p, err := l.Intent(restart("alice"))
	if err != nil {
		t.Fatal(err)
	}
	p.Outcome(nil)
	l.Event(Record{})
	l.Denial("192.0.2.7", "csrf_failed", Record{})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIntentFailsWhenWriteFailsOnWritableDir(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := open(t, dir, Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
	l.f.Close() // every later write on the active file fails

	p, err := l.Intent(restart("alice"))
	if !errors.Is(err, ErrIntent) {
		t.Fatalf("Intent error = %v, want one that wraps ErrIntent although the write failed", err)
	}
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Errorf("failed intent not logged at ERROR: %s", buf.String())
	}
	p.Outcome(nil) // the zero Pending is inert
	if strings.Contains(buf.String(), `"msg":"audit"`) {
		t.Error("an intent that failed still produced an audit slog record")
	}
}

func TestFailedOutcomeAndEventAreLoggedNotReturned(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := open(t, dir, Options{Logger: jsonLogger(&buf, slog.LevelInfo)})
	p, err := l.Intent(restart("alice"))
	if err != nil {
		t.Fatal(err)
	}
	l.f.Close()
	buf.Reset()

	p.Outcome(nil)
	l.Event(Record{Action: "login", Actor: Actor{User: "alice"}})
	out := buf.String()
	if n := strings.Count(out, `"level":"ERROR"`); n != 2 {
		t.Errorf("%d ERROR records, want 2 (outcome, event): %s", n, out)
	}
	if n := strings.Count(out, `"msg":"audit"`); n != 2 {
		t.Errorf("%d audit slog records, want 2: the slog side must survive a file failure: %s", n, out)
	}
}

func TestFailedWriteDoesNotAdvanceChain(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	l.Event(Record{Action: "a"})
	l.mu.Lock()
	good := l.f
	l.f, _ = os.Open(os.DevNull) // read-only handle: Write fails
	l.mu.Unlock()
	l.Event(Record{Action: "lost"})
	l.mu.Lock()
	l.f = good
	l.mu.Unlock()
	l.Event(Record{Action: "b"})
	if rep, err := Verify(dir); err != nil || !rep.OK() || rep.Records != 2 {
		t.Errorf("Verify = %+v, %v; want the failed record skipped without breaking the chain", rep, err)
	}
}

func TestVerifyDetectsEditedLine(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(line string) string
		wantLine int
	}{
		{"changed field", func(s string) string { return strings.Replace(s, "app-2", "app-9", 1) }, 3},
		{"garbage", func(string) string { return "not json" }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l := open(t, dir, Options{})
			for i := range 6 {
				rec := restart("alice")
				rec.Target.Name = fmt.Sprintf("app-%d", i)
				l.Event(rec)
			}
			path := filepath.Join(dir, FileName)
			lines := strings.Split(strings.TrimSuffix(string(mustRead(t, path)), "\n"), "\n")
			lines[2] = tc.edit(lines[2])
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			rep, err := Verify(dir)
			if err != nil {
				t.Fatal(err)
			}
			if rep.OK() || rep.Break.File != FileName || rep.Break.Line != tc.wantLine {
				t.Errorf("Break = %+v, want %s:%d", rep.Break, FileName, tc.wantLine)
			}
		})
	}
}

func TestVerifyDetectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 400})
	for i := range 20 {
		rec := restart("alice")
		rec.Target.Name = fmt.Sprintf("app-%d", i)
		l.Event(rec)
	}
	if rep, err := Verify(dir); err != nil || !rep.OK() {
		t.Fatalf("baseline Verify = %+v, %v", rep, err)
	}
	// Removing a file from the middle of the series breaks the link at the file before
	// the gap, which is where its successor stops matching.
	if err := os.Remove(filepath.Join(dir, FileName+".2")); err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(dir)
	if err != nil || rep.OK() {
		t.Fatalf("Verify = %+v, %v; want a break", rep, err)
	}
	if rep.Break.File != FileName+".3" {
		t.Errorf("Break = %+v, want it in %s.3", rep.Break, FileName)
	}
}

func TestVerifyReportsDanglingIntent(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	done, _ := l.Intent(restart("alice"))
	done.Outcome(nil)
	rec := restart("bob")
	rec.Target.Name = "stuck"
	pending, err := l.Intent(rec)
	if err != nil {
		t.Fatal(err)
	}
	_ = pending // crashed before the outcome

	rep, err := Verify(dir)
	if err != nil || !rep.OK() {
		t.Fatalf("Verify = %+v, %v", rep, err)
	}
	if len(rep.Dangling) != 1 {
		t.Fatalf("Dangling = %+v, want exactly the second intent", rep.Dangling)
	}
	d := rep.Dangling[0]
	if d.ID != pending.rec.ID || d.Target.Name != "stuck" || d.Action != "kubernetes.restart" || d.File != FileName || d.Line != 3 {
		t.Errorf("Dangling[0] = %+v", d)
	}
}

func TestVerifyDetectsDeletedLine(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	for i := range 6 {
		rec := restart("alice")
		rec.Target.Name = fmt.Sprintf("app-%d", i)
		l.Event(rec)
	}
	path := filepath.Join(dir, FileName)
	lines := strings.Split(strings.TrimSuffix(string(mustRead(t, path)), "\n"), "\n")
	lines = append(lines[:2], lines[3:]...) // drop line 3
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The line before the gap is where the chain stops: its successor vouches for the
	// deleted line, not for it.
	if rep.OK() || rep.Break.File != FileName || rep.Break.Line != 2 {
		t.Errorf("Break = %+v, want %s:2", rep.Break, FileName)
	}
}

// A crash mid-write leaves an unterminated last line; it is no record, so it is reported but
// the chain stays intact (the next start truncates it).
func TestVerifyReportsTornTailWithoutBreaking(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{})
	p, _ := l.Intent(restart("alice"))
	p.Outcome(nil)
	l.Close()
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	const torn = `{"time":"2026-11-01T12:00:00Z","kind":"inte`
	f.WriteString(torn)
	f.Close()

	rep, err := Verify(dir)
	if err != nil || !rep.OK() {
		t.Fatalf("Verify = %+v, %v; want the chain intact", rep, err)
	}
	if rep.Records != 2 {
		t.Errorf("Records = %d, want the 2 complete lines", rep.Records)
	}
	if rep.Torn == nil || rep.Torn.File != FileName || rep.Torn.Line != 3 || rep.Torn.Bytes != len(torn) {
		t.Errorf("Torn = %+v, want %s:3 with %d bytes", rep.Torn, FileName, len(torn))
	}
}

func TestVerifyEmptyAndMissingDir(t *testing.T) {
	rep, err := Verify(t.TempDir())
	if err != nil || !rep.OK() || rep.Records != 0 || len(rep.Files) != 0 {
		t.Errorf("empty dir: %+v, %v", rep, err)
	}
	if _, err := Verify(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing dir: want an error")
	}
}

func TestDenialRateLimitPerUserAndReason(t *testing.T) {
	dir := t.TempDir()
	clk := newClock()
	var buf bytes.Buffer
	l := open(t, dir, Options{Now: clk.Now, Logger: jsonLogger(&buf, slog.LevelInfo)})

	rec := Record{Actor: Actor{User: "alice"}, RequestID: "r1", Action: "kubernetes.restart"}
	l.Denial("192.0.2.7", "csrf_failed", rec)
	l.Denial("192.0.2.7", "csrf_failed", rec) // same user and reason: suppressed
	clk.Advance(59 * time.Second)
	l.Denial("192.0.2.7", "csrf_failed", rec)                               // still within the minute
	l.Denial("192.0.2.7", "forbidden", rec)                                 // other reason
	l.Denial("192.0.2.8", "csrf_failed", rec)                               // same user from another peer: suppressed
	l.Denial("192.0.2.7", "csrf_failed", Record{Actor: Actor{User: "bob"}}) // other user behind the same peer
	clk.Advance(2 * time.Second)
	l.Denial("192.0.2.7", "csrf_failed", rec) // window elapsed

	lines := readLines(t, filepath.Join(dir, FileName))
	if len(lines) != 4 {
		t.Fatalf("%d denial records, want 4:\n%s", len(lines), bytes.Join(lines, []byte("\n")))
	}
	first := decode(t, lines[0])
	if first.Kind != KindDenial || first.Peer != "192.0.2.7" || first.Reason != "csrf_failed" || first.RequestID != "r1" {
		t.Errorf("first denial = %+v", first)
	}
	if n := strings.Count(buf.String(), `"msg":"audit"`); n != 4 {
		t.Errorf("%d slog records, want 4: a suppressed repeat must not log", n)
	}
	if rep, err := Verify(dir); err != nil || !rep.OK() {
		t.Errorf("Verify = %+v, %v", rep, err)
	}
}

// A refusal that names no user (an unauthenticated request) is counted per peer, and a user
// named like a peer address is a different key from that peer.
func TestDenialWithoutAUserIsLimitedPerPeer(t *testing.T) {
	dir := t.TempDir()
	clk := newClock()
	l := open(t, dir, Options{Now: clk.Now})

	l.Denial("192.0.2.7", "unauthenticated", Record{})
	l.Denial("192.0.2.7", "unauthenticated", Record{})                                // same peer, no user: suppressed
	l.Denial("192.0.2.8", "unauthenticated", Record{})                                // other peer
	l.Denial("192.0.2.7", "unauthenticated", Record{Actor: Actor{User: "192.0.2.7"}}) // a user named like the peer

	if lines := readLines(t, filepath.Join(dir, FileName)); len(lines) != 3 {
		t.Fatalf("%d denial records, want 3:\n%s", len(lines), bytes.Join(lines, []byte("\n")))
	}
}

func TestDenialTableIsBounded(t *testing.T) {
	clk := newClock()
	l := open(t, "", Options{Now: clk.Now}) // no file: thousands of fsyncs would only slow the test
	for i := range maxDenialKeys + 10 {
		l.Denial(fmt.Sprintf("peer-%d", i), "forbidden", Record{})
	}
	if len(l.denials) != maxDenialKeys {
		t.Errorf("table holds %d keys, want it capped at %d", len(l.denials), maxDenialKeys)
	}
	clk.Advance(2 * time.Minute)
	l.Denial("fresh", "forbidden", Record{})
	if len(l.denials) != 1 {
		t.Errorf("expired keys not pruned: %d left", len(l.denials))
	}
}

func TestScrubAppliesToOutcomeErrorInFile(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	l := open(t, dir, Options{
		Logger: jsonLogger(&buf, slog.LevelInfo),
		Scrub:  func(s string) string { return strings.ReplaceAll(s, "hunter2hunter2", "[redacted]") },
	})
	p, _ := l.Intent(restart("alice"))
	p.Outcome(errors.New("login with hunter2hunter2 failed"))
	last := readLines(t, filepath.Join(dir, FileName))[1]
	if strings.Contains(string(last), "hunter2") || !strings.Contains(string(last), "[redacted]") {
		t.Errorf("file record not scrubbed: %s", last)
	}
}

func TestClosedLogRefusesIntent(t *testing.T) {
	l := open(t, t.TempDir(), Options{})
	l.Close()
	if _, err := l.Intent(restart("alice")); err == nil {
		t.Error("Intent on a closed log returned nil error")
	}
}

func TestConcurrentWritersKeepOneChain(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, Options{MaxSize: 2048, Keep: 100})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 20 {
				rec := restart(fmt.Sprintf("user-%d", g))
				rec.Target.Name = fmt.Sprintf("app-%d", i)
				p, err := l.Intent(rec)
				if err != nil {
					t.Error(err)
					return
				}
				p.Outcome(nil)
			}
		}()
	}
	wg.Wait()
	rep, err := Verify(dir)
	if err != nil || !rep.OK() || len(rep.Dangling) != 0 {
		t.Fatalf("Verify = %+v, %v", rep, err)
	}
	// Keep is large enough that rotation drops nothing: 8 writers x 20 x (intent, outcome).
	if rep.Records != 320 {
		t.Errorf("%d records, want exactly 320", rep.Records)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
