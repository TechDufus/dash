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

// The wire codes a Failure carries. They are the model.ErrCode* strings (auth imports no model).
const (
	CodeUnauthenticated = "unauthenticated"
	CodeInvalidToken    = "invalid_token"
)

// The machine reasons of a Failure. The API maps them to the obs auth reasons of the same name,
// ReasonNoIdentity and ReasonBearerRefused to "unauthenticated".
const (
	ReasonNoIdentity        = "no_identity"        // no identity header, or one that cannot be a user name
	ReasonUntrustedPeer     = "untrusted_peer"     // identity headers from a peer outside the trusted set
	ReasonAmbiguousIdentity = "ambiguous_identity" // an identity header with more than one line
	ReasonBearerRefused     = "bearer_refused"     // Authorization: Bearer, until bearer tokens exist (P7.1)
)

// Authenticator resolves the principal of a request, or says why it cannot.
type Authenticator interface {
	Authenticate(*http.Request) (Principal, *Failure)
}

// The warning codes an Authenticator reports through Warner. The API puts them in Me.warnings
// and the SPA renders a banner for each.
const (
	WarningAdminWildcard = "admin_wildcard" // adminUsers holds "*": every authenticated user is an administrator
	WarningAuthNone      = "auth_none"      // auth.mode=none: every request is an administrator
)

// Warner is implemented by an Authenticator that has standing warnings for the operator: Proxy
// with the admin wildcard, and the fixed principal of auth.mode=none. The API reads it through a
// type assertion; an Authenticator without warnings does not implement it.
type Warner interface {
	// Warnings are the codes of the standing warnings, empty when there are none.
	Warnings() []string
}

// Kinder is implemented by an authenticator that signs every request in as one kind of principal
// (Fixed: dev, demo, none). The API names a refusal that has no principal after it, so the audit
// record of a CSRF denial on a path authn does not cover says none in none mode, not proxy.
type Kinder interface {
	Kind() Kind
}

// Bearer extracts a bearer token. present is true when ANY Authorization header line has the
// Bearer scheme (case-insensitive), so a Basic line in front of a Bearer one cannot hide it;
// every other scheme, or no header, is ignored (not present) so an upstream proxy that forwards
// Basic credentials changes nothing. The token is the trimmed remainder of the first Bearer line
// and may be empty when present.
func Bearer(r *http.Request) (token string, present bool) {
	for _, line := range r.Header.Values("Authorization") {
		if token, present = bearerLine(line); present {
			return token, true
		}
	}
	return "", false
}

// bearerLine parses one Authorization value.
func bearerLine(line string) (token string, present bool) {
	v := strings.TrimSpace(line)
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
