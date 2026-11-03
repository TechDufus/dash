package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func iconLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestIconSourceConvertsAnInternationalHost(t *testing.T) {
	for base, want := range map[string]string{
		"https://bücher.example/png":             "https://xn--bcher-kva.example/png/",
		"https://BÜCHER.example:8443/a/b":        "https://xn--bcher-kva.example:8443/a/b/",
		"https://xn--bcher-kva.example/png":      "https://xn--bcher-kva.example/png/",
		"https://cdn.example.com/icons/png":      "https://cdn.example.com/icons/png/",
		"https://user:pw@cdn.example.com/p?q=1#": "https://cdn.example.com/p/",
	} {
		if got := iconSource(base); got != want {
			t.Errorf("iconSource(%q) = %q, want %q", base, got, want)
		}
	}
}

// What a CSP source cannot carry still gives no source (never a wrong one).
func TestIconSourceRefusesWhatItCannotCarry(t *testing.T) {
	for _, base := range []string{
		"https://[2001:db8::1]/png",
		"https://my_cdn.example/png",
		"https://cdn.example.com/a;b",
		"https://cdn.example.com/a,b",
		"https://cdn.example.com/a'b",
		"https://cdn.example.com:80a/png",
		"ftp://cdn.example.com/png",
		"https://-.example/x\x00y",
	} {
		if got := iconSource(base); got != "" {
			t.Errorf("iconSource(%q) = %q, want none", base, got)
		}
	}
}

func TestUnusableIconBaseWarnsOnceWithSchemeAndHostOnly(t *testing.T) {
	for _, base := range []string{
		"https://[2001:db8::1]/png",
		"https://my_cdn.example/png",
		"https://user:hunter2@my_cdn.example/a;b?token=sekret#frag",
	} {
		log, buf := iconLogger()
		if got := iconSourceOrWarn(base, log); got != "" {
			t.Errorf("%q: source = %q, want none", base, got)
		}
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if len(lines) != 1 || !strings.Contains(lines[0], "level=WARN") || !strings.Contains(lines[0], "scheme=https") ||
			!strings.Contains(lines[0], "block every icon") {
			t.Errorf("%q: want one WARN with the scheme, got:\n%s", base, buf)
		}
		for _, leak := range []string{"hunter2", "sekret", "frag", "a;b", "/png", "user"} {
			if strings.Contains(buf.String(), leak) {
				t.Errorf("%q: the warning carries %q:\n%s", base, leak, buf)
			}
		}
	}
}

func TestUsableOrEmptyIconBaseDoesNotWarn(t *testing.T) {
	for _, base := range []string{"", "   ", "https://cdn.example.com/png", "https://bücher.example/png"} {
		log, buf := iconLogger()
		iconSourceOrWarn(base, log)
		if buf.Len() != 0 {
			t.Errorf("%q warned:\n%s", base, buf)
		}
	}
}

// New computes the source once: one WARN at start however many pages are served, and the
// document policy carries the converted host.
func TestServerWarnsOnceAtStartAndServesTheConvertedHost(t *testing.T) {
	log, buf := iconLogger()
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	bad := cspServer("", "https://my_cdn.example/png")
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Fatalf("want one WARN at start, got %d:\n%s", n, buf)
	}
	for range 3 {
		w := req{path: "/"}.do(bad)
		if pol := w.Header().Get("Content-Security-Policy"); strings.Contains(pol, "my_cdn") || !strings.Contains(pol, "img-src 'self' data:;") {
			t.Errorf("an unusable base must add no source: %s", pol)
		}
	}
	if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
		t.Errorf("serving pages warned again (%d WARN lines):\n%s", n, buf)
	}

	buf.Reset()
	idn := cspServer("", "https://bücher.example/png")
	if buf.Len() != 0 {
		t.Errorf("a convertible base warned:\n%s", buf)
	}
	w := req{path: "/"}.do(idn)
	if pol := w.Header().Get("Content-Security-Policy"); !strings.Contains(pol, "img-src 'self' data: https://xn--bcher-kva.example/png/;") {
		t.Errorf("the international host is not in its xn-- form: %s", pol)
	}
}
