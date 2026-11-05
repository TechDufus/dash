package model

// APIError is the body of every non-2xx JSON answer. Code is one of the ErrCode* constants:
// clients branch on it, never on Error, which is text for a person. RequestID repeats the
// X-Request-Id header of the response, so a copied error body is enough to find the log lines
// and the audit record of the request.
type APIError struct {
	Error     string `json:"error" dash:"required"`
	Code      string `json:"code" dash:"required"`
	RequestID string `json:"requestId" dash:"required"`
	Details   any    `json:"details,omitempty" tstype:"unknown"`
}

// Error codes (design L.2). Each is the machine-readable reason of a status; one status can
// carry several (a 403 is forbidden or csrf_failed, a 429 rate_limited or too_many_streams).
// ErrCodes lists them all.
const (
	ErrCodeUnauthenticated      = "unauthenticated"       // 401
	ErrCodeForbidden            = "forbidden"             // 403
	ErrCodeInsufficientScope    = "insufficient_scope"    // 403: a token without the scope the route needs
	ErrCodeInvalidToken         = "invalid_token"         // 401: a bearer token that is unknown, expired or for another audience
	ErrCodeAmbiguousCredentials = "ambiguous_credentials" // 400: a bearer token together with a session cookie
	ErrCodeCSRFFailed           = "csrf_failed"           // 403
	ErrCodeNotFound             = "not_found"             // 404
	ErrCodeInvalidInput         = "invalid_input"         // 400, 422
	ErrCodeLockedField          = "locked_field"          // 409: a setting that is not editable from the UI
	ErrCodeReadonlySettings     = "readonly_settings"     // 409: the settings store is read-only
	ErrCodeRateLimited          = "rate_limited"          // 429
	ErrCodeTooManyStreams       = "too_many_streams"      // 429, 503
	ErrCodeUpstreamUnavailable  = "upstream_unavailable"  // 502 or 503 (the OIDC provider cannot be reached)
	ErrCodeApprovalRequired     = "approval_required"     // 202: an agent action waits for its approval
	ErrCodeApprovalDenied       = "approval_denied"       // 403
	ErrCodeApprovalExpired      = "approval_expired"      // 410
	ErrCodeConflict             = "conflict"              // 409
	ErrCodePreconditionFailed   = "precondition_failed"   // 412
	ErrCodePreconditionRequired = "precondition_required" // 428: a settings write without If-Match
	ErrCodeAuditUnavailable     = "audit_unavailable"     // 503
	ErrCodeTokensUnavailable    = "tokens_unavailable"    // 409: no writable data dir keeps a new token
	ErrCodeTokenLimit           = "token_limit"           // 409: the user holds the most tokens allowed
	ErrCodeGrantLimit           = "grant_limit"           // 409: the user holds the most OAuth grants allowed
	ErrCodeUnknownHost          = "unknown_host"          // 421
	ErrCodeInternal             = "internal"              // 500
)

// ErrCodes is every ErrCode* constant, in the order above. The OpenAPI document lists them as
// the values of APIError.code; a test keeps this list and the constants together.
func ErrCodes() []string {
	return []string{
		ErrCodeUnauthenticated, ErrCodeForbidden, ErrCodeInsufficientScope, ErrCodeInvalidToken,
		ErrCodeAmbiguousCredentials, ErrCodeCSRFFailed, ErrCodeNotFound, ErrCodeInvalidInput,
		ErrCodeLockedField, ErrCodeReadonlySettings, ErrCodeRateLimited, ErrCodeTooManyStreams,
		ErrCodeUpstreamUnavailable, ErrCodeApprovalRequired, ErrCodeApprovalDenied,
		ErrCodeApprovalExpired, ErrCodeConflict, ErrCodePreconditionFailed, ErrCodePreconditionRequired,
		ErrCodeAuditUnavailable, ErrCodeTokensUnavailable, ErrCodeTokenLimit, ErrCodeGrantLimit, ErrCodeUnknownHost, ErrCodeInternal,
	}
}
