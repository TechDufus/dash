package secret

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// minEncodedLen is the shortest secret whose encoded forms are scrubbed. A
// shorter secret would make its base64 or percent-encoded form a common
// substring of unrelated text.
const minEncodedLen = 8

// shapes match credentials by format, whether or not they are registered
// (design C.6). Token characters follow the prefix: base64url for the dash_*
// tokens, base64 std plus ':' for the enc:v1:<kid>:<payload> form.
var (
	shapes    = regexp.MustCompile(`\A(?:dash_(?:pat|at|rt)_[A-Za-z0-9_-]+|enc:v1:[A-Za-z0-9+/=:_-]+)`)
	logShapes = regexp.MustCompile(`\A(?:dash_(?:pat|at|rt|setup)_[A-Za-z0-9_-]+|enc:v1:[A-Za-z0-9+/=:_-]+)`)
)

// Registry knows every secret currently in use and scrubs them from text. It
// tracks Values by reference, so a rotation through Value.Update is picked up
// without re-registering. It is safe for concurrent use; a nil *Registry only
// scrubs by shape.
type Registry struct {
	mu      sync.RWMutex
	tracked []*holder
	fixed   []string

	// forms is compiled lazily: every registered value and its encoded forms. stamp is the
	// version sum it was compiled at, so a Value.Update (which bumps its version) invalidates it.
	built bool
	forms []string // nil when nothing is registered
	stamp uint64
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

// Add tracks v. Adding the zero Value or the same Value twice is harmless.
func (r *Registry) Add(v Value) {
	if v.h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, h := range r.tracked {
		if h == v.h {
			return
		}
	}
	r.tracked = append(r.tracked, v.h)
	r.built = false
}

// AddString registers a secret that is not held in a Value, such as the
// DASH_SECRET_KEY text. It never rotates.
func (r *Registry) AddString(s string) {
	if s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fixed = append(r.fixed, s)
	r.built = false
}

// Scrub replaces every registered secret, its encoded forms, and credentials
// that match a known token shape (dash_pat_, dash_at_, dash_rt_, enc:v1:) with
// "[redacted]".
func (r *Registry) Scrub(s string) string {
	return r.scrub(s, shapes)
}

// ScrubLogLine is Scrub plus dash_setup_ tokens. The logs API uses it on every
// line it returns; the slog handler does not, because the one-time setup
// token must stay readable in the startup log (design C.6).
func (r *Registry) ScrubLogLine(s string) string {
	return r.scrub(s, logShapes)
}

// span is a byte range [start, end) of the text to redact.
type span struct{ start, end int }

// scrub redacts the union of what the registered secrets and the shapes match, both found on
// the original text: the spans are merged where they overlap or touch, and each merged span
// becomes one "[redacted]". No order of two replacing passes is safe. Literals first let a short
// secret (one character of "dash_setup_") break a token apart before its shape could match;
// shapes first left the part of a secret before an embedded token readable; and a token that
// straddles the end of a secret lost its shape either way. Text in which nothing overlaps or
// touches comes out as it did with either order.
func (r *Registry) scrub(s string, re *regexp.Regexp) string {
	if s == "" {
		return s
	}
	spans := spansOf(s, re, r.formList())
	if len(spans) == 0 {
		return s
	}
	return redact(s, spans, len(s))
}

// spansOf returns, sorted by start, the spans of every shape match and of every occurrence of
// every form in s.
func spansOf(s string, re *regexp.Regexp, forms []string) []span {
	spans := shapeSpans(s, re)
	for _, f := range forms {
		// Every occurrence, overlapping ones included: the next search starts one byte on.
		for i := 0; i < len(s); {
			j := strings.Index(s[i:], f)
			if j < 0 {
				break
			}
			spans = append(spans, span{i + j, i + j + len(f)})
			i += j + 1
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	return spans
}

// redact returns s[:end] with the spans (sorted by start) merged where they overlap or touch and
// each merged span that starts before end written as one "[redacted]". A merged span that reaches
// end or runs past it ends the text right after its marker.
func redact(s string, spans []span, end int) string {
	var b strings.Builder
	b.Grow(end)
	done := 0
	for i := 0; i < len(spans) && spans[i].start < end; {
		cur := spans[i]
		for i++; i < len(spans) && spans[i].start <= cur.end; i++ {
			cur.end = max(cur.end, spans[i].end)
		}
		b.WriteString(s[done:cur.start])
		b.WriteString(redacted)
		done = cur.end
		if done >= end {
			return b.String()
		}
	}
	b.WriteString(s[done:end])
	return b.String()
}

// formList is literals for a registry, nil for a nil one (which only matches shapes).
func (r *Registry) formList() []string {
	if r == nil {
		return nil
	}
	return r.literals()
}

// shapeAttempt, when not nil, is called for every anchored shape match scrub tries (a test
// counts them).
var shapeAttempt func()

// shapeSpans returns the spans of every shape match, overlapping starts included, in O(len(s)).
// Every shape starts with a hint, so the shapes are anchored and tried at hint positions in
// order. A hint inside a span already matched is skipped when it cannot extend it: every prefix
// byte is in its own token class, and the dash_ class is a subset of the enc:v1: class, so a
// match starting inside an enc:v1: span, or a dash_ match inside a dash_ span, ends at or before
// that span's end. Only an enc:v1: anchor inside a dash_ span is tried (dash_pat_xenc:v1:…).
// The tried dash_ matches then never overlap each other, nor do the enc:v1: ones, so the
// regexp reads each byte at most twice.
func shapeSpans(s string, re *regexp.Regexp) []span {
	var spans []span
	dash, enc := strings.Index(s, "dash_"), strings.Index(s, "enc:v1:")
	dashEnd, encEnd := 0, 0
	for dash >= 0 || enc >= 0 {
		isEnc := enc >= 0 && (dash < 0 || enc < dash)
		q := dash
		if isEnc {
			q = enc
		}
		if q >= encEnd && (isEnc || q >= dashEnd) {
			if shapeAttempt != nil {
				shapeAttempt()
			}
			if m := re.FindStringIndex(s[q:]); m != nil {
				spans = append(spans, span{q, q + m[1]})
				if isEnc {
					encEnd = q + m[1]
				} else {
					dashEnd = q + m[1]
				}
			}
		}
		if isEnc {
			enc = nextIndex(s, "enc:v1:", q)
		} else {
			dash = nextIndex(s, "dash_", q)
		}
	}
	return spans
}

// nextIndex is the next index of hint in s after q, -1 when there is none.
func nextIndex(s, hint string, q int) int {
	if j := strings.Index(s[q+1:], hint); j >= 0 {
		return q + 1 + j
	}
	return -1
}

// literals returns the compiled forms, rebuilding them when a tracked Value changed since the
// last build.
func (r *Registry) literals() []string {
	r.mu.RLock()
	if r.built && r.stamp == r.versionSum() {
		forms := r.forms
		r.mu.RUnlock()
		return forms
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuild()
	return r.forms
}

func (r *Registry) versionSum() uint64 {
	sum := uint64(len(r.tracked) + len(r.fixed))
	for _, h := range r.tracked {
		sum += h.currentVersion()
	}
	return sum
}

// rebuild compiles the forms, longest first (the order only makes a build deterministic: the
// spans of every form are merged). It leaves forms nil when there is nothing to match. Callers
// hold r.mu for writing.
func (r *Registry) rebuild() {
	forms := map[string]struct{}{}
	sum := uint64(len(r.tracked) + len(r.fixed))
	for _, h := range r.tracked {
		values, version := h.snapshot()
		sum += version
		for _, v := range values {
			addForms(forms, v)
		}
	}
	for _, v := range r.fixed {
		addForms(forms, v)
	}
	r.stamp = sum
	r.built = true
	r.forms = nil
	if len(forms) == 0 {
		return
	}
	list := make([]string, 0, len(forms))
	for f := range forms {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	r.forms = list
}

func addForms(forms map[string]struct{}, v string) {
	if v == "" {
		return
	}
	forms[v] = struct{}{}
	if len(v) < minEncodedLen {
		return
	}
	b := []byte(v)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	} {
		forms[enc.EncodeToString(b)] = struct{}{}
	}
	forms[url.QueryEscape(v)] = struct{}{}
	forms[url.PathEscape(v)] = struct{}{}
}
