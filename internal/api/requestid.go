package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
)

const requestIDHeader = "X-Request-Id"

// inboundRequestID is what a request id sent by a trusted proxy must look like.
var inboundRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

// requestIDFrom returns the id requestID gave the request, or "" outside the chain.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// requestID is the outermost middleware (design L.2, L.8). Every response carries an
// X-Request-Id: 128 random bits as hex, or the id an upstream proxy sent when its TCP peer is
// loopback (the trusted-proxy default; no header is believed from anyone else) and it is a
// short token of letters, digits, dot, underscore and dash. The id reaches the error body, the
// audit records and the log line of an upstream failure.
//
// It also marks every /api/v1 response no-store, so an intermediary never keeps a user's data;
// a handler that wants caching (the topic snapshot) overrides the header.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !inboundRequestID.MatchString(id) || !loopbackPeer(r) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never fails since Go 1.24
	return hex.EncodeToString(b[:])
}
