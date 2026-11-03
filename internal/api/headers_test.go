package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

const hdrNonce = "AAAAAAAAAAAAAAAAAAAAAA"

// hdrDirective returns the value of one directive of a policy and whether it is present.
func hdrDirective(policy, name string) (string, bool) {
	for part := range strings.SplitSeq(policy, ";") {
		part = strings.TrimSpace(part)
		if d, v, _ := strings.Cut(part, " "); d == name {
			return v, true
		}
	}
	return "", false
}

// hdrServe runs one request through the middleware around h and returns the recorder.
func hdrServe(mode cspMode, path string, h http.HandlerFunc) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	securityHeaders(mode)(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func hdrOK(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }

func TestCSPImgSrcCarriesTheIconBasePathWithATrailingSlash(t *testing.T) {
	for _, tc := range []struct {
		name, base, want string // want: the sources after 'self' data:, "" for none
	}{
		{"with a path", "https://cdn.example.com/icons/png", "https://cdn.example.com/icons/png/"},
		{"with a path and a trailing slash", "https://cdn.example.com/icons/png/", "https://cdn.example.com/icons/png/"},
		{"with a deep path", "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png", "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/"},
		{"without a path", "https://cdn.example.com", "https://cdn.example.com/"},
		{"origin only, with the slash", "https://cdn.example.com/", "https://cdn.example.com/"},
		{"origin only, many slashes", "https://cdn.example.com///", "https://cdn.example.com/"},
		{"with a port", "http://icons.lan:8080/png", "http://icons.lan:8080/png/"},
		{"host is lowercased", "https://CDN.Example.COM/Icons", "https://cdn.example.com/Icons/"},
		{"query is stripped", "https://cdn.example.com/icons?v=3;x=1", "https://cdn.example.com/icons/"},
		{"fragment is stripped", "https://cdn.example.com/icons#top", "https://cdn.example.com/icons/"},
		{"userinfo is stripped", "https://user:pass@cdn.example.com/icons", "https://cdn.example.com/icons/"},
		{"surrounding spaces are trimmed", "  https://cdn.example.com/icons  ", "https://cdn.example.com/icons/"},
		// A base that cannot become a safe source expression adds nothing, never a wrong source.
		{"empty", "", ""},
		{"javascript scheme", "javascript:alert(1)", ""},
		{"semicolon in the path", "https://cdn.example.com/a;b", ""},
		{"data scheme", "data:image/png;base64,AAAA", ""},
		{"ftp scheme", "ftp://cdn.example.com/icons", ""},
		{"scheme-relative", "//cdn.example.com/icons", ""},
		{"no host", "https:///icons", ""},
		{"policy separator in the path", "https://cdn.example.com/a;script-src *", ""},
		{"comma in the path", "https://cdn.example.com/a,b", ""},
		{"quote in the path", "https://cdn.example.com/a'b", ""},
		{"ipv6 host", "https://[::1]/icons", ""},
		{"wildcard host", "https://*.example.com/icons", ""},
		{"not a url", "::::", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, v, ok := documentPolicy(cspEnforce, hdrNonce, tc.base)
			if !ok || name != "Content-Security-Policy" {
				t.Fatalf("documentPolicy = %q, ok %v", name, ok)
			}
			img, found := hdrDirective(v, "img-src")
			if !found {
				t.Fatalf("no img-src in %q", v)
			}
			want := "'self' data:"
			if tc.want != "" {
				want += " " + tc.want
			}
			if img != want {
				t.Errorf("img-src = %q, want %q", img, want)
			}
			if tc.want != "" && !strings.HasSuffix(tc.want, "/") {
				t.Errorf("test table row %q does not end in a slash", tc.want)
			}
		})
	}
}

func TestDocumentPolicyIsTheDesignedPolicy(t *testing.T) {
	_, v, ok := documentPolicy(cspEnforce, hdrNonce, "https://cdn.example.com/icons/png")
	if !ok {
		t.Fatal("not ok")
	}
	want := "default-src 'self'; script-src 'self'; style-src 'self' 'nonce-" + hdrNonce + "'; " +
		"img-src 'self' data: https://cdn.example.com/icons/png/; connect-src 'self'; worker-src 'self'; " +
		"manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'; form-action 'self'"
	if v != want {
		t.Errorf("policy =\n%s\nwant\n%s", v, want)
	}
	for _, banned := range []string{"unsafe-inline", "unsafe-eval", "font-src", "report-uri"} {
		if strings.Contains(v, banned) {
			t.Errorf("policy contains %q: %s", banned, v)
		}
	}
	if script, _ := hdrDirective(v, "script-src"); script != "'self'" {
		t.Errorf("script-src = %q, want 'self' alone", script)
	}
}

