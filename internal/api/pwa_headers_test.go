package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TechDufus/dash/internal/config"
)

const (
	pwaOfflineCSS  = "\n      body { color: #e6edf7; }\n    "
	pwaOfflinePage = "<!doctype html><html><head><style>" + pwaOfflineCSS + "</style></head>" +
		`<body><img src="/icons/icon-192.png" alt=""><a href="">Try again</a></body></html>`
	pwaWorker   = `self.addEventListener("fetch", () => {});`
	pwaManifest = `{"id":"/","name":"dash","display":"standalone"}`
)

// pwaServer is a Server over the real chain with the SPA and the PWA files, in the given
// server.csp mode. Like New, it computes the PWA headers once from the files it serves.
func pwaServer(mode string) http.Handler {
	s := newServer(func(d *Deps) { d.Cfg.CSP = mode })
	s.static = fstest.MapFS{
		"index.html":           {Data: []byte(nonceIndex)},
		"assets/x.js":          {Data: []byte(`console.log(1)`)},
		"sw.js":                {Data: []byte(pwaWorker)},
		"manifest.webmanifest": {Data: []byte(pwaManifest)},
		"offline.html":         {Data: []byte(pwaOfflinePage)},
		"icons/icon-192.png":   {Data: []byte("\x89PNG\r\n\x1a\n")},
	}
	s.pwa = pwaFiles(s.static)
	return s.Handler()
}

func pwaETag(body string) string {
	sum := sha256.Sum256([]byte(body))
	return `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`
}

// The service worker and the offline page always carry their own enforcing policy, whatever
// server.csp says (it is the kill switch for the document's policy, and these two cannot blank the
// UI). The manifest and the icons follow the mode like every other response. Each file comes with
// its headers and its bytes as built.
func TestPWAFilesGetTheirHeadersInEveryCSPMode(t *testing.T) {
	sum := sha256.Sum256([]byte(pwaOfflineCSS))
	styleHash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	offline := "default-src 'none'; style-src " + styleHash +
		"; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
	worker := "default-src 'none'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'"
	for _, tc := range []struct {
		mode, name string // name: the header that carries the mode's policy ("" = none)
	}{
		{config.CSPEnforce, "Content-Security-Policy"},
		{config.CSPReportOnly, "Content-Security-Policy-Report-Only"},
		{config.CSPOff, ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h := pwaServer(tc.mode)
			for _, f := range []struct {
				path, contentType, cacheControl, etag, body string
				enforce, reportOnly                         []string // the policy headers' values
			}{
				{"/sw.js", "text/javascript; charset=utf-8", "no-cache", pwaETag(pwaWorker), pwaWorker, []string{worker}, nil},
				{"/offline.html", "text/html; charset=utf-8", "no-cache", pwaETag(pwaOfflinePage), pwaOfflinePage, []string{offline}, nil},
				{"/manifest.webmanifest", "application/manifest+json", "no-cache", "", pwaManifest, nil, nil},
				{"/icons/icon-192.png", "image/png", "", "", "\x89PNG\r\n\x1a\n", nil, nil},
			} {
				if f.etag == "" { // follows the mode: the API policy under the mode's header
					switch tc.name {
					case "Content-Security-Policy":
						f.enforce = []string{apiPolicy}
					case "Content-Security-Policy-Report-Only":
						f.reportOnly = []string{apiPolicy}
					}
				}
				w := req{path: f.path}.do(h)
				if w.Code != http.StatusOK || w.Body.String() != f.body {
					t.Errorf("%s: status %d, body %q; want 200 and the file as built", f.path, w.Code, w.Body)
				}
				for name, want := range map[string]string{"Content-Type": f.contentType, "Cache-Control": f.cacheControl, "ETag": f.etag} {
					if got := w.Header().Get(name); got != want {
						t.Errorf("%s: %s = %q, want %q", f.path, name, got, want)
					}
				}
				enforce, reportOnly := policyHeaders(w.Header())
				if strings.Join(enforce, "|") != strings.Join(f.enforce, "|") || strings.Join(reportOnly, "|") != strings.Join(f.reportOnly, "|") {
					t.Errorf("%s: policy %q and report-only %q, want %q and %q", f.path, enforce, reportOnly, f.enforce, f.reportOnly)
				}
			}
		})
	}
}

// The ETag of the worker and the offline page lets the browser's update check revalidate: the
// same bytes are a 304 with no body.
func TestPWAFilesRevalidateByETag(t *testing.T) {
	h := pwaServer(config.CSPEnforce)
	for path, body := range map[string]string{"/sw.js": pwaWorker, "/offline.html": pwaOfflinePage} {
		w := req{path: path, header: map[string]string{"If-None-Match": pwaETag(body)}}.do(h)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("%s with its ETag: status %d, body %q; want 304 and none", path, w.Code, w.Body)
		}
		w = req{path: path, header: map[string]string{"If-None-Match": `"other"`}}.do(h)
		if w.Code != http.StatusOK || w.Body.String() != body {
			t.Errorf("%s with another ETag: status %d; want 200 and the file", path, w.Code)
		}
	}
}

// The offline page is kept by the service worker, so its policy carries no nonce, and its inline
// CSS is allowed by hash, never by 'unsafe-inline'. Every <style> element is hashed; a page
// without one gets no style source at all.
func TestOfflinePolicyHashesEveryStyleElement(t *testing.T) {
	hash := func(css string) string {
		sum := sha256.Sum256([]byte(css))
		return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	for _, tc := range []struct {
		name, page, wantStyle string // wantStyle "" = no style-src directive
	}{
		{"none", "<p>offline</p>", ""},
		{"one", "<style>a{}</style>", "style-src " + hash("a{}")},
		{"two, attributes and case", `<STYLE media="all">a{}</STYLE><style>
b{}</style>`, "style-src " + hash("a{}") + " " + hash("\nb{}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := offlinePolicy([]byte(tc.page))
			var style string
			for d := range strings.SplitSeq(v, "; ") {
				if strings.HasPrefix(d, "style-src") {
					style = d
				}
			}
			if style != tc.wantStyle {
				t.Errorf("style directive = %q, want %q (policy %q)", style, tc.wantStyle, v)
			}
			if !strings.HasPrefix(v, "default-src 'none';") || strings.Contains(v, "nonce-") || strings.Contains(v, "unsafe-inline") {
				t.Errorf("policy = %q", v)
			}
		})
	}
}
