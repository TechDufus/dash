package secret

import "unicode/utf8"

// shapeReach is how far past a cut a token shape whose hint starts before the cut may have to be
// read to match: the longest prefix, "dash_setup_", and one token byte.
const shapeReach = len("dash_setup_") + 1

// Cut bounds text that a scrubber only sees afterwards (a log line, an audit record, an error) to
// n bytes without cutting a secret in half: the half before the cut would no longer match.
//
// It returns s and false when s is at most n bytes long; that text is not scanned, and the
// caller's scrubber still runs on it. Otherwise it returns the first n bytes of s in which every
// span ScrubLogLine redacts (a registered secret in any of its forms, a token shape) that starts
// before byte n is one "[redacted]" in full, and true. A span that reaches byte n or runs past it
// ends the result right after its marker. The spans are searched in s[:n+reach] only, reach being
// the longest registered form and at least shapeReach, so every span that starts before the cut
// is matched whole and the cost is bounded by n and the registry, never by len(s). Text in which
// nothing matches comes out as s[:n], even where byte n splits a character.
//
// Scrubbing s first and cutting the result is not the same: each redaction shifts the text
// after it, so a part of a secret from past the window (s itself may end in one, cut by a read
// limit) could slide back under the cut. A nil registry only matches shapes. A negative n counts
// as 0.
func (r *Registry) Cut(s string, n int) (string, bool) {
	n = max(n, 0)
	if len(s) <= n {
		return s, false
	}
	return r.cutAt(s, n), true
}

// CutRunes is Cut counting n in runes, for text that today's cut bounds in runes. A cut result
// is what string([]rune(s)[:n]) gives for plain text: every invalid byte becomes U+FFFD. Finding
// rune n reads s only up to it. A negative n counts as 0.
func (r *Registry) CutRunes(s string, n int) (string, bool) {
	n = max(n, 0)
	k := 0
	for i := range s {
		if k == n {
			out := r.cutAt(s, i)
			if !utf8.ValidString(out) {
				out = string([]rune(out))
			}
			return out, true
		}
		k++
	}
	return s, false
}

// cutAt is s[:end] with every span that starts before end redacted whole (Cut); end < len(s).
func (r *Registry) cutAt(s string, end int) string {
	forms := r.formList() // longest first
	reach := shapeReach
	if len(forms) > 0 {
		reach = max(reach, len(forms[0]))
	}
	w := s[:min(len(s), end+reach)]
	return redact(w, spansOf(w, logShapes, forms), end)
}