func TestDocumentPolicyRefusesANonceThatIsNotBase64(t *testing.T) {
	for _, nonce := range []string{"", "a b", "a'; script-src *", "a;b", "a,b"} {
		if _, _, ok := documentPolicy(cspEnforce, nonce, ""); ok {
			t.Errorf("nonce %q accepted", nonce)
		}
	}
	// What newNonce produces is always accepted.
	n, err := newNonce()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := documentPolicy(cspEnforce, n, ""); !ok {
		t.Errorf("a nonce from newNonce (%q) was refused", n)
	}
}

func TestCSPContainsNoThirdPartyOriginByDefault(t *testing.T) {
	_, v, ok := documentPolicy(cspEnforce, hdrNonce, "")
	if !ok {
		t.Fatal("not ok")
	}
	origin := regexp.MustCompile(`(?i)(https?|wss?)://|\*|\b[a-z0-9-]+\.[a-z]{2,}\b`)
	if m := origin.FindString(v); m != "" {
		t.Errorf("document policy names a host (%q) with no icon base: %s", m, v)
	}
	if img, _ := hdrDirective(v, "img-src"); img != "'self' data:" {
		t.Errorf("img-src = %q, want 'self' data: alone", img)
	}
	rec := hdrServe(cspEnforce, "/api/v1/me", hdrOK)
	if got := rec.Header().Get("Content-Security-Policy"); origin.MatchString(got) {
		t.Errorf("API policy names a host: %s", got)
	}
}

func TestCSPModeOffSendsNoPolicy(t *testing.T) {
	if name, v, ok := documentPolicy(cspOff, hdrNonce, "https://cdn.example.com/icons"); ok || name != "" || v != "" {
		t.Errorf("documentPolicy(off) = %q, %q, %v; want nothing", name, v, ok)
	}
	rec := hdrServe(cspOff, "/", hdrOK)
	for _, h := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
		if got := rec.Header().Values(h); len(got) != 0 {
			t.Errorf("%s = %q with the mode off, want none", h, got)
		}
	}
	// The other two headers are not part of the kill switch.
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers with the mode off = %v, want nosniff and same-origin", rec.Header())
	}
}

func TestCSPModeReportOnlyUsesTheReportOnlyHeader(t *testing.T) {
	name, v, ok := documentPolicy(cspReportOnly, hdrNonce, "")
	if !ok || name != "Content-Security-Policy-Report-Only" {
		t.Fatalf("documentPolicy(report-only) = %q, %v; want the report-only header", name, ok)
	}
	if _, enforcing, _ := documentPolicy(cspEnforce, hdrNonce, ""); v != enforcing {
		t.Errorf("report-only policy differs from the enforced one:\n%s\n%s", v, enforcing)
	}
	if strings.Contains(v, "report-uri") || strings.Contains(v, "report-to") {
		t.Errorf("report-only policy names a report endpoint: %s", v)
	}

	rec := hdrServe(cspReportOnly, "/api/v1/me", hdrOK)
	if got := rec.Header().Get("Content-Security-Policy-Report-Only"); got != "default-src 'none'; frame-ancestors 'none'" {
		t.Errorf("report-only API policy = %q", got)
	}
	if got := rec.Header().Values("Content-Security-Policy"); len(got) != 0 {
		t.Errorf("enforcing header present in report-only mode: %q", got)
	}
}

func TestEnforceModeUsesTheEnforcingHeaderAndUnknownModesEnforce(t *testing.T) {
	for _, mode := range []cspMode{cspEnforce, "", "bogus"} {
		rec := hdrServe(mode, "/x", hdrOK)
		if got := rec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; frame-ancestors 'none'" {
			t.Errorf("mode %q: Content-Security-Policy = %q", mode, got)
		}
		if got := rec.Header().Values("Content-Security-Policy-Report-Only"); len(got) != 0 {
			t.Errorf("mode %q: report-only header present: %q", mode, got)
		}
		if name, _, ok := documentPolicy(mode, hdrNonce, ""); !ok || name != "Content-Security-Policy" {
			t.Errorf("mode %q: documentPolicy header = %q, %v", mode, name, ok)
		}
	}
}

