package api

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// noncePlaceholder is the content of index.html's csp-nonce meta in the built SPA.
var noncePlaceholder = []byte("__CSP_NONCE__")

// nonceSource feeds newNonce; tests replace it to exercise the failure path.
var nonceSource io.Reader = rand.Reader

// newNonce returns 16 random bytes, base64 raw-std encoded.
func newNonce() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(nonceSource, b[:]); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(b[:]), nil
}

// serveStatic serves the SPA: existing files as they are, any other path as index.html. The setup
// page is served while setup is needed (the local mode before its account exists, also after the
// token expired, so the page can say so) and is 404 otherwise, so an installed dash never offers it.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if (name == "setup" || strings.HasPrefix(name, "setup/")) && !s.d.Local.SetupNeeded() {
		http.NotFound(w, r)
		return
	}
	if name == "" {
		name = "index.html"
	}
	if st, err := fs.Stat(s.static, name); err != nil || st.IsDir() {
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}
	b, err := fs.ReadFile(s.static, name)
	if err != nil {
		http.Error(w, "dashboard not built", http.StatusNotFound)
		return
	}
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", "no-cache")
		// The document carries a per-response nonce, so no two responses may share a body. NEVER set
		// an ETag or Last-Modified (a non-zero modtime below) on it: a conditional request would
		// then be answered 304 and the browser would keep an old document whose nonce no longer
		// matches the one the server issues. With a zero modtime and no ETag there is no validator,
		// but ServeContent still answers `If-None-Match: *` with 304 (and `If-Match` with 412), and
		// a `Range` request with 206 (it ignores Range only while If-Range is present, and a client
		// could then stitch bytes of two documents with different nonces together). So the
		// request's preconditions and ranges are dropped: the document is always a fresh, whole
		// 200 (TestIndexIsNeverNotModified).
		nonce, err := newNonce()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		b = bytes.ReplaceAll(b, noncePlaceholder, []byte(nonce))
		// The document's policy replaces the API policy securityHeaders set (same header name, so
		// the response never carries two); off sends none.
		if hn, hv, ok := documentPolicyWith(s.cspMode, nonce, s.iconSrc); ok {
			w.Header().Set(hn, hv)
		}
		r = r.Clone(r.Context())
		for _, h := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range", "Range"} {
			r.Header.Del(h)
		}
	case strings.HasPrefix(name, "assets/"):
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}
