// Package audit is dash's persistent audit log (epic P1.8, design C.7).
//
// Every record goes to the structured "audit" slog record the dashboard has always
// emitted and, when the data dir is writable, to a JSONL file. A record names who did
// what to which target and with which result; it never holds values, only the field
// paths a change touched.
//
// Each file line carries "prev", the hex SHA-256 of the previous line exactly as
// written, so an edited, deleted or reordered line is detectable (Verify). The chain
// runs across restarts and across rotated files. It cannot see an edit to the very
// last line, nor a truncation of the log's tail, because no later line vouches for
// them; it proves history was not rewritten, not that the tail is complete.
//
// Writes that change state are bracketed: Intent is written (and fsynced) before the
// change and returns an error the caller turns into a 503, so nothing changes without a
// durable record; Pending.Outcome follows the change and cannot fail the request.
// Auth events and denials are best effort.
package audit

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileName is the active log file inside the data dir. Rotated files are FileName.1
// (newest) to FileName.<Keep> (oldest).
const FileName = "audit.jsonl"

const (
	defaultMaxSize = 10 << 20
	defaultKeep    = 5

	// denialWindow is the rate limit for denial records per (peer, reason).
	denialWindow = time.Minute
	// maxDenialKeys bounds the rate-limit table against a flood of distinct peers.
	// Past it, new keys are dropped rather than recorded: denials are best effort.
	maxDenialKeys = 8192
)

// Kind classifies a record.
type Kind string

const (
	// KindIntent precedes a change; KindOutcome follows it, with the same ID.
	KindIntent  Kind = "intent"
	KindOutcome Kind = "outcome"
	// KindEvent is a single-step auth event (login, logout, setup).
	KindEvent Kind = "event"
	// KindDenial is a refused request (403, CSRF failure, rate limit).
	KindDenial Kind = "denial"
)

// Result values of an outcome or event.
const (
	ResultOK    = "ok"
	ResultError = "error"
)

// Actor identifies who acted. Surface is ui, api or mcp.
type Actor struct {
	Type     string `json:"type,omitempty"`
	User     string `json:"user,omitempty"`
	TokenID  string `json:"tokenID,omitempty"`
	GrantID  string `json:"grantID,omitempty"`
	ClientID string `json:"clientID,omitempty"`
	Surface  string `json:"surface,omitempty"`
}

// Target is the object an action or change applies to.
type Target struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// Record is one JSONL line. Callers fill the descriptive fields; Time, Kind, ID and
// Prev are set by the Log. Fields lists changed config paths, never their values.
type Record struct {
	Time      time.Time `json:"time"`
	Kind      Kind      `json:"kind"`
	ID        string    `json:"id,omitempty"`
	Action    string    `json:"action,omitempty"`
	Target    Target    `json:"target,omitzero"`
	Fields    []string  `json:"fields,omitempty"`
	Source    string    `json:"source,omitempty"`
	Result    string    `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
	Actor     Actor     `json:"actor,omitzero"`
	Peer      string    `json:"peer,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	AuthMode  string    `json:"authMode,omitempty"`
	Plugin    string    `json:"plugin,omitempty"` // the plugin instance behind a framework action
	Reason    string    `json:"reason,omitempty"`
	Prev      string    `json:"prev"`
}

// Options configure Open. The zero value is production-ready.
type Options struct {
	// Now is the clock for record times and the denial rate limit (default time.Now).
	Now func() time.Time
	// MaxSize rotates the active file once a record would push it past this many bytes
	// (default 10 MiB).
	MaxSize int64
	// Keep is the number of rotated files retained besides the active one (default 5).
	Keep int
	// Logger receives the slog side of every record (default slog.Default(), resolved
	// per call so a later slog.SetDefault is honoured).
	Logger *slog.Logger
	// Scrub is applied to error text before it is written to the file. The slog side
	// is scrubbed by the logger's own handler; the file has no such layer.
	Scrub func(string) string
}

