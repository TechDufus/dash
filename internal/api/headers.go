package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"

	"golang.org/x/net/idna"
)

// cspMode is the value of server.csp: which Content-Security-Policy header dash sends.
type cspMode string

const (
	// cspEnforce sends Content-Security-Policy. It is the default.
	cspEnforce cspMode = "enforce"
	// cspReportOnly sends Content-Security-Policy-Report-Only: violations show in the browser
	// console and nothing is blocked (no report-uri).
	cspReportOnly cspMode = "report-only"
	// cspOff sends no policy at all: the kill switch for a browser difference.
	cspOff cspMode = "off"
)

const (
	cspHeader           = "Content-Security-Policy"
	cspReportOnlyHeader = "Content-Security-Policy-Report-Only"

	// apiPolicy is what every response but the SPA document gets: JSON, SSE, assets and errors
	// load nothing and may not be framed.
	apiPolicy = "default-src 'none'; frame-ancestors 'none'"
)

// headerName returns the header that carries the policy in this mode, or "" for none. Any value
// that is not report-only or off enforces: a mode nobody validated never silently drops the policy.
func (m cspMode) headerName() string {
	switch m {
	case cspOff:
		return ""
	case cspReportOnly:
		return cspReportOnlyHeader
	default:
		return cspHeader
	}
}

// securityHeaders is the middleware that sits just inside requestID. Every response gets
// X-Content-Type-Options: nosniff and Referrer-Policy: same-origin, and, unless the mode is off,
// the API policy under the mode's header name.
//
// The policy is set before the handler runs, so the SPA document replaces it with
// Header().Set(documentPolicy(...)): the name is the same, so the response never carries two.
//
// Cache-Control: no-store is forced when the response starts, after the handler chose its headers,
// on /login, /setup and /auth/* (and anything below them) and on any response that sets a
// cookie: a cookie-setting answer is never kept by an intermediary or the back/forward cache.
// Flush, FlushError and the deadline methods keep working through Unwrap.
func securityHeaders(mode cspMode) func(http.Handler) http.Handler {
	name := mode.headerName()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "same-origin")
			if name != "" {
				h.Set(name, apiPolicy)
			}
			nw := &noStoreWriter{ResponseWriter: w, authPath: authPath(r.URL.Path)}
			next.ServeHTTP(nw, r)
			nw.start() // a handler that never wrote still answers 200 with these headers
		})
	}
}

