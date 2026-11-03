package session

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNextBytes bounds a "next" path: it travels in the sealed OIDC state cookie, which must stay
// small.
const maxNextBytes = 1024

// encodedDot is a percent-encoded dot, which a browser treats as "." when it removes dot
// segments ("/%2e//evil" is "/.//evil" is "//evil").
var encodedDot = strings.NewReplacer("%2e", ".", "%2E", ".")

// ValidNext reports whether next may be the target of the redirect after a login (the local
// login, the OIDC callback): a path on this origin that starts with a single "/", holds no
// backslash and no control character (also percent-encoded), and parses with neither a scheme
// nor a host. "//evil", "/\evil", "https://evil" and "javascript:..." are not. It also checks the
// path as a browser will follow it: with its dot segments removed (RFC 3986 5.2.4, encoded dots
// included) it must still start with exactly one "/", so "/.//evil", "/%2e//evil" and
// "/a/..//evil" (all "//evil" once normalized, a protocol-relative URL) are refused too.
func ValidNext(next string) bool {
	if len(next) == 0 || len(next) > maxNextBytes || next[0] != '/' {
		return false
	}
	if len(next) > 1 && next[1] == '/' {
		return false // protocol-relative: //evil
	}
	// Browsers read "\" as "/" and drop tabs and newlines inside a URL, so "/\evil" and
	// "/\t/evil" would both become "//evil". Neither is ever needed in a path of dash.
	if strings.ContainsRune(next, '\\') || !utf8.ValidString(next) || hasControl(next) {
		return false
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") {
		return false
	}
	// The decoded path: a server or a proxy may decode "%5C" or "%09" on the way.
	if strings.ContainsRune(u.Path, '\\') || !utf8.ValidString(u.Path) || hasControl(u.Path) {
		return false
	}
	normalized := removeDotSegments(encodedDot.Replace(u.EscapedPath()))
	return strings.HasPrefix(normalized, "/") && !strings.HasPrefix(normalized, "//")
}

// removeDotSegments is the algorithm of RFC 3986 section 5.2.4, which browsers follow (net/url's
// ResolveReference collapses "/a/..//x" differently, so it cannot stand in for one).
func removeDotSegments(in string) string {
	var out strings.Builder
	dropLast := func() {
		s := out.String()
		out.Reset()
		if i := strings.LastIndexByte(s, '/'); i > 0 {
			out.WriteString(s[:i])
		}
	}
	for in != "" {
		switch {
		case strings.HasPrefix(in, "../"):
			in = in[3:]
		case strings.HasPrefix(in, "./"):
			in = in[2:]
		case strings.HasPrefix(in, "/./"):
			in = in[2:]
		case in == "/.":
			in = "/"
		case strings.HasPrefix(in, "/../"):
			in = in[3:]
			dropLast()
		case in == "/..":
			in = "/"
			dropLast()
		case in == "." || in == "..":
			in = ""
		default:
			start := 0
			if in[0] == '/' {
				start = 1
			}
			end := len(in)
			if i := strings.IndexByte(in[start:], '/'); i >= 0 {
				end = start + i
			}
			out.WriteString(in[:end])
			in = in[end:]
		}
	}
	return out.String()
}

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// SafeNext is next when ValidNext accepts it, "/" otherwise.
func SafeNext(next string) string {
	if ValidNext(next) {
		return next
	}
	return "/"
}
