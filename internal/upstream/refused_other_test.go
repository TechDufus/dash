//go:build !unix

package upstream

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// refusedURL is the URL of a port nothing listens on (a closed test server's; CI runs on unix,
// where refused_unix_test.go holds the port instead).
func refusedURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	url := s.URL
	s.Close()
	return url
}
