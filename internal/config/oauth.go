package config

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// AuthOAuthConfig is auth.oauth: dash's own OAuth 2.1 authorization server (design M.6). Every
// key needs a restart.
type AuthOAuthConfig struct {
	Enabled          bool          `json:"enabled" dash:"restart" doc:"Serve dash's OAuth 2.1 authorization server, so MCP clients and the Android app sign in through dash and receive tokens (/oauth/authorize, /oauth/token, /oauth/revoke and the /.well-known/oauth-* documents). Off by default: it adds an endpoint that needs no sign-in (/oauth/token). It only runs in auth.mode local, oidc and proxy with a writable data dir; otherwise those paths answer 404. Behind an identity proxy, set server.directURL so clients can reach the token endpoint."`
	CIMDAllowPrivate bool          `json:"cimdAllowPrivate" dash:"restart" doc:"Let dash fetch a client ID metadata document (an MCP client whose client_id is an https URL) from a private (RFC 1918, CGNAT, ULA) or loopback address. Off by default: dash fetches them only from global addresses, so a client_id cannot make dash probe the local network. Link-local addresses, which hold cloud metadata services, stay refused."`
	Clients          []OAuthClient `json:"clients" dash:"restart" doc:"OAuth clients registered in config, as [{clientID, name, redirectURIs, audiences}]: clientID 1 to 128 visible characters that are not a URL and not dash-android, a name to show on the consent screen, redirect URIs that are https, http on a loopback host (any port matches) or a private-use scheme containing a dot (com.example.app:/callback), and audiences from api and mcp. They may never be granted approvals or push. A JSON list in the environment."`
}

// OAuthClient is one auth.oauth.clients entry.
type OAuthClient struct {
	ClientID     string   `json:"clientID" doc:"The client_id the application sends: 1 to 128 visible characters, not a URL (a URL is a client ID metadata document) and not dash-android."`
	Name         string   `json:"name" doc:"The name the consent screen and Settings -> Connected apps show: 1 to 64 characters."`
	RedirectURIs []string `json:"redirectURIs" doc:"The exact redirect URIs the client may use: https, http on a loopback host (any port matches) or a private-use scheme containing a dot; no fragment."`
	Audiences    []string `json:"audiences" doc:"The resources the client may ask for: api, mcp or both."`
}

// OAuthAndroidClientID is the built-in first-party app; a config client may not take its ID.
const OAuthAndroidClientID = "dash-android"

// privateUseScheme is a URI scheme with a dot (RFC 8252 7.1: a reverse domain name).
var privateUseScheme = regexp.MustCompile(`^[a-z][a-z0-9+-]*(\.[a-z0-9+-]+)+$`)

// LoopbackRedirect reports whether u is an http redirect to a loopback host (127.0.0.0/8, ::1
// or localhost), whose port the client picks at run time (RFC 8252 7.3).
func LoopbackRedirect(u *url.URL) bool {
	return u.Scheme == "http" && loopbackHost(u.Hostname())
}

// RedirectURIProblem is why raw cannot be an OAuth redirect URI, or "" when it can (design
// M.6): https with a host, http on a loopback host, or a private-use scheme containing a dot;
// never a fragment or user information. It is the rule of client ID metadata documents and
// auth.oauth.clients alike. The message never repeats the URI.
func RedirectURIProblem(raw string) string {
	if raw == "" || len(raw) > 2048 || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "is not a URI: empty, longer than 2048 bytes, or with spaces or control characters"
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Scheme == "":
		return "is not an absolute URI"
	case u.Fragment != "" || strings.Contains(raw, "#"):
		return "has a fragment, which a redirect URI never carries"
	case u.User != nil:
		return "has user information"
	case u.Scheme == "https":
		if u.Hostname() == "" {
			return "needs a host"
		}
	case u.Scheme == "http":
		if !LoopbackRedirect(u) {
			return "is http on a host that is not loopback: use https, or 127.0.0.1, [::1] or localhost"
		}
	case privateUseScheme.MatchString(u.Scheme):
		// A private-use scheme (com.example.app:/callback): the app claims it on the device.
	default:
		return "needs https, http on a loopback host, or a private-use scheme containing a dot"
	}
	return ""
}

// validOAuthClientID is 1 to 128 printable, non-space characters.
func validOAuthClientID(id string) bool {
	if id == "" || len(id) > 128 || !utf8.ValidString(id) {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) })
}

// validate reports unusable auth.oauth settings through add, by position and never by value.
func (o AuthOAuthConfig) validate(add func(path, format string, args ...any)) {
	ids := map[string]bool{}
	for i, c := range o.Clients {
		n := i + 1
		switch lower := strings.ToLower(c.ClientID); {
		case !validOAuthClientID(c.ClientID):
			add("oauth.clients", "entry %d needs a clientID of 1 to 128 visible characters without spaces", n)
		case c.ClientID == OAuthAndroidClientID:
			add("oauth.clients", "entry %d takes the clientID of the built-in app, dash-android", n)
		case strings.HasPrefix(lower, "https:") || strings.HasPrefix(lower, "http:"):
			add("oauth.clients", "entry %d has a URL as its clientID: a URL client_id is a client ID metadata document, which dash fetches; use a plain name", n)
		case ids[c.ClientID]:
			add("oauth.clients", "entry %d repeats the clientID of an earlier entry", n)
		}
		ids[c.ClientID] = true
		if name := strings.TrimSpace(c.Name); name == "" || utf8.RuneCountInString(c.Name) > 64 || !utf8.ValidString(c.Name) ||
			strings.ContainsFunc(c.Name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			add("oauth.clients", "entry %d needs a name of 1 to 64 characters without control characters", n)
		}
		if len(c.RedirectURIs) == 0 {
			add("oauth.clients", "entry %d needs at least one redirect URI", n)
		}
		for j, r := range c.RedirectURIs {
			if problem := RedirectURIProblem(r); problem != "" {
				add("oauth.clients", "entry %d redirect URI %d %s", n, j+1, problem)
			}
		}
		if len(c.Audiences) == 0 {
			add("oauth.clients", "entry %d needs at least one audience: api, mcp or both", n)
		}
		for j, a := range c.Audiences {
			if a != "api" && a != "mcp" {
				add("oauth.clients", "entry %d audience %d is not api or mcp", n, j+1)
			}
		}
	}
}

// directURLProblem is why direct cannot be server.directURL next to public, or "" when it can
// (contract D20): an origin (scheme, host, optional port; no path, query or fragment) other than
// the public URL's, https unless the host is loopback. The message never repeats the value.
func directURLProblem(direct, public string) string {
	u, err := url.Parse(direct)
	if err != nil || u.Host == "" {
		return "must be an http(s) URL with a host, e.g. https://dash-direct.example.com"
	}
	switch {
	case u.User != nil:
		return "must not carry user information"
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(direct, "?#"):
		return "must be an origin (scheme://host[:port]) without a path, query or fragment"
	case u.Scheme != "https" && !loopbackHost(u.Hostname()):
		return "must be https (http only for a loopback host): OAuth clients send their tokens to it"
	}
	if p, err := url.Parse(public); err == nil && strings.EqualFold(p.Scheme, u.Scheme) && strings.EqualFold(p.Host, u.Host) {
		return "must be another origin than server.publicURL: it is the address that bypasses the identity proxy"
	}
	return ""
}

// directHost is the host of server.directURL, normalized as HostMatcher compares it; "" when
// it is unset or does not parse.
func directHost(direct string) string {
	if direct == "" {
		return ""
	}
	u, err := url.Parse(direct)
	if err != nil {
		return ""
	}
	return normalizeHost(u.Hostname())
}
