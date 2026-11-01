package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ErrInvalidResponse marks an answer dash could not use: a body that does not decode, or a
// payload that is not what the API promises. Category reports it as "invalid_response".
var ErrInvalidResponse = errors.New("invalid response")

// Error categories (design C.6, K.3): all a viewer ever learns about a failed upstream. The
// detail (URLs, hosts, upstream bodies) goes to the logs only. A non-2xx answer is "auth" for
// 401 and 403 and "http_<code>" otherwise.
const (
	CategoryUnreachable     = "unreachable"
	CategoryTimeout         = "timeout"
	CategoryTLS             = "tls"
	CategoryAuth            = "auth"
	CategoryInvalidResponse = "invalid_response"
	CategoryNotConfigured   = "not_configured"
)

// Category reduces an upstream error to one of the categories above. It is empty for a nil
// error. Anything that is not recognised is "unreachable".
func Category(err error) string {
	var se *StatusError
	var ne net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrDisabled):
		return CategoryNotConfigured
	case errors.As(err, &se):
		return HTTPCategory(se.Code)
	case isTLS(err):
		return CategoryTLS
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return CategoryTimeout
	case errors.Is(err, ErrInvalidResponse):
		return CategoryInvalidResponse
	}
	return CategoryUnreachable
}

// HTTPCategory is the category of a non-2xx answer with the given status code.
func HTTPCategory(code int) string {
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return CategoryAuth
	}
	return fmt.Sprintf("http_%d", code)
}

// isTLS reports certificate and handshake failures. The alerts a peer sends back during the
// handshake are not exported types, so those are matched by their message prefix.
func isTLS(err error) bool {
	var (
		verify  *tls.CertificateVerificationError
		unknown x509.UnknownAuthorityError
		host    x509.HostnameError
		invalid x509.CertificateInvalidError
		record  tls.RecordHeaderError
	)
	if errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) ||
		errors.As(err, &invalid) || errors.As(err, &record) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "tls: ") || strings.Contains(msg, "x509: ")
}
