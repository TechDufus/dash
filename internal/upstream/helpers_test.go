package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

var ctx = context.Background()

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}