// Log is the audit log. A nil *Log behaves like a slog-only log.
type Log struct {
	opts Options
	dir  string // empty when the file is disabled

	mu      sync.Mutex
	f       *os.File
	closed  bool
	size    int64
	last    string // hex SHA-256 of the last line written
	denials map[denialKey]time.Time
	lock    *os.File // flock on dir/.audit.lock, held for the log's lifetime
}

// ErrLocked means another dash process holds the data directory's audit log.
var ErrLocked = errors.New("audit log is locked by another dash instance")

// syncDir makes a created or renamed directory entry durable. A variable so a test can see
// that creating the log file calls it.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writable reports whether dir accepts new files. Creating a probe is the only reliable test:
// mode bits ignore read-only mounts and ACLs (the settings store probes the same way).
func writable(dir string) bool {
	f, err := os.CreateTemp(dir, ".audit-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	return os.Remove(f.Name()) == nil
}

type denialKey struct{ peer, reason string }

// Open starts the audit log in dir. A missing, empty or unwritable dir yields a
// slog-only log (Enabled reports false); that is not an error, so an unconfigured data
// dir keeps today's behaviour. In a writable dir the log takes an flock of its own, so a
// second dash on the same dir is refused (ErrLocked) even when its settings are read-only.
// Any other failure there is an error and the caller should refuse to start: a log file that
// cannot be opened must not silently turn into slog-only (the 503 rule would be void), and
// one that cannot be read to continue its hash chain must not be forked.
func Open(dir string, opts Options) (*Log, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxSize <= 0 {
		opts.MaxSize = defaultMaxSize
	}
	if opts.Keep <= 0 {
		opts.Keep = defaultKeep
	}
	l := &Log{opts: opts, denials: make(map[denialKey]time.Time)}
	if dir == "" {
		l.logger().Info("audit log file disabled, logging to slog only", "reason", "no data dir")
		return l, nil
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		l.logger().Info("audit log file disabled, logging to slog only", "dir", dir, "reason", "data dir is missing")
		return l, nil
	}
	if !writable(dir) {
		l.logger().Info("audit log file disabled, logging to slog only", "dir", dir, "reason", "data dir is not writable")
		return l, nil
	}
	lock, err := acquireLock(dir)
	if err != nil {
		return nil, fmt.Errorf("audit log %s: %w", dir, err)
	}
	l.dir, l.lock = dir, lock
	if err := l.openActive(); err != nil {
		lock.Close()
		return nil, fmt.Errorf("audit log %s: %w", filepath.Join(dir, FileName), err)
	}
	if err := l.restoreChain(); err != nil {
		l.f.Close()
		lock.Close()
		return nil, fmt.Errorf("audit log %s: %w", filepath.Join(dir, FileName), err)
	}
	return l, nil
}

// Enabled reports whether records are persisted to the JSONL file.
func (l *Log) Enabled() bool { return l != nil && l.dir != "" }

// Close releases the file. Later file writes fail; slog output continues.
func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	var err error
	if l.f != nil {
		err = l.f.Close()
		l.f = nil
	}
	if l.lock != nil {
		l.lock.Close() // drops the flock
		l.lock = nil
	}
	return err
}

func (l *Log) logger() *slog.Logger {
	if l != nil && l.opts.Logger != nil {
		return l.opts.Logger
	}
	return slog.Default()
}

func (l *Log) now() time.Time {
	if l == nil || l.opts.Now == nil {
		return time.Now().UTC()
	}
	return l.opts.Now().UTC()
}

// Pending is an intent awaiting its outcome. The zero value (returned with an Intent
// error) ignores Outcome.
type Pending struct {
	l    *Log
	rec  Record
	once *sync.Once
}

// Intent records that an action or change is about to happen. The error is non-nil
// only when the file is enabled and the record could not be durably written; the
// caller must then fail closed (503) and change nothing. Without a file it always
// succeeds and the returned Pending still produces the slog record.
func (l *Log) Intent(rec Record) (Pending, error) {
	rec.Kind = KindIntent
	rec.ID = newID()
	rec.Result, rec.Error, rec.Reason = "", "", ""
	rec.Time = l.now()
	if err := l.append(rec); err != nil {
		l.logger().Error("audit write failed", "kind", string(KindIntent), "action", rec.Action, "error", err.Error())
		return Pending{}, fmt.Errorf("audit intent: %w", err)
	}
	return Pending{l: l, rec: rec, once: new(sync.Once)}, nil
}

