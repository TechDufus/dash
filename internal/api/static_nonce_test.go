package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

const nonceIndex = `<!doctype html><html><head><meta name="csp-nonce" content="__CSP_NONCE__" /></head><body><div id="root"></div></body></html>`

var nonceMeta = regexp.MustCompile(`<meta name="csp-nonce" content="([^"]*)" />`)

func nonceServer() *Server {
	return &Server{static: fstest.MapFS{
		"index.html":  {Data: []byte(nonceIndex)},
		"assets/x.js": {Data: []byte(`const placeholder = "__CSP_NONCE__";`)},
		"favicon.svg": {Data: []byte(`<svg data-n="__CSP_NONCE__"/>`)},
	}}
}

func getStatic(s *Server, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.serveStatic(w, r)
	return w
}

// nonceOf extracts the nonce from a served document and requires it to be 16 base64 raw-std bytes.
func nonceOf(t *testing.T, body string) string {
	t.Helper()
	m := nonceMeta.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csp-nonce meta in %q", body)
	}
	raw, err := base64.RawStdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatalf("nonce %q is not raw-std base64: %v", m[1], err)
	}
	if len(raw) != 16 {
		t.Fatalf("nonce %q decodes to %d bytes, want 16", m[1], len(raw))
	}
	return m[1]
}

func TestStaticSPAReplacesTheNoncePlaceholder(t *testing.T) {
	s := nonceServer()
	for _, target := range []string{"/", "/index.html", "/alerts", "/cluster/pods/x"} {
		t.Run(target, func(t *testing.T) {
			w := getStatic(s, http.MethodGet, target, nil)
			if w.Code != http.StatusOK {
				t.Fatalf("code = %d", w.Code)
			}
			body := w.Body.String()
			if strings.Contains(body, "__CSP_NONCE__") {
				t.Fatalf("placeholder reached the client: %q", body)
			}
			nonceOf(t, body)
			if got := w.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("Cache-Control = %q, want no-cache", got)
			}
		})
	}
}

func TestStaticNonceDiffersPerResponse(t *testing.T) {
	s := nonceServer()
	seen := map[string]bool{}
	const n = 50
	for range n {
		w := getStatic(s, http.MethodGet, "/", nil)
		seen[nonceOf(t, w.Body.String())] = true
	}
	if len(seen) != n {
		t.Fatalf("%d distinct nonces in %d responses", len(seen), n)
	}
}

func TestStaticAssetsAreNotTemplated(t *testing.T) {
	s := nonceServer()
	tests := map[string]string{
		"/assets/x.js": `const placeholder = "__CSP_NONCE__";`,
		"/favicon.svg": `<svg data-n="__CSP_NONCE__"/>`,
	}
	for target, want := range tests {
		t.Run(target, func(t *testing.T) {
			w := getStatic(s, http.MethodGet, target, nil)
			if w.Code != http.StatusOK || w.Body.String() != want {
				t.Fatalf("code = %d body = %q, want 200 %q", w.Code, w.Body.String(), want)
			}
		})
	}
}

func TestIndexIsNeverNotModified(t *testing.T) {
	s := nonceServer()
	conditions := map[string]map[string]string{
		"If-None-Match *":     {"If-None-Match": "*"},
		"If-None-Match etag":  {"If-None-Match": `"x"`},
		"If-None-Match weak":  {"If-None-Match": `W/"x", "y"`},
		"If-Modified-Since":   {"If-Modified-Since": "Fri, 01 Jan 2100 00:00:00 GMT"},
		"both":                {"If-None-Match": "*", "If-Modified-Since": "Fri, 01 Jan 2100 00:00:00 GMT"},
		"If-Unmodified-Since": {"If-Unmodified-Since": "Fri, 01 Jan 2100 00:00:00 GMT"},
		"If-Match":            {"If-Match": `"x"`},
	}
	for _, target := range []string{"/", "/alerts"} {
		for name, hdr := range conditions {
			t.Run(target+" "+name, func(t *testing.T) {
				w := getStatic(s, http.MethodGet, target, hdr)
				if w.Code != http.StatusOK {
					t.Fatalf("code = %d, want 200", w.Code)
				}
				nonceOf(t, w.Body.String())
				for _, h := range []string{"ETag", "Last-Modified"} {
					if v := w.Header().Get(h); v != "" {
						t.Errorf("%s = %q: a validator on the nonced document lets a 304 pin a stale nonce", h, v)
					}
				}
			})
		}
	}
}

func TestStaticHeadServesTheDocumentHeadersOnly(t *testing.T) {
	w := getStatic(nonceServer(), http.MethodHead, "/alerts", nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Fatalf("code = %d body = %q, want 200 and no body", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

func TestStaticNonceFailureIsA500WithoutADocument(t *testing.T) {
	orig := nonceSource
	t.Cleanup(func() { nonceSource = orig })
	nonceSource = errReader{}

	w := getStatic(nonceServer(), http.MethodGet, "/", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "csp-nonce") || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("a document was served without a nonce: %q", w.Body.String())
	}

	// A short read is a failure too, never a nonce padded with zeros.
	nonceSource = strings.NewReader("short")
	if w := getStatic(nonceServer(), http.MethodGet, "/", nil); w.Code != http.StatusInternalServerError {
		t.Fatalf("short read: code = %d, want 500", w.Code)
	}

	// Assets do not need a nonce and keep working.
	if w := getStatic(nonceServer(), http.MethodGet, "/assets/x.js", nil); w.Code != http.StatusOK {
		t.Fatalf("asset: code = %d, want 200", w.Code)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
