package secret

import (
	"encoding/base64"
	"math/rand/v2"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

// oldCutRunes is the rune cut the sites made before Cut existed (upstream.truncate).
func oldCutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// A secret or a token that straddles the cut is redacted whole, and the text ends after its
// marker; one that ends before the cut is redacted and the text goes on; one that starts at the
// cut is not in the result at all.
func TestCutRedactsASpanThatStraddlesTheCut(t *testing.T) {
	const sec = "client-secret-canary-55aa" // 25 bytes
	const odd = "pa&ss w/rd!x9"             // its QueryEscape form differs from it
	reg := registryOf([]string{sec, odd})
	b64 := base64.StdEncoding.EncodeToString([]byte(sec))
	qe := url.QueryEscape(odd)
	const n = 64
	pad := func(k int) string { return strings.Repeat("x", k) }
	for _, tc := range []struct {
		name, span string
		start      int // where span starts; the text is pad(start) + span + " tail…"
	}{
		{"secret, one byte before the cut", sec, n - 1},
		{"secret, half before the cut", sec, n - 12},
		{"base64 form", b64, n - 5},
		{"query-escaped form", qe, n - 4},
		{"dash_pat_ token", "dash_pat_AbC123-xyz_0", n - 3},
		{"dash_setup_ token, cut inside its prefix", "dash_setup_Q5Zk3m0D1yWb", n - 7},
		{"enc:v1: value", "enc:v1:k1:c2VhbGVkLXNlY3JldA==", n - 2},
	} {
		s := pad(tc.start) + tc.span + " " + pad(200)
		want := pad(tc.start) + redacted
		if got, cut := reg.Cut(s, n); got != want || !cut {
			t.Errorf("%s: Cut = %q, %v; want %q, true", tc.name, got, cut, want)
		}
		if got, cut := reg.CutRunes(s, n); got != want || !cut {
			t.Errorf("%s: CutRunes = %q, %v; want %q, true", tc.name, got, cut, want)
		}
		// Runes: the same span after two-byte characters, cut at rune n.
		rs := strings.Repeat("é", tc.start) + tc.span + " " + pad(200)
		if got, _ := reg.CutRunes(rs, n); got != strings.Repeat("é", tc.start)+redacted {
			t.Errorf("%s: CutRunes after é = %q", tc.name, got)
		}
	}

	// Ending exactly at the cut: redacted, and nothing follows it.
	s := pad(n-len(sec)) + sec + "rest of the line"
	if got, _ := reg.Cut(s, n); got != pad(n-len(sec))+redacted {
		t.Errorf("ending at the cut: %q", got)
	}
	// Ending before the cut: redacted, and the text goes on to the cut.
	s = pad(10) + sec + pad(100)
	if got, _ := reg.Cut(s, n); got != pad(10)+redacted+pad(n-10-len(sec)) {
		t.Errorf("ending before the cut: %q", got)
	}
	// Starting at the cut: not in the result.
	s = pad(n) + sec
	if got, _ := reg.Cut(s, n); got != pad(n) {
		t.Errorf("starting at the cut: %q", got)
	}
	// Touching spans across the cut are one marker.
	s = pad(n-3) + sec + "dash_pat_abc" + pad(10)
	if got, _ := reg.Cut(s, n); got != pad(n-3)+redacted {
		t.Errorf("touching spans: %q", got)
	}
	// A nil registry still redacts the shapes.
	s = pad(n-3) + "dash_pat_AbC123" + pad(10)
	if got, _ := (*Registry)(nil).Cut(s, n); got != pad(n-3)+redacted {
		t.Errorf("nil registry: %q", got)
	}
}

// Text Cut does not cut is returned as is, unscanned: the caller's scrubber handles it.
func TestCutLeavesTextItDoesNotCut(t *testing.T) {
	reg := registryOf([]string{"client-secret-canary-55aa"})
	s := "a client-secret-canary-55aa and dash_pat_abc"
	for _, tc := range []struct {
		name    string
		s       string
		bytes   int // n for Cut
		runes   int // n for CutRunes
		want    string
		wantCut bool
	}{
		{"at the length", s, len(s), utf8.RuneCountInString(s), s, false},
		{"past the length", s, len(s) + 5, len(s) + 5, s, false},
		{"empty text", "", 3, 3, "", false},
		{"empty text, n 0", "", 0, 0, "", false},
		{"empty text, negative n", "", -4, -4, "", false},
		{"n 0", s, 0, 0, "", true},
		{"negative n counts as 0", s, -4, -4, "", true},
		{"negative n, a token at the start", "dash_pat_abc tail", -1, -1, "", true},
	} {
		if got, cut := reg.Cut(tc.s, tc.bytes); got != tc.want || cut != tc.wantCut {
			t.Errorf("%s: Cut(%q, %d) = %q, %v; want %q, %v", tc.name, tc.s, tc.bytes, got, cut, tc.want, tc.wantCut)
		}
		if got, cut := reg.CutRunes(tc.s, tc.runes); got != tc.want || cut != tc.wantCut {
			t.Errorf("%s: CutRunes(%q, %d) = %q, %v; want %q, %v", tc.name, tc.s, tc.runes, got, cut, tc.want, tc.wantCut)
		}
	}
}

// Text in which nothing matches is cut exactly as before: s[:n] in bytes (a character may be
// split), string([]rune(s)[:n]) in runes (an invalid byte becomes U+FFFD).
func TestCutPlainTextIsTheOldCut(t *testing.T) {
	secrets := []string{"client-secret-canary-55aa", "token-secret-0123456789", "hunter2-hunter2",
		"q5Zk3m0D1yWb8c4tVfN2uE7xLpR9sJhA6oGiKwTnY0Q=", "u@pve!t=0b7e"}
	reg := registryOf(secrets)
	pieces := []string{"a", "b", "z", "Q", "0", "9", " ", ":", "/", ".", ",", "=", "-", "_", "é", "日", "\xff", "\xe6\x97", "dash", "enc", "v1"}
	rng := rand.New(rand.NewPCG(20261104, 2))
	for range 20000 {
		var sb strings.Builder
		for k := rng.IntN(60); k > 0; k-- {
			sb.WriteString(pieces[rng.IntN(len(pieces))])
		}
		s := sb.String()
		if want, _ := scrubOracle(s, secrets, oracleLogShapes); want != s {
			continue
		}
		n := rng.IntN(len(s) + 2)
		want := s
		if len(s) > n {
			want = s[:n]
		}
		if got, cut := reg.Cut(s, n); got != want || cut != (len(s) > n) {
			t.Fatalf("Cut(%q, %d) = %q, %v; want %q", s, n, got, cut, want)
		}
		if got, cut := reg.CutRunes(s, n); got != oldCutRunes(s, n) || cut != (utf8.RuneCountInString(s) > n) {
			t.Fatalf("CutRunes(%q, %d) = %q, %v; want %q", s, n, got, cut, oldCutRunes(s, n))
		}
	}
}

// oracleCut is the cut of s at n by the union oracle on the whole text: the marked bytes before
// n, each run one marker, a run that reaches n ending the text.
func oracleCut(s string, n int, mask []bool) string {
	var b strings.Builder
	for i := 0; i < n; {
		if !mask[i] {
			b.WriteByte(s[i])
			i++
			continue
		}
		for i < len(s) && mask[i] {
			i++
		}
		b.WriteString(redacted)
	}
	return b.String()
}

// On random texts rich in token prefixes and secret fragments, Cut over its window gives what the
// union oracle gives over the whole text, wherever the cut falls.
func TestCutMatchesTheUnionOracleOnTheWholeText(t *testing.T) {
	pieces := []string{"dash_", "pat_", "at_", "rt_", "setup_", "enc:", "v1:", "k1:", ":", "_", "-", "a", "b", "e", "s",
		"Z", "9", " ", "\"", "é", "=", "/", "+", "%", "d", "t", "p", "r", "[", "]"}
	sec := []string{"a", "b", "e", "s", "_", "-", "Z", "9", ":", "d", "t", "p", "r", "é", "x"}
	rng := rand.New(rand.NewPCG(20261104, 3))
	checked := 0
	for range 50000 {
		var sb strings.Builder
		for k := rng.IntN(20) + 1; k > 0; k-- {
			sb.WriteString(pieces[rng.IntN(len(pieces))])
		}
		s := sb.String()
		var secrets []string
		for k := rng.IntN(3) + 1; k > 0; k-- {
			var v strings.Builder
			for l := rng.IntN(12) + 1; l > 0; l-- {
				v.WriteString(sec[rng.IntN(len(sec))])
			}
			if rng.IntN(3) == 0 && len(s) > 2 {
				i := rng.IntN(len(s) - 1)
				j := i + 1 + rng.IntN(len(s)-i-1)
				v.Reset()
				v.WriteString(s[i : j+1])
			}
			secrets = append(secrets, v.String())
		}
		reg := registryOf(secrets)
		whole, mask := scrubOracle(s, secrets, oracleLogShapes)
		if reg.ScrubLogLine(s) != whole {
			continue // the scrubber's own agreement with the oracle is TestScrubMatchesTheUnionOracle's
		}
		checked++
		n := rng.IntN(len(s))
		if got, want := reg.cutAt(s, n), oracleCut(s, n, mask); got != want {
			t.Fatalf("secrets=%q s=%q n=%d: cut %q, oracle %q", secrets, s, n, got, want)
		}
	}
	if checked < 40000 {
		t.Fatalf("only %d texts checked", checked)
	}
}

// Scrubbing first and cutting the scrubbed text is wrong: an earlier redaction shifts the text
// after it, so the start of a secret that the source had already cut off (a read limit) slides
// back under the cut. Cut keeps to the original positions.
func TestCutNeverPullsTextFromPastTheCut(t *testing.T) {
	a := strings.Repeat("A", 40)
	b := "Bsecret-0123456789-0123456789-0123456789"
	reg := registryOf([]string{a, b})
	const n = 100
	s := a + strings.Repeat("x", n-40+20) + b[:6] // the source ended inside b
	if wrong := reg.ScrubLogLine(s); !strings.Contains(wrong[:min(n, len(wrong))], b[:6]) {
		t.Fatalf("the counterexample no longer shows the flaw: %q", wrong)
	}
	got, _ := reg.Cut(s, n)
	if want := redacted + strings.Repeat("x", n-40); got != want {
		t.Errorf("Cut = %q, want %q", got, want)
	}
}

// Cut reads a window of n plus the longest form, whatever follows it: counting the anchored
// shape attempts bounds the work without a timing assertion.
func TestCutReadsOnlyItsWindow(t *testing.T) {
	attempts := 0
	shapeAttempt = func() { attempts++ }
	t.Cleanup(func() { shapeAttempt = nil })
	long := strings.Repeat("L", 300)
	reg := registryOf([]string{long})
	const n = 1000
	tail := strings.Repeat("dash_pat_a ", 100000) // 1.1 MB of tokens past the cut
	for name, cut := range map[string]func(string, int) (string, bool){"Cut": reg.Cut, "CutRunes": reg.CutRunes} {
		attempts = 0
		got, _ := cut(strings.Repeat("y", n)+tail, n)
		if got != strings.Repeat("y", n) {
			t.Errorf("%s = %.40q…", name, got)
		}
		// The window is n plus the longest form (the base64 of the 300-byte secret): the hints in
		// its part past the cut are tried, nothing of the tail beyond it.
		if limit := len(reg.formList()[0])/len("dash_pat_a ") + 1; attempts > limit {
			t.Errorf("%s: %d anchored attempts, want at most %d", name, attempts, limit)
		}
	}
}

// BenchmarkCut cuts an 8192-rune log message followed by a 256 KiB tail, the largest record the
// victorialogs reader keeps; ScrubLogLine of the whole record is the cost Cut avoids.
func BenchmarkCut(b *testing.B) {
	reg := registryOf([]string{"client-secret-canary-55aa", "token-secret-0123456789", strings.Repeat("k", 300)})
	line := strings.Repeat("é", 8192) + strings.Repeat("dash_pat_a ", 256<<10/11)
	for name, f := range map[string]func(){
		"CutRunes 8192":             func() { reg.CutRunes(line, 8192) },
		"Cut 200":                   func() { reg.Cut(line, 200) },
		"ScrubLogLine whole record": func() { reg.ScrubLogLine(line) },
	} {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				f()
			}
		})
	}
}
