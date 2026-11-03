package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TechDufus/dash/internal/config"
)

const iconBase = "https://cdn.example.com/icons/png"

var nonceInPolicy = regexp.MustCompile(`'nonce-([A-Za-z0-9+/]+)'`)

// cspServer is a Server over the real chain with a small SPA, in the given server.csp mode and
// with the given icon base.
func cspServer(mode, icons string) http.Handler {
	s := newServer(func(d *Deps) {
		d.Cfg.CSP = mode
		d.Cfg.IconBaseURL = icons
	})
	s.static = fstest.MapFS{
		"index.html":  {Data: []byte(nonceIndex)},
		"assets/x.js": {Data: []byte(`console.log(1)`)},
	}
	return s.Handler()
}

// policyHeaders is every Content-Security-Policy header of a response, either name.
func policyHeaders(h http.Header) (enforce, reportOnly []string) {
	return h.Values("Content-Security-Policy"), h.Values("Content-Security-Policy-Report-Only")
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	h := cspServer("", iconBase)
	for _, tc := range []struct {
		name     string
		r        req
		status   int
		document bool
	}{
		{"an API answer", req{path: "/api/v1/me"}, http.StatusOK, false},
		{"an API 404", req{path: "/api/v1/nosuch"}, http.StatusNotFound, false},
		{"a write to a read-only API route", req{method: http.MethodDelete, path: "/api/v1/me"}, 0, false},
		{"an unauthenticated API request", req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}, http.StatusUnauthorized, false},
		{"a refused host", req{path: "/api/v1/me", host: "evil.example.net"}, 0, false},
		{"an asset", req{path: "/assets/x.js"}, http.StatusOK, false},
		{"a missing asset", req{path: "/assets/none.js"}, http.StatusNotFound, false},
		{"the document", req{path: "/"}, http.StatusOK, true},
		{"a client-side route", req{path: "/alerts"}, http.StatusOK, true},
		{"the document refused for its method", req{method: http.MethodPost, path: "/"}, http.StatusMethodNotAllowed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.r.do(h)
			if tc.status != 0 && w.Code != tc.status {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q", got)
			}
			if got := w.Header().Get("Referrer-Policy"); got != "same-origin" {
				t.Errorf("Referrer-Policy = %q", got)
			}
			enforce, reportOnly := policyHeaders(w.Header())
			if len(enforce) != 1 || len(reportOnly) != 0 {
				t.Fatalf("policy headers = %q and report-only %q, want exactly one enforcing header", enforce, reportOnly)
			}
			switch {
			case tc.document:
				if !strings.HasPrefix(enforce[0], "default-src 'self';") || !nonceInPolicy.MatchString(enforce[0]) {
					t.Errorf("the document's policy is not the full one: %s", enforce[0])
				}
			case enforce[0] != apiPolicy:
				t.Errorf("policy = %q, want %q", enforce[0], apiPolicy)
			}
		})
	}
}

func TestSecurityHeadersOnAnSSEStream(t *testing.T) {
	h := cspServer("", "")
	resp, lines := sseClient(t, h, "/api/v1/stream")
	expect(t, lines, "retry: 3000")
	if got := resp.Header.Values("Content-Security-Policy"); len(got) != 1 || got[0] != apiPolicy {
		t.Errorf("policy = %q, want %q", got, apiPolicy)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers = %v", resp.Header)
	}
}

func TestCSPNonceMatchesTheMetaAndDiffersPerResponse(t *testing.T) {
	h := cspServer("", "")
	seen := map[string]bool{}
	for range 5 {
		w := req{path: "/"}.do(h)
		meta := nonceOf(t, w.Body.String())
		m := nonceInPolicy.FindStringSubmatch(w.Header().Get("Content-Security-Policy"))
		if m == nil {
			t.Fatalf("no nonce in %q", w.Header().Get("Content-Security-Policy"))
		}
		if m[1] != meta {
			t.Errorf("policy nonce %q != meta nonce %q", m[1], meta)
		}
		if seen[meta] {
			t.Errorf("nonce %q was issued twice", meta)
		}
		seen[meta] = true
	}
}

// The policy never carries an origin the operator did not configure, whatever the page.
func TestDocumentCSPContainsNoThirdPartyOriginByDefault(t *testing.T) {
	w := req{path: "/"}.do(cspServer("", ""))
	pol := w.Header().Get("Content-Security-Policy")
	if strings.Contains(pol, "http:") || strings.Contains(pol, "https:") || strings.Contains(pol, "//") {
		t.Errorf("a default policy names an origin: %s", pol)
	}
	if strings.Contains(pol, "unsafe-inline") || strings.Contains(pol, "unsafe-eval") {
		t.Errorf("the policy loosens script or style: %s", pol)
	}
}

func TestCSPImgSrcCarriesTheConfiguredIconBase(t *testing.T) {
	w := req{path: "/"}.do(cspServer("", iconBase))
	if pol := w.Header().Get("Content-Security-Policy"); !strings.Contains(pol, "img-src 'self' data: "+iconBase+"/;") {
		t.Errorf("img-src lacks the base with its path and a trailing slash: %s", pol)
	}
}

func TestCSPModesSelectTheHeaderOfTheDocumentAndTheAPI(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		wantName   string // "" = none
		wantAbsent []string
	}{
		{config.CSPEnforce, "Content-Security-Policy", []string{"Content-Security-Policy-Report-Only"}},
		{config.CSPReportOnly, "Content-Security-Policy-Report-Only", []string{"Content-Security-Policy"}},
		{config.CSPOff, "", []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h := cspServer(tc.mode, iconBase)
			for _, path := range []string{"/", "/api/v1/me", "/assets/x.js"} {
				w := req{path: path}.do(h)
				if tc.wantName != "" {
					if got := w.Header().Values(tc.wantName); len(got) != 1 {
						t.Errorf("%s: %s = %q, want one value", path, tc.wantName, got)
					}
				}
				for _, name := range tc.wantAbsent {
					if got := w.Header().Values(name); len(got) != 0 {
						t.Errorf("%s: %s = %q, want none", path, name, got)
					}
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Referrer-Policy") != "same-origin" {
					t.Errorf("%s: the other two headers are mode-independent: %v", path, w.Header())
				}
			}
		})
	}
}

// A refused host and a failed document are answered by the chain below the headers, so they carry
// them too: the middleware sits outside hosts.
func TestSecurityHeadersSurviveARefusedHost(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "evil.example.net"
	r.RemoteAddr = testPeer
	cspServer("", "").ServeHTTP(w, r)
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("a refused host lacks nosniff: %v", w.Header())
	}
}