func TestDocumentPolicyReplacesTheGenericOneOnTheResponse(t *testing.T) {
	for _, mode := range []cspMode{cspEnforce, cspReportOnly} {
		var want string
		rec := hdrServe(mode, "/", func(w http.ResponseWriter, _ *http.Request) {
			name, v, ok := documentPolicy(mode, hdrNonce, "https://cdn.example.com/icons")
			if !ok {
				t.Fatal("not ok")
			}
			want = v
			w.Header().Set(name, v)
			_, _ = io.WriteString(w, "<html>")
		})
		name := mode.headerName()
		if got := rec.Header().Values(name); len(got) != 1 || got[0] != want {
			t.Errorf("mode %s: %s = %q, want exactly the document policy", mode, name, got)
		}
		other := cspHeader
		if name == cspHeader {
			other = cspReportOnlyHeader
		}
		if got := rec.Header().Values(other); len(got) != 0 {
			t.Errorf("mode %s: %s = %q, want none", mode, other, got)
		}
	}
	// Off: the document has no policy to set, and the response carries none.
	rec := hdrServe(cspOff, "/", func(w http.ResponseWriter, _ *http.Request) {
		if name, v, ok := documentPolicy(cspOff, hdrNonce, ""); ok {
			w.Header().Set(name, v)
		}
	})
	if got := rec.Header().Get("Content-Security-Policy") + rec.Header().Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("document with the mode off carries %q", got)
	}
}

func TestSecurityHeadersMiddlewareSetsAllThree(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		h      http.HandlerFunc
	}{
		{"200", 200, hdrOK},
		{"404", 404, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) }},
		{"405", 405, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusMethodNotAllowed) }},
		{"no body", 204, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }},
		{"redirect", 302, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/x", http.StatusFound) }},
		{"never writes", 200, func(http.ResponseWriter, *http.Request) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := hdrServe(cspEnforce, "/api/v1/anything", tc.h)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			h := rec.Result().Header
			for k, want := range map[string]string{
				"X-Content-Type-Options":  "nosniff",
				"Referrer-Policy":         "same-origin",
				"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
			} {
				if got := h.Values(k); len(got) != 1 || got[0] != want {
					t.Errorf("%s = %q, want exactly %q", k, got, want)
				}
			}
		})
	}
}

func TestNoStoreOnCookieAndAuthResponses(t *testing.T) {
	setCookie := func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "dash_session", Value: "x", Path: "/", HttpOnly: true})
	}
	for _, tc := range []struct {
		name, path string
		h          http.HandlerFunc
		want       string // the Cache-Control the response must carry
	}{
		// Auth routes: always, whatever the handler said and whether or not it sets a cookie.
		{"login document", "/login", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = io.WriteString(w, "<html>")
		}, "no-store"},
		{"setup document", "/setup", hdrOK, "no-store"},
		{"login post", "/login", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }, "no-store"},
		{"auth logout", "/auth/logout", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }, "no-store"},
		{"oidc start redirect", "/auth/oidc/start", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://idp.example/authorize", http.StatusFound)
		}, "no-store"},
		{"oidc callback", "/auth/callback", hdrOK, "no-store"},
		{"below login", "/login/extra", hdrOK, "no-store"},
		{"unclean auth path", "/x/../auth/logout", hdrOK, "no-store"},
		{"empty 200 body on login", "/login", func(http.ResponseWriter, *http.Request) {}, "no-store"},
		// Any response that sets a cookie, on any path, in every way a handler can start a response.
		{"cookie then write", "/api/v1/me", func(w http.ResponseWriter, _ *http.Request) {
			setCookie(w)
			_, _ = io.WriteString(w, "{}")
		}, "no-store"},
		{"cookie then WriteHeader", "/anything", func(w http.ResponseWriter, _ *http.Request) {
			setCookie(w)
			w.WriteHeader(http.StatusNoContent)
		}, "no-store"},
		{"cookie then Flush", "/anything", func(w http.ResponseWriter, _ *http.Request) {
			setCookie(w)
			w.(http.Flusher).Flush()
		}, "no-store"},
		{"cookie then redirect", "/anything", func(w http.ResponseWriter, r *http.Request) {
			setCookie(w)
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}, "no-store"},
		{"cookie over a cacheable header", "/api/v1/topics/x", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
			setCookie(w)
			_, _ = io.WriteString(w, "{}")
		}, "no-store"},
		// Everything else keeps what its handler chose.
		{"plain response has no header from the middleware", "/", hdrOK, ""},
		{"handler caching is kept", "/assets/app.js", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			_, _ = io.WriteString(w, "x")
		}, "public, max-age=31536000, immutable"},
		{"snapshot revalidation is kept", "/api/v1/topics/x", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
			_, _ = io.WriteString(w, "{}")
		}, "private, max-age=0, must-revalidate"},
		{"prefix lookalike /authority", "/authority", hdrOK, ""},
		{"prefix lookalike /loginx", "/loginx", hdrOK, ""},
		{"prefix lookalike /setups", "/setups/x", hdrOK, ""},
		{"login deeper in the path", "/x/login", hdrOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := hdrServe(cspEnforce, tc.path, tc.h)
			if got := rec.Result().Header.Values("Cache-Control"); tc.want == "" {
				if len(got) != 0 {
					t.Errorf("Cache-Control = %q, want none", got)
				}
			} else if len(got) != 1 || got[0] != tc.want {
				t.Errorf("Cache-Control = %q, want exactly %q", got, tc.want)
			}
		})
	}
}

