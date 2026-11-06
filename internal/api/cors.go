package api

import (
	"context"
	"net/http"
	"slices"
)

// The CORS answers of design L.7.
const (
	corsAllowMethods  = "GET, POST, DELETE"
	corsAllowHeaders  = "authorization, content-type, if-none-match, mcp-protocol-version, mcp-method, mcp-name"
	corsExposeHeaders = "ETag, X-Request-Id, WWW-Authenticate"
	corsMaxAge        = "600"
)

// cors serves server.corsOrigins (design L.7, contract D9) for browser-based bearer clients.
// Without a list, or where no bearer token can resolve (auth.mode=none), it sends no
// Access-Control-* header at all. For a listed Origin:
//   - a preflight (OPTIONS with Access-Control-Request-Method) is answered 204 here, on the
//     origin alone: it carries no credential and grants nothing by itself;
//   - a response to a request authn resolved to a token principal gets
//     Access-Control-Allow-Origin, Vary: Origin and the exposed headers, as does a 401 to any
//     request, so a client can read the challenge. A Bearer header alone grants nothing: the
//     decision is the resolved principal's (the CSRF rule of contract D3, applied to CORS);
//   - every answer on the OAuth metadata documents and the token and revoke endpoints gets
//     them too (oauthCORSPath): a browser-based client calls those without a credential.
//
// /oauth/authorize, a navigation, gets no Access-Control-* header at all, a preflight included.
//
// Access-Control-Allow-Credentials is never sent: cookies and the identity proxy's session stay
// same-origin, so the web UI's own requests are never readable cross-origin.
func (s *Server) cors(next http.Handler) http.Handler {
	origins := s.d.Cfg.CORSOrigins
	if len(origins) == 0 || s.d.Tokens == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !slices.Contains(origins, origin) || r.URL.Path == "/oauth/authorize" {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", corsAllowMethods)
			h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
			h.Set("Access-Control-Max-Age", corsMaxAge)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		cw := &corsWriter{ResponseWriter: w, origin: origin, token: oauthCORSPath(r.URL.Path)}
		next.ServeHTTP(cw, r.WithContext(context.WithValue(r.Context(), corsKey{}, cw)))
	})
}

type corsKey struct{}

// markTokenForCORS tells the CORS layer that authn resolved the request to a token principal, so
// its answer may be read by the listed origin it came from.
func markTokenForCORS(r *http.Request) {
	if cw, ok := r.Context().Value(corsKey{}).(*corsWriter); ok {
		cw.token = true
	}
}

// corsWriter adds the allow headers when the status is written: to every answer of a token
// principal, and to a 401 of any request.
type corsWriter struct {
	http.ResponseWriter
	origin string
	token  bool // set by authn (markTokenForCORS), or from the start on an OAuth endpoint
	wrote  bool
}

func (c *corsWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		if c.token || code == http.StatusUnauthorized {
			h := c.Header()
			h.Set("Access-Control-Allow-Origin", c.origin)
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		}
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *corsWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// Flush sends what was written, the status first.
func (c *corsWriter) Flush() {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection (flushes, deadlines).
func (c *corsWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