// Outcome records how the change ended; a nil err is a success. It also emits the
// slog "audit" record. A failed file write is logged at ERROR and never returned: the
// change already happened and the response must report its real result. Only the first
// call has an effect.
func (p Pending) Outcome(err error) {
	if p.once == nil {
		return
	}
	p.once.Do(func() {
		rec := p.rec
		rec.Kind = KindOutcome
		rec.Result, rec.Error = result(err)
		rec.Time = p.l.now()
		p.l.emit(rec, "")
		if aerr := p.l.append(p.l.scrubbed(rec)); aerr != nil {
			p.l.logger().Error("audit write failed", "kind", string(KindOutcome), "action", rec.Action, "id", rec.ID, "error", aerr.Error())
		}
	})
}

// WithTarget returns p with the target its outcome reports. An action that learns the
// object's identity only by acting (a created silence's ID) records the intent against what
// it knew and the outcome against what exists. The copy shares p's once, so the outcome is
// still written at most once.
func (p Pending) WithTarget(t Target) Pending {
	p.rec.Target = t
	return p
}

// Event records a single-step auth event (login, logout, failed login, setup) and
// emits the slog record. It is best effort: a failed write is logged at ERROR and
// never blocks the caller. An empty Result means ok.
func (l *Log) Event(rec Record) {
	rec.Kind = KindEvent
	rec.Time = l.now()
	if rec.Result == "" {
		rec.Result = ResultOK
	}
	l.emit(rec, "")
	l.bestEffort(rec)
}

// Denial records a refused request, best effort, at most once per peer and reason per
// minute so an attacker cannot fill the log; a suppressed repeat leaves no slog record
// either. rec carries the actor, request ID and target when the caller knows them.
func (l *Log) Denial(peer, reason string, rec Record) {
	if !l.allowDenial(peer, reason) {
		return
	}
	rec.Kind = KindDenial
	rec.Time = l.now()
	rec.Peer, rec.Reason = peer, reason
	l.emit(rec, reason)
	l.bestEffort(rec)
}

func (l *Log) allowDenial(peer, reason string) bool {
	if l == nil {
		return true
	}
	now := l.now()
	key := denialKey{peer, reason}
	l.mu.Lock()
	defer l.mu.Unlock()
	if at, ok := l.denials[key]; ok && now.Sub(at) < denialWindow {
		return false
	}
	if len(l.denials) >= maxDenialKeys {
		for k, at := range l.denials {
			if now.Sub(at) >= denialWindow {
				delete(l.denials, k)
			}
		}
		if len(l.denials) >= maxDenialKeys {
			return false
		}
	}
	l.denials[key] = now
	return true
}

func (l *Log) bestEffort(rec Record) {
	if err := l.append(l.scrubbed(rec)); err != nil {
		l.logger().Error("audit write failed", "kind", string(rec.Kind), "action", rec.Action, "error", err.Error())
	}
}

func (l *Log) scrubbed(rec Record) Record {
	if l != nil && l.opts.Scrub != nil {
		rec.Error = l.opts.Scrub(rec.Error)
	}
	return rec
}

// emit writes the slog record. Its shape for outcomes is the helper dash has always
// had in internal/api/actions.go (user, action, kind, namespace, name, result, error);
// logs and dashboards built on it keep working. A denial adds peer and reason.
func (l *Log) emit(rec Record, denialReason string) {
	attrs := []any{"user", rec.Actor.User, "action", rec.Action, "kind", rec.Target.Kind,
		"namespace", rec.Target.Namespace, "name", rec.Target.Name, "result", rec.Result, "error", rec.Error}
	if rec.Plugin != "" {
		attrs = append(attrs, "plugin", rec.Plugin)
	}
	if denialReason != "" {
		attrs = append(attrs, "peer", rec.Peer, "reason", denialReason)
	}
	l.logger().Info("audit", attrs...)
}