// authPath reports whether p is one of the login, setup or auth routes, or below one.
func authPath(p string) bool {
	p = path.Clean("/" + p)
	for _, root := range []string{"/login", "/setup", "/auth"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

// noStoreWriter forces Cache-Control: no-store on the first final response header when the route
// is an auth route or the response sets a cookie.
type noStoreWriter struct {
	http.ResponseWriter
	authPath bool
	started  bool
}

func (w *noStoreWriter) start() {
	if w.started {
		return
	}
	w.started = true
	h := w.Header()
	if w.authPath || len(h.Values("Set-Cookie")) > 0 {
		h.Set("Cache-Control", "no-store")
	}
}

func (w *noStoreWriter) WriteHeader(code int) {
	if code >= 200 { // 1xx are informational: the final header is still to come
		w.start()
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *noStoreWriter) Write(b []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(b)
}

func (w *noStoreWriter) Flush() { _ = w.FlushError() }

func (w *noStoreWriter) FlushError() error {
	w.start()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *noStoreWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// documentPolicy returns the header that the SPA document (index.html) carries instead of the
// API policy: the full policy of design P4.6 with the response's nonce. ok is false when the mode
// is off, or when the nonce is not base64 (newNonce never makes one that is not), and then the
// document sets no header and sends none of its own.
//
// img-src is 'self' data: plus the icon base as a source expression with its path and a trailing
// slash, so a CDN that serves arbitrary repositories cannot supply any image: a path source
// matches by prefix and the launcher only builds <base>/<name>.png. An empty or unusable base
// adds nothing: no third-party origin appears by default. (The path is defence in depth only: a
// browser decodes %2F before it matches a path source.)
func documentPolicy(mode cspMode, nonce, iconBase string) (name, value string, ok bool) {
	return documentPolicyWith(mode, nonce, iconSource(iconBase))
}

// documentPolicyWith is documentPolicy with the icon source expression already computed
// (iconSource, once at start): the server builds it in New, not on every page load.
func documentPolicyWith(mode cspMode, nonce, imgSource string) (name, value string, ok bool) {
	name = mode.headerName()
	if name == "" || !validNonce(nonce) {
		return "", "", false
	}
	img := "'self' data:"
	if imgSource != "" {
		img += " " + imgSource
	}
	return name, strings.Join([]string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self' 'nonce-" + nonce + "'",
		"img-src " + img,
		"connect-src 'self'",
		"worker-src 'self'",
		"manifest-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"object-src 'none'",
		"form-action 'self'",
	}, "; "), true
}

// validNonce reports whether n is a non-empty base64 string (standard alphabet), the only
// characters a nonce source may hold.
func validNonce(n string) bool {
	return n != "" && onlyBytes(n, func(c byte) bool {
		return isAlnum(c) || c == '+' || c == '/' || c == '='
	})
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// onlyBytes reports whether every byte of s satisfies ok.
func onlyBytes(s string, ok func(byte) bool) bool {
	for i := range len(s) {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

// iconSourceOrWarn is iconSource for the server's startup: a non-empty base that yields no source
// (an IPv6 host, a name with an underscore, a path or port the policy cannot carry) is logged
// once, with what the policy mode does to its icons: enforce blocks them, report-only reports
// them as violations, and off sends no policy, so there is nothing to warn about. The line carries
// the scheme and the host only, never the path, the query or any userinfo.
func iconSourceOrWarn(base string, mode cspMode, log *slog.Logger) string {
	src := iconSource(base)
	if src != "" || strings.TrimSpace(base) == "" {
		return src
	}
	var effect string
	switch mode.headerName() {
	case "":
		return ""
	case cspReportOnlyHeader:
		effect = "the browser will report every icon from it as a violation (report-only does not block it, the enforced policy would)"
	default:
		effect = "the browser will block every icon from it"
	}
	scheme, host := "", ""
	if u, err := url.Parse(strings.TrimSpace(base)); err == nil {
		scheme, host = u.Scheme, u.Hostname()
	}
	log.Warn("ui.iconBaseURL cannot be written as a Content-Security-Policy source, so "+effect+"; use a DNS host name (an international name is converted), a plain port and a path of letters, digits and - . _ ~",
		"scheme", scheme, "host", host)
	return ""
}

// iconSource turns ui.iconBaseURL into a CSP source expression: scheme://host[:port]/path/ with
// the path and a trailing slash. The userinfo, the query and the fragment are dropped (a source
// expression has no place for them, and a base is a prefix, never a single URL). An international
// host name is converted to its xn-- form. It returns "" for an empty base, a scheme other than
// http(s), a host that is not a DNS name or IPv4 address, or a path that holds a character a
// source expression cannot carry safely (the policy separators ';' and ',', spaces, quotes): such
// a base adds no image source rather than a wrong one.
func iconSource(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return ""
	}
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil {
		return ""
	}
	host = strings.ToLower(host)
	if !onlyBytes(host, func(c byte) bool { return isAlnum(c) || c == '.' || c == '-' }) {
		return ""
	}
	// The conversion can empty a label ("xn--", a soft hyphen or a zero-width space), leaving an
	// empty host or one like ".example" that is no DNS name and matches nothing as a source. A
	// single trailing dot (the root) may stay.
	if h := strings.TrimSuffix(host, "."); h == "" || strings.HasPrefix(h, ".") || strings.Contains(h, "..") {
		return ""
	}
	src := u.Scheme + "://" + host
	if p := u.Port(); p != "" {
		if !onlyBytes(p, func(c byte) bool { return c >= '0' && c <= '9' }) {
			return ""
		}
		src += ":" + p
	}
	p := strings.TrimRight(u.EscapedPath(), "/")
	if !onlyBytes(p, func(c byte) bool {
		return isAlnum(c) || c == '/' || c == '-' || c == '.' || c == '_' || c == '~' || c == '%'
	}) {
		return ""
	}
	return src + p + "/"
}
