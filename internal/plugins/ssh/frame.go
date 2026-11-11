package ssh

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Limits of what a host may send. A host past one of them breaks the protocol: dash drops the
// connection and tries again later.
const (
	// maxLineBytes is the longest line dash reads. /proc/stat's interrupt line, the one line of
	// /proc that grows with the machine, is filtered out by the script.
	maxLineBytes = 64 << 10
	// maxFrameBytes is the most one frame may hold: the inventory of a host with hundreds of
	// containers, mounts and disks is well below it.
	maxFrameBytes = 4 << 20
	// maxPreambleLines is how many lines before the first frame dash skips: a login shell's rc
	// file may print something before the script starts (bash run by sshd reads ~/.bashrc).
	maxPreambleLines = 200
)

// errProtocol is every way a host's output breaks the frame protocol.
var errProtocol = errors.New("protocol violation")

func protocolError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errProtocol, fmt.Sprintf(format, args...))
}

// frame is one frame of the collector script: the raw lines of each section it printed.
type frame struct {
	seq      string
	sections map[string][]string
}

// has reports whether the frame carries section name (an inventory frame carries "uname").
func (f frame) has(name string) bool {
	_, ok := f.sections[name]
	return ok
}

// frameReader reads frames from a host's output.
type frameReader struct {
	r      *bufio.Reader
	marker []byte // "@@<nonce>"
	// started is set once the first frame began: a line outside a frame after it is a violation.
	started bool
}

func newFrameReader(r io.Reader, nonce string) *frameReader {
	return &frameReader{r: bufio.NewReaderSize(r, maxLineBytes), marker: []byte("@@" + nonce)}
}

// line reads one line without its end. A line longer than maxLineBytes is a violation.
func (fr *frameReader) line() ([]byte, error) {
	b, err := fr.r.ReadSlice('\n')
	switch {
	case errors.Is(err, bufio.ErrBufferFull):
		return nil, protocolError("a line is longer than %d bytes", maxLineBytes)
	case err == io.EOF && len(b) > 0:
		return nil, io.ErrUnexpectedEOF
	case err != nil:
		return nil, err
	}
	return bytes.TrimSuffix(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\r'}), nil
}

// marked reports whether l is a marker line and returns what follows the marker.
func (fr *frameReader) marked(l []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(l, fr.marker)
	if !ok || len(rest) > 0 && rest[0] != ' ' {
		return "", false
	}
	return string(bytes.TrimSpace(rest)), true
}

// next reads one whole frame. io.EOF means the output ended between frames.
func (fr *frameReader) next() (frame, error) {
	// The frame's start, after whatever a login shell printed before the first one.
	var seq string
	for skipped := 0; ; skipped++ {
		l, err := fr.line()
		if err != nil {
			return frame{}, err
		}
		if m, ok := fr.marked(l); ok {
			s, isFrame := strings.CutPrefix(m, "frame ")
			if !isFrame {
				return frame{}, protocolError("a frame starts with a section marker")
			}
			seq = s
			break
		}
		if fr.started || skipped >= maxPreambleLines {
			return frame{}, protocolError("a line outside a frame")
		}
	}
	fr.started = true
	f := frame{seq: seq, sections: map[string][]string{}}
	size, section := 0, ""
	for {
		l, err := fr.line()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return frame{}, err
		}
		if size += len(l) + 1; size > maxFrameBytes {
			return frame{}, protocolError("a frame is larger than %d bytes", maxFrameBytes)
		}
		m, ok := fr.marked(l)
		switch {
		case ok && m == "end":
			return f, nil
		case ok && (m == "" || strings.HasPrefix(m, "frame")):
			return frame{}, protocolError("a frame starts before the last one ended")
		case ok:
			section = m
			if _, dup := f.sections[section]; dup {
				return frame{}, protocolError("a section appears twice in a frame")
			}
			f.sections[section] = []string{}
		case section == "":
			return frame{}, protocolError("a line before the frame's first section")
		default:
			f.sections[section] = append(f.sections[section], string(l))
		}
	}
}
