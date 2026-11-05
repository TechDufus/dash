package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// Ring is one of the two lists Recent serves. Denials, failed logins among them, are kept apart
// from everything else, so a flood of refused requests cannot push what signed-in users did out
// of sight (G4-R2-S9).
type Ring string

const (
	// RingMain is every record but denials: actions, settings changes, events, intents and
	// outcomes.
	RingMain Ring = "main"
	// RingUnauth is the denials, failed logins included.
	RingUnauth Ring = "unauth"
)

// Where Recent found its records.
const (
	// SourceFile is audit.jsonl and its rotated files.
	SourceFile = "file"
	// SourceMemory is the in-memory rings of a log without a file.
	SourceMemory = "memory"
)

const (
	// The rings of a log without a file keep this many of the latest records each.
	mainRingSize   = 500
	unauthRingSize = 200

	// A ring's copy of a record keeps at most this many changed paths, then one "+N more"
	// entry, and this many bytes of its error, so a settings write of many paths or a long
	// error cannot make the rings outgrow their 1 MiB bound. The file keeps whole records.
	maxRingFields = 32
	maxRingError  = 256

	// readChunk is how much of a file Recent reads at a time, from its end.
	readChunk = 64 << 10
)

// Recent lists the latest records of one ring, newest first: at most limit of them, all older
// than the record the cursor before names, or the newest ones when before is empty. next is the
// cursor of the oldest record listed when older ones exist, and "" otherwise. A cursor that names
// no record Recent can still see lists nothing. Prev is cleared in every record.
//
// With a file (source SourceFile) Recent reads audit.jsonl and then its rotated files, newest
// first, each from its end and without holding up writers. It follows the hash chain down from
// the newest record written when it was called; a cursor is the hex SHA-256 of a record's line
// as written (the next line's prev). The chain keeps the listing exact while the log moves
// underneath: a torn final line, a record written during the read and a file seen twice because
// the log rotated mid-read never link into it. A break in the chain (Verify names it) ends the
// listing there.
//
// Without a file (source SourceMemory: no data dir, or a missing or unwritable one) it reads the
// in-memory rings, which keep the latest 500 records of RingMain and 200 of RingUnauth and drop
// the oldest; a cursor is a record's number within its ring. A nil Log lists nothing.
func (l *Log) Recent(limit int, before string, ring Ring) (recs []Record, next string, source string) {
	source = SourceMemory
	if l.Enabled() {
		source = SourceFile
	}
	if limit <= 0 || (ring != RingMain && ring != RingUnauth) {
		return nil, "", source
	}
	if source == SourceFile {
		recs, next = l.recentFile(limit, before, ring)
	} else {
		recs, next = l.recentMemory(limit, before, ring)
	}
	return recs, next, source
}

// ringOf is the ring a record belongs to, with or without a file.
func ringOf(rec Record) Ring {
	if rec.Kind == KindDenial {
		return RingUnauth
	}
	return RingMain
}