// A 1xx response is not the final header: no-store must still be applied to the final one. The
// recorder snapshots headers at the first WriteHeader, so this runs against a real server.
func TestNoStoreIsAppliedToTheFinalHeaderAfterAnInformationalOne(t *testing.T) {
	srv := httptest.NewServer(securityHeaders(cspEnforce)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		http.SetCookie(w, &http.Cookie{Name: "c", Value: "v"})
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestFlushStillWorksThroughTheMiddleware(t *testing.T) {
	t.Run("Flusher and ResponseController reach the writer", func(t *testing.T) {
		rec := hdrServe(cspEnforce, "/api/v1/stream", func(w http.ResponseWriter, _ *http.Request) {
			f, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("the wrapped writer is not an http.Flusher")
			}
			_, _ = io.WriteString(w, "event: a\n\n")
			f.Flush()
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("ResponseController.Flush = %v", err)
			}
		})
		if !rec.Flushed {
			t.Error("the underlying writer was never flushed")
		}
	})

	t.Run("a flush before any write still carries the headers", func(t *testing.T) {
		rec := hdrServe(cspEnforce, "/auth/events", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			w.(http.Flusher).Flush()
		})
		if !rec.Flushed {
			t.Error("never flushed")
		}
		if got := rec.Result().Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store (applied before the flush started the response)", got)
		}
	})

	t.Run("Unwrap keeps the deadline methods", func(t *testing.T) {
		d := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		dl := time.Unix(1_700_000_000, 0)
		securityHeaders(cspEnforce)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if err := http.NewResponseController(w).SetWriteDeadline(dl); err != nil {
				t.Errorf("SetWriteDeadline = %v", err)
			}
		})).ServeHTTP(d, httptest.NewRequest(http.MethodGet, "/", nil))
		if len(d.write) != 1 || !d.write[0].Equal(dl) {
			t.Errorf("deadlines reaching the writer = %v, want [%v]", d.write, dl)
		}
	})

	t.Run("a real server delivers the first event before the handler returns", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(securityHeaders(cspEnforce)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: one\n\n")
			w.(http.Flusher).Flush()
			// The stream stays open until the client has read the first event (or gave up).
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})))
		defer srv.Close()
		var once bool
		closeRelease := func() {
			if !once {
				once = true
				close(release)
			}
		}
		defer closeRelease()

		// The timeout only keeps a broken Flush from hanging the suite: without a flush the
		// client never sees the headers. It is not asserted on.
		resp, err := (&http.Client{Timeout: 20 * time.Second}).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("nosniff = %q on the streaming response", got)
		}
		buf := make([]byte, len("data: one\n\n"))
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("reading the flushed event: %v", err)
		}
		if string(buf) != "data: one\n\n" {
			t.Errorf("event = %q", buf)
		}
		closeRelease()
	})
}
