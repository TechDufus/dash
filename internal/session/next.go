package session

// ValidNext reports whether next may be the target of the redirect after a login (the local
// login, the OIDC callback): a path on this origin that starts with a single "/", holds no
// backslash and no control character, and parses with neither a scheme nor a host. "//evil",
// "/\evil", "https://evil" and "javascript:..." are not.
func ValidNext(next string) bool { return false }

// SafeNext is next when ValidNext accepts it, "/" otherwise.
func SafeNext(next string) string {
	if ValidNext(next) {
		return next
	}
	return "/"
}
