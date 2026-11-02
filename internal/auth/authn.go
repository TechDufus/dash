package auth

import (
	"net/http"
	"strings"
)

// Failure is why a request could not be authenticated. Code and Reason are the wire error code
// and its machine reason; WWWAuth, when set, is the WWW-Authenticate challenge to send.
type Failure struct {
	Status  int
	Code    string
	Reason  string
	Details any
	WWWAuth string
}

// Authenticator resolves the principal of a request, or says why it cannot.
type Authenticator interface {
	Authenticate(*http.Request) (Principal, *Failure)
}

// Bearer extracts a bearer token. present is true only when the Authorization header's scheme
// is Bearer (case-insensitive); every other scheme, or no header, is ignored (not present) so an
// upstream proxy that forwards Basic credentials changes nothing. The token is the trimmed
// remainder and may be empty when present.
func Bearer(r *http.Request) (token string, present bool) {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "bearer"
	if len(v) < len(scheme) || !strings.EqualFold(v[:len(scheme)], scheme) {
		return "", false
	}
	rest := v[len(scheme):]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}
