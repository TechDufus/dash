package model

// APIError is the body of every non-2xx JSON answer. Code is one of the ErrCode* constants:
// clients branch on it, never on Error, which is text for a person.
type APIError struct {
	Error   string `json:"error"`
	Code    string `json:"code"`
	Details any    `json:"details,omitempty" tstype:"unknown"`
}

// Error codes (design L.2). Each is the machine-readable reason of a status; one status can
// carry several (a 403 is forbidden or csrf_failed, a 429 rate_limited or too_many_streams).
const (
	ErrCodeUnauthenticated     = "unauthenticated"      // 401
	ErrCodeForbidden           = "forbidden"            // 403
	ErrCodeInsufficientScope   = "insufficient_scope"   // 403: a token without the scope the route needs (phase 7)
	ErrCodeCSRFFailed          = "csrf_failed"          // 403
	ErrCodeNotFound            = "not_found"            // 404
	ErrCodeInvalidInput        = "invalid_input"        // 400, 422
	ErrCodeConflict            = "conflict"             // 409
	ErrCodeRateLimited         = "rate_limited"         // 429
	ErrCodeTooManyStreams      = "too_many_streams"     // 429, 503
	ErrCodeUpstreamUnavailable = "upstream_unavailable" // 502
	ErrCodeAuditUnavailable    = "audit_unavailable"    // 503
	ErrCodeUnknownHost         = "unknown_host"         // 421
	ErrCodeInternal            = "internal"             // 500
)
