package auth

import (
	"net/http"
	"strings"
)

// Failure is why a request could not be authenticated. Code and Reason are the wire error code
// and its machine reason; Message, when set, is the error text for a person (otherwise the code
// in words); WWWAuth, when set, is the WWW-Authenticate challenge to send.
type Failure struct {
	Status  int
	Code    string
	Reason  string
	Message string
	Details any
	WWWAuth string
}

// The wire codes a Failure carries. They are the model.ErrCode* strings (auth imports no model).
const (
	CodeUnauthenticated      = "unauthenticated"
	CodeInvalidToken         = "invalid_token"
	CodeAmbiguousCredentials = "ambiguous_credentials"
)

// The machine reasons of a Failure. The API maps them to the obs auth reasons of the same name,
// ReasonNoIdentity to "unauthenticated".
const (
	ReasonNoIdentity        = "no_identity"        // no identity header, or one that cannot be a user name
	ReasonUntrustedPeer     = "untrusted_peer"     // identity headers from a peer outside the trusted set
	ReasonDirectHost        = "direct_host"        // identity headers on server.directURL's host (DistrustPeer)
	ReasonAmbiguousIdentity = "ambiguous_identity" // an identity header with more than one line
	// ReasonInvalidToken is a bearer token that is unknown, expired, revoked or for another
	// audience. An authenticator that is handed a bearer credential (the API resolves tokens
	// ahead of it, so only a process without a token resolver gets here) refuses it with this
	// too, never reading the other credentials.
	ReasonInvalidToken = "invalid_token"
	// ReasonAmbiguousCredentials is a bearer token together with dash's session cookie.
	ReasonAmbiguousCredentials = "ambiguous_credentials"
)

// Authenticator resolves the principal of a request, or says why it cannot.
type Authenticator interface {
	Authenticate(*http.Request) (Principal, *Failure)
}

// The warning codes an Authenticator reports through Warner. The API puts them in Me.warnings
// and the SPA renders a banner for each.
const (
	WarningAdminWildcard   = "admin_wildcard"   // adminUsers holds "*": every authenticated user is an administrator
	WarningAuthNone        = "auth_none"        // auth.mode=none: every request is an administrator
	WarningAnonymousViewer = "anonymous_viewer" // auth.local.anonymousViewer: unauthenticated reads are a viewer
	WarningOIDCOpen        = "oidc_open"        // auth.oidc.allowedUsers holds "*": every account the IdP signs in may read
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

// The auth.mode values of the session modes, which Moder reports. The other modes are named by
// the kind of their principals (proxy, none, dev, demo).
const (
	ModeLocal = "local"
	ModeOIDC  = "oidc"
)

// Moder is implemented by an authenticator whose mode is not the kind of the principals it
// produces: the session modes, whose principals are session or anonymous. The API takes the
// mode of Me.authMode and of every audit record from it first, ahead of the principal's kind
// and Kinder, so a refusal without a principal in local or oidc mode is not recorded as proxy.
type Moder interface {
	Mode() string
}

// CookieAuthenticator is implemented by the authenticators of the session modes. authn calls
// AuthenticateCookie instead of Authenticate: besides the principal or the failure it returns
// the session cookie the response must carry, or nil when the request's cookie stays as it is.
// That is a re-issued cookie when the session is in the second half of its idle window, or a
// clearing cookie when the request carried one that failed (malformed, expired, revoked by an
// epoch). authn sets it before anything is written, on a refusal too, so the browser drops a
// dead cookie even when the request ends in 401. Authenticate returns the same principal or
// failure without the cookie.
type CookieAuthenticator interface {
	Authenticator
	AuthenticateCookie(*http.Request) (Principal, *http.Cookie, *Failure)
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
