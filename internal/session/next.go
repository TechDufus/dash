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

// ValidNext reports whether next may be the target of the redirect after a login (the local
// login, the OIDC callback): a path on this origin that starts with a single "/", holds no
// backslash and no control character, and parses with neither a scheme nor a host. "//evil",
// "/\evil", "https://evil" and "javascript:..." are not.
func ValidNext(next string) bool {
	if len(next) == 0 || len(next) > maxNextBytes || next[0] != '/' {
		return false
	}
	if len(next) > 1 && next[1] == '/' {
		return false // protocol-relative: //evil
	}
	// Browsers read "\" as "/" and drop tabs and newlines inside a URL, so "/\evil" and
	// "/\t/evil" would both become "//evil". Neither is ever needed in a path of dash.
	if strings.ContainsRune(next, '\\') || !utf8.ValidString(next) {
		return false
	}
	for _, r := range next {
		if unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(next)
	if err != nil {
		return false
	}
	return u.Scheme == "" && u.Host == "" && u.User == nil && u.Opaque == "" && strings.HasPrefix(u.Path, "/")
}

// SafeNext is next when ValidNext accepts it, "/" otherwise.
func SafeNext(next string) string {
	if ValidNext(next) {
		return next
	}
	return "/"
}