func (l *Log) recentMemory(limit int, before string, ring Ring) ([]Record, string) {
	if l == nil {
		return nil, ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mem == nil {
		return nil, ""
	}
	return l.mem.of(ring).recent(limit, before)
}

// recentFile follows the hash chain down from the newest record written so far.
func (l *Log) recentFile(limit int, before string, ring Ring) ([]Record, string) {
	l.mu.Lock()
	head := l.last
	l.mu.Unlock()
	var (
		recs   []Record
		oldest string // the cursor of recs' last record
		next   string
		passed = before == ""
	)
	err := l.walkChain(head, func(sum string, rec Record) bool {
		if !passed {
			passed = sum == before
			return true
		}
		if ringOf(rec) != ring {
			return true
		}
		if len(recs) == limit {
			next = oldest // one more exists, so this page is not the last
			return false
		}
		rec.Prev = ""
		recs, oldest = append(recs, rec), sum
		return true
	})
	if err != nil {
		l.logger().Error("audit read failed", "error", err.Error())
	}
	if !passed {
		return nil, ""
	}
	return recs, next
}

// walkChain calls fn with the records of the hash chain that ends at head, newest first, each
// with the hex SHA-256 of its line, until fn returns false or the chain ends: at the first record
// ever written (an empty prev), or past the oldest rotated file. It reads audit.jsonl and then
// .1 to .Keep, each from its end, without l.mu. A line is taken only when it hashes to the prev
// of the line taken before it; every other line is passed over, so is everything behind a break.
func (l *Log) walkChain(head string, fn func(sum string, rec Record) bool) error {
	want, done := head, head == ""
	var buf []byte
	for n := 0; n <= l.opts.Keep && !done; n++ {
		f, err := l.openRead(l.path(n))
		if errors.Is(err, os.ErrNotExist) {
			continue // not rotated that far yet, or being renamed by a rotation right now
		}
		if err != nil {
			return err
		}
		buf, err = backward(f, buf, func(line []byte) bool {
			sum := hashLine(line)
			if sum != want {
				return true
			}
			var rec Record
			if json.Unmarshal(line, &rec) != nil {
				done = true // a line dash wrote always decodes
				return false
			}
			keep := fn(sum, rec)
			want = rec.Prev
			done = !keep || want == ""
			return !done
		})
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// backward calls fn with each line of f, the last one first and without its newline, until fn
// returns false. The bytes after the last newline (a torn line, or nothing) come first. line is
// only valid during the call. buf is scratch space, returned for reuse.
func backward(f *os.File, buf []byte, fn func(line []byte) bool) ([]byte, error) {
	fi, err := f.Stat()
	if err != nil {
		return buf, err
	}
	rest := 0 // buf[:rest] is the end of a line that starts before pos
	for pos := fi.Size(); pos > 0; {
		n := int(min(readChunk, pos))
		pos -= int64(n)
		need := n + rest
		if cap(buf) < need {
			grown := make([]byte, need, 2*need)
			copy(grown[n:], buf[:rest])
			buf = grown
		} else {
			buf = buf[:need]
			copy(buf[n:], buf[:rest])
		}
		k, err := f.ReadAt(buf[:n], pos)
		if err != nil && !errors.Is(err, io.EOF) {
			return buf, err
		}
		data := buf
		if k < n {
			// The file shrank since Stat (a failed write rolled back): what lay past its new
			// end is gone, and it was never part of the chain.
			data = buf[:k]
		}
		for {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			if !fn(data[i+1:]) {
				return buf, nil
			}
			data = data[:i]
		}
		rest = len(data)
	}
	if rest > 0 {
		fn(buf[:rest])
	}
	return buf, nil
}

// rings keep the latest records of a log without a file, where stdout is the only other trail.
// They are guarded by Log.mu.
type rings struct{ main, unauth ring }

func newRings() *rings {
	return &rings{main: ring{size: mainRingSize}, unauth: ring{size: unauthRingSize}}
}

func (m *rings) of(r Ring) *ring {
	if r == RingUnauth {
		return &m.unauth
	}
	return &m.main
}

func (m *rings) push(rec Record) { m.of(ringOf(rec)).push(rec) }

// ring keeps the latest size records of one Ring and drops the oldest. Record number k, counted
// from 1 since Open, sits at recs[(k-1)%size]; its number is its cursor.
type ring struct {
	size int
	recs []Record
	n    uint64 // records pushed so far: the newest is number n
}

func (r *ring) push(rec Record) {
	rec = bounded(rec)
	if len(r.recs) < r.size {
		if r.recs == nil {
			r.recs = make([]Record, 0, r.size)
		}
		r.recs = append(r.recs, rec)
	} else {
		r.recs[r.n%uint64(r.size)] = rec
	}
	r.n++
}

// bounded is rec as a ring keeps it: its own copy of at most maxRingFields paths, the number of
// the others as a last "+N more" entry (a count, never a path), and its error cut to maxRingError
// bytes. The error was scrubbed already; the cut still goes through secret.Registry.Cut, so no
// token shape is left in half, and never splits a character.
func bounded(rec Record) Record {
	if n := len(rec.Fields); n > maxRingFields {
		f := make([]string, maxRingFields+1)
		copy(f, rec.Fields[:maxRingFields])
		f[maxRingFields] = "+" + strconv.Itoa(n-maxRingFields) + " more"
		rec.Fields = f
	} else {
		rec.Fields = slices.Clone(rec.Fields) // the caller may reuse its slice
	}
	if out, cut := (*secret.Registry)(nil).Cut(rec.Error, maxRingError); cut {
		rec.Error = strings.ToValidUTF8(out, "")
	}
	return rec
}

// recent lists up to limit of the held records older than the cursor before (from the newest
// when it is empty), newest first, and the cursor of the next page.
func (r *ring) recent(limit int, before string) ([]Record, string) {
	from := r.n
	if before != "" {
		k, err := strconv.ParseUint(before, 10, 64)
		if err != nil || k == 0 || k > r.n || strconv.FormatUint(k, 10) != before {
			return nil, ""
		}
		from = k - 1
	}
	oldest := r.n - uint64(len(r.recs)) + 1
	if from < oldest {
		return nil, ""
	}
	count := min(uint64(limit), from-oldest+1)
	recs := make([]Record, 0, count)
	for k := from; k > from-count; k-- {
		rec := r.recs[(k-1)%uint64(r.size)]
		rec.Fields = slices.Clone(rec.Fields) // the ring's copy stays the ring's
		rec.Prev = ""
		recs = append(recs, rec)
	}
	var next string
	if last := from - count + 1; last > oldest {
		next = strconv.FormatUint(last, 10)
	}
	return recs, next
}
