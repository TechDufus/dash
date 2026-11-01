package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Report is the result of Verify.
type Report struct {
	// Files are the files walked, oldest first (base names).
	Files []string
	// Records is the number of lines checked.
	Records int
	// Break is the first place the chain fails, nil when it holds.
	Break *Break
	// Dangling are intents with no outcome record, oldest first. An intent in flight
	// when dash crashed shows up here; so does a change whose outcome could not be
	// written.
	Dangling []Dangling
	// Torn is an unterminated last line of the active file: a crash mid-write. Its writer
	// never got an acknowledgement, so it is not a record and does not break the chain;
	// the next start truncates it.
	Torn *Torn
}

// Torn locates an unterminated final line.
type Torn struct {
	File  string
	Line  int
	Bytes int
}

// OK reports whether the hash chain is intact. Dangling intents do not affect it.
func (r Report) OK() bool { return r.Break == nil }

// Break locates a chain failure.
type Break struct {
	File string
	Line int
	// Reason says what is wrong. A line edited after the fact is reported at that line:
	// the line after it holds the hash of the original.
	Reason string
}

func (b Break) String() string { return fmt.Sprintf("%s:%d: %s", b.File, b.Line, b.Reason) }

// Dangling is an intent without an outcome.
type Dangling struct {
	ID     string
	Action string
	Target Target
	Time   time.Time
	File   string
	Line   int
}

// Verify walks the rotated files oldest to newest, then the active file, and checks
// that every record's prev is the SHA-256 of the line before it. The oldest line
// present anchors the chain: earlier files may have been rotated away. An error means
// the log could not be read; a broken chain is reported in the Report.
func Verify(dir string) (Report, error) {
	var rep Report
	nums, err := rotatedNumbers(dir)
	if err != nil {
		return rep, err
	}
	var names []string
	for _, n := range slices.Backward(nums) {
		names = append(names, FileName+"."+strconv.Itoa(n))
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		names = append(names, FileName)
	} else if !errors.Is(err, os.ErrNotExist) {
		return rep, err
	}
	v := verifier{open: map[string]struct{}{}}
	for _, name := range names {
		rep.Files = append(rep.Files, name)
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return rep, err
		}
		err = v.walk(name, f, &rep)
		f.Close()
		if err != nil {
			return rep, err
		}
		if rep.Break != nil {
			break
		}
	}
	for _, d := range v.order {
		if _, ok := v.open[d.ID]; ok {
			rep.Dangling = append(rep.Dangling, d)
		}
	}
	return rep, nil
}

type verifier struct {
	prevHash string
	prevFile string
	prevLine int
	seen     bool

	open  map[string]struct{} // intent IDs still waiting for an outcome
	order []Dangling
}

func (v *verifier) walk(name string, r io.Reader, rep *Report) error {
	br := bufio.NewReader(r)
	for n := 1; ; n++ {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if err != nil && name == FileName {
			rep.Torn = &Torn{File: name, Line: n, Bytes: len(line)}
			return nil
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		rep.Records++
		var rec Record
		if jerr := json.Unmarshal(line, &rec); jerr != nil {
			rep.Break = &Break{File: name, Line: n, Reason: "not a valid record: " + jerr.Error()}
			return nil
		}
		if v.seen && rec.Prev != v.prevHash {
			rep.Break = &Break{File: v.prevFile, Line: v.prevLine,
				Reason: fmt.Sprintf("%s:%d does not link to this line (edited, deleted or reordered)", name, n)}
			return nil
		}
		switch rec.Kind {
		case KindIntent:
			v.open[rec.ID] = struct{}{}
			v.order = append(v.order, Dangling{ID: rec.ID, Action: rec.Action, Target: rec.Target, Time: rec.Time, File: name, Line: n})
		case KindOutcome:
			delete(v.open, rec.ID)
		}
		v.prevHash, v.prevFile, v.prevLine, v.seen = hashLine(line), name, n, true
	}
}

// rotatedNumbers lists the N of every audit.jsonl.N in dir, ascending (1 is newest).
func rotatedNumbers(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var nums []int
	for _, e := range entries {
		suffix, ok := strings.CutPrefix(e.Name(), FileName+".")
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil && n > 0 && strconv.Itoa(n) == suffix {
			nums = append(nums, n)
		}
	}
	slices.Sort(nums)
	return nums, nil
}