func result(err error) (string, string) {
	if err != nil {
		return ResultError, err.Error()
	}
	return ResultOK, ""
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // never fails since Go 1.24
	return hex.EncodeToString(b[:])
}

func hashLine(line []byte) string {
	sum := sha256.Sum256(line)
	return hex.EncodeToString(sum[:])
}

// append chains rec to the previous line and writes it durably. It does nothing when
// the file is disabled. A failed or partial write is rolled back so the next record
// does not land after a torn line, and the chain head only advances on success.
func (l *Log) append(rec Record) error {
	if !l.Enabled() {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("audit log is closed")
	}
	rec.Prev = l.last
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	buf := append(line, '\n')
	if l.f != nil && l.size > 0 && l.size+int64(len(buf)) > l.opts.MaxSize {
		if err := l.rotate(); err != nil {
			return err
		}
	}
	if l.f == nil {
		if err := l.openActive(); err != nil {
			return err
		}
	}
	if n, err := l.f.Write(buf); err != nil {
		if n > 0 {
			_ = l.f.Truncate(l.size)
		}
		return err
	}
	if err := l.f.Sync(); err != nil {
		_ = l.f.Truncate(l.size)
		return err
	}
	l.size += int64(len(buf))
	l.last = hashLine(line)
	return nil
}

func (l *Log) path(n int) string {
	p := filepath.Join(l.dir, FileName)
	if n > 0 {
		p += "." + fmt.Sprint(n)
	}
	return p
}

func (l *Log) openActive() error {
	f, err := os.OpenFile(l.path(0), os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if fi.Size() == 0 {
		// Possibly just created: a crash before the directory entry is durable would lose
		// the file and with it every record fsynced into it.
		if err := syncDir(l.dir); err != nil {
			f.Close()
			return err
		}
	}
	l.f, l.size = f, fi.Size()
	return nil
}

// rotate shifts audit.jsonl.N to .N+1 (dropping the oldest), moves the active file to
// .1 and opens a fresh one. The chain head stays in memory, so the new file's first
// record links to the last line of .1.
func (l *Log) rotate() error {
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return err
	}
	for n := l.opts.Keep - 1; n >= 1; n-- {
		if err := os.Rename(l.path(n), l.path(n+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(l.path(0), l.path(1)); err != nil {
		return err
	}
	if err := syncDir(l.dir); err != nil {
		return err
	}
	return l.openActive()
}

// restoreChain learns the chain head from the newest complete line on disk, and
// truncates a torn final line (a crash mid-write; its writer never got an
// acknowledgement, so it is not a record).
func (l *Log) restoreChain() error {
	last, end, err := scanTail(io.NewSectionReader(l.f, 0, l.size))
	if err != nil {
		return err
	}
	if end < l.size {
		if err := l.f.Truncate(end); err != nil {
			return err
		}
		l.size = end
	}
	if last == nil {
		// A fresh active file after rotation or a crash before the first write: the
		// head is the newest rotated file's last line.
		nums, err := rotatedNumbers(l.dir)
		if err != nil {
			return err
		}
		if len(nums) == 0 {
			return nil
		}
		f, err := os.Open(l.path(nums[0])) // ascending: .1 is the newest
		if err != nil {
			return err
		}
		defer f.Close()
		if last, _, err = scanTail(f); err != nil {
			return err
		}
	}
	if last != nil {
		l.last = hashLine(last)
	}
	return nil
}

// scanTail returns the last complete (newline-terminated) line, without its newline,
// and the offset just past it. last is nil when there is no complete line.
func scanTail(r io.Reader) (last []byte, end int64, err error) {
	br := bufio.NewReader(r)
	for {
		line, rerr := br.ReadBytes('\n')
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return last, end, nil
			}
			return nil, 0, rerr
		}
		end += int64(len(line))
		last = line[:len(line)-1]
	}
}
