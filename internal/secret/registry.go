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
	shapes     = regexp.MustCompile(`(?:dash_(?:pat|at|rt)_[A-Za-z0-9_-]+|enc:v1:[A-Za-z0-9+/=:_-]+)`)
	logShapes  = regexp.MustCompile(`(?:dash_(?:pat|at|rt|setup)_[A-Za-z0-9_-]+|enc:v1:[A-Za-z0-9+/=:_-]+)`)
	shapeHints = []string{"dash_", "enc:v1:"}
)

// Registry knows every secret currently in use and scrubs them from text. It
// tracks Values by reference, so a rotation through Value.Update is picked up
// without re-registering. It is safe for concurrent use; a nil *Registry only
// scrubs by shape.
type Registry struct {
	mu      sync.RWMutex
	tracked []*holder
	fixed   []string

	// repl is compiled lazily. stamp is the version sum it was compiled at,
	// so a Value.Update (which bumps its version) invalidates it.
	built bool
	repl  *strings.Replacer // nil when nothing is registered
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

// scrub redacts the shapes first, then the registered secrets. The other order let a short
// registered secret (one character of "dash_setup_", say) break a token apart before the shape
// could match it, leaving most of the token readable. The literals then run on the text between
// the markers, so a secret that is a substring of "[redacted]" does not mangle them; for text in
// which no secret overlaps a shape, the result is the same as before.
func (r *Registry) scrub(s string, re *regexp.Regexp) string {
	if s == "" {
		return s
	}
	for _, hint := range shapeHints {
		if strings.Contains(s, hint) {
			s = re.ReplaceAllLiteralString(s, redacted)
			break
		}
	}
	if r == nil {
		return s
	}
	repl := r.replacer()
	if repl == nil {
		return s
	}
	if !strings.Contains(s, redacted) {
		return repl.Replace(s)
	}
	parts := strings.Split(s, redacted)
	for i, p := range parts {
		parts[i] = repl.Replace(p)
	}
	return strings.Join(parts, redacted)
}

// replacer returns the compiled replacer, rebuilding it when a tracked Value
// changed since the last build.
func (r *Registry) replacer() *strings.Replacer {
	r.mu.RLock()
	if r.built && r.stamp == r.versionSum() {
		repl := r.repl
		r.mu.RUnlock()
		return repl
	}
	r.mu.RUnlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	r.rebuild()
	return r.repl
}

func (r *Registry) versionSum() uint64 {
	sum := uint64(len(r.tracked) + len(r.fixed))
	for _, h := range r.tracked {
		sum += h.currentVersion()
	}
	return sum
}

// rebuild compiles the replacer. Longer forms go first because strings.Replacer
// prefers earlier pairs at the same position, and a short secret must not eat
// the start of a longer one. It leaves repl nil when there is nothing to
// match. Callers hold r.mu for writing.
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
	r.repl = nil
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
	pairs := make([]string, 0, 2*len(list))
	for _, f := range list {
		pairs = append(pairs, f, redacted)
	}
	r.repl = strings.NewReplacer(pairs...)
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
