package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// hostNameRE matches a DNS host name: dot-separated labels of lowercase letters, digits and hyphens.
var hostNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

// Values of server.csp.
const (
	CSPEnforce    = "enforce"
	CSPReportOnly = "report-only"
	CSPOff        = "off"
)

// validator collects every problem, each named by its path and the layer that set it, so an
// operator knows which variable or file to fix.
type validator struct {
	prov   map[string]Provenance
	errs   []error
	failed map[string]bool // the paths that already have a problem
}

// where says which layer set the value at path: " (env DASH_PLUGINS_PROMETHEUS_URL)", " (file /etc/dash/config.yaml)".
func where(prov map[string]Provenance, path string) string {
	p := prov[path]
	switch p.Source {
	case SourceEnv, SourceFile:
		return fmt.Sprintf(" (%s %s)", p.Source, p.Ref)
	case SourceUI:
		return " (UI settings)"
	}
	return ""
}

func (v *validator) fail(path, format string, args ...any) {
	if v.failed == nil {
		v.failed = map[string]bool{}
	}
	v.failed[path] = true
	// The text keeps the layer; FieldErrors finds the path (a candidate's form shows it there).
	msg := fmt.Sprintf(format, args...)
	v.errs = append(v.errs, &fieldError{error: fmt.Errorf("%s%s: %s", path, where(v.prov, path), msg), field: &FieldError{Path: path, Message: msg}})
}

// section reports what a pluginconf section's Validate found, each problem at its full path
// under prefix. A setting the generic checks already rejected before the section ran is not
// reported twice; problems of the section itself at one path all are, as they were before the
// sections moved to pluginconf.
func (v *validator) section(prefix string, err error) {
	earlier := maps.Clone(v.failed)
	for _, fe := range pluginconf.Problems(err) {
		path := prefix
		if fe.Path != "" {
			path += "." + fe.Path
		}
		if !earlier[path] {
			v.fail(path, "%s", fe.Msg)
		}
	}
}

// validate reports settings that are missing or unusable.
func validate(t Tree, dev, demo bool, prov map[string]Provenance) error {
	v := &validator{prov: prov}
	s := t.Server

	if s.PublicURL == "" && !dev && !demo {
		v.fail("server.publicURL", "is required (the URL users open, e.g. https://dash.example.com); set %s or server.publicURL in the config file", envName("server.publicURL"))
	}
	for _, l := range schema.leaves {
		if !l.url {
			continue
		}
		// The message never repeats the value: a URL can carry a password (user:password@host).
		if u := l.field(&t).String(); u != "" && !httpURL(u) {
			v.fail(l.path, "must be an http(s) URL with a host, e.g. https://example.com")
		}
	}
	if dev && !LoopbackAddr(s.Listen) {
		v.fail("server.listen", "DASH_DEV=true signs requests in as %q without authentication, so the listen address must be a loopback address such as 127.0.0.1:8080", "dev")
	}
	// Dev and demo sign in their own fixed user whatever auth.mode says, so the rule is theirs above.
	if t.Auth.Mode == AuthModeNone && !dev && !demo && !t.Auth.None.AllowRemote && !LoopbackAddr(s.Listen) {
		v.fail("server.listen", "auth.mode=none signs every request in as an administrator without authentication, so it needs a loopback listen address: set %s=127.0.0.1:8080 (in a container only with --network host: -p cannot reach a loopback listener), or set %s=true (auth.none.allowRemote) knowing that anyone who can reach dash is an administrator", envName("server.listen"), envName("auth.none.allowRemote"))
	}
	// The message names the entry by its position, never its text: a URL pasted here can carry a
	// password (user:password@host). The position counts the list as normalize left it: every
	// entry split at its commas and the empty ones dropped, so ["a,b", "bad"] reports entry 3.
	for i, h := range s.AllowedHosts {
		if h != "*" && net.ParseIP(h) == nil && !hostNameRE.MatchString(h) {
			v.fail("server.allowedHosts", "entry %d is not a host name or IP address (no scheme or port)", i+1)
		}
	}
	for i, o := range s.CORSOrigins {
		if !originRE.MatchString(o) {
			v.fail("server.corsOrigins", "entry %d is not an origin: scheme://host[:port] with http or https and no path, never \"*\"", i+1)
		}
	}
	if s.ReadyTimeout <= 0 {
		v.fail("server.readyTimeout", "must be greater than zero")
	}
	if !slices.Contains([]string{CSPEnforce, CSPReportOnly, CSPOff}, s.CSP) {
		v.fail("server.csp", "must be one of: %s, %s, %s", CSPEnforce, CSPReportOnly, CSPOff)
	}
	known := map[string]bool{}
	for _, sec := range t.PluginSections() {
		known[sec.Name] = true
	}
	for i, id := range s.ReadyRequires {
		if !known[id] {
			v.fail("server.readyRequires", "entry %d is not a plugin (one of: %s)", i+1, strings.Join(slices.Sorted(maps.Keys(known)), ", "))
		}
	}
	validateLimits(v, s.Limits)
	if t.Settings.UI != SettingsEditable && t.Settings.UI != SettingsReadOnly {
		v.fail("settings.ui", "must be one of: %s, %s", SettingsEditable, SettingsReadOnly)
	}
	validateSettings(v, t.Settings)
	for _, sec := range t.PluginSections() {
		if sec.Type != sec.Name {
			v.fail("plugins."+sec.Name+".type", "must be empty or %q: dash runs one instance per plugin type, and the instance name is the type (design J.16)", sec.Name)
		}
	}

	// Every section that knows its own cross-field rules says so (auth, header names, launcher
	// links, ...); the ones still in this package are checked above.
	v.section("ui", t.UI.Validate())
	v.section("auth", t.Auth.Validate())
	refused, _ := proxyKeysOutsideProxyMode(t, prov, dev, demo)
	// Someone who never set auth.mode is told it is the default: v0.11.x defaulted to proxy.
	mode := t.Auth.Mode
	if src := prov["auth.mode"].Source; src == SourceDefault || src == "" {
		mode += " (the default since v0.12.0)"
	}
	for _, path := range refused {
		v.fail(path, "is set, but auth.mode is %s and the proxy admin lists are read only in auth.mode=proxy: set DASH_AUTH_MODE=proxy if dash runs behind an identity proxy, or remove this setting", mode)
	}
	if t.Auth.Mode == AuthModeOIDC && !dev && !demo && s.PublicURL != "" {
		if u, err := url.Parse(s.PublicURL); err == nil && u.Scheme != "https" && !loopbackHost(u.Hostname()) {
			v.fail("server.publicURL", "must be https in auth.mode=oidc: the provider sends the browser back to it with the login code (http only for a loopback host)")
		}
	}
	// server.directURL (contract D20): checked once the url rule above passed it.
	if s.DirectURL != "" && !v.failed["server.directURL"] {
		if problem := directURLProblem(s.DirectURL, s.PublicURL); problem != "" {
			v.fail("server.directURL", "%s", problem)
		} else if t.Auth.Mode == AuthModeNone && !dev && !demo {
			v.fail("server.directURL", "is set, but auth.mode=none has no OAuth and no bearer clients: remove it")
		}
	}
	// OAuth names its endpoints from the origins (the issuer, /oauth/authorize on publicURL), and
	// an issuer with a path would move the /.well-known documents under it (RFC 8414 3.1), where
	// nothing serves them.
	if t.Auth.OAuth.Enabled && s.PublicURL != "" && !v.failed["server.publicURL"] {
		if u, err := url.Parse(s.PublicURL); err == nil && u.Path != "" {
			v.fail("server.publicURL", "has a path, which auth.oauth.enabled cannot serve: the issuer, /oauth/* and the /.well-known documents are at the root of the origin")
		}
	}
	validatePush(t.Push, v)
	// An inline launcher icon outside the allowed forms is a load error (the links of a launcher
	// file are only read by the launcher's start, which turns its row to error instead). The
	// message names the entry, never the icon.
	if err := pluginconf.ValidateIcons(t.Plugins.Launcher.Links, t.UI.IconBaseURL); err != nil && !v.failed["plugins.launcher.links"] {
		v.fail("plugins.launcher.links", "%v; use an icon name, a same-origin path or an image URL under ui.iconBaseURL", err)
	}
	for _, sec := range t.PluginSections() {
		if val, ok := sec.Value.(interface{ Validate() error }); ok {
			v.section("plugins."+sec.Name, val.Validate())
		}
	}
	return errors.Join(v.errs...)
}

// validateLimits needs every limit above zero (zero would turn the stream cap into "none may
// open", or net/http's default header size into a silent surprise) and a per-user cap that fits
// inside the total.
func validateLimits(v *validator, l LimitsConfig) {
	for _, c := range []struct {
		path string
		n    int64
	}{
		{"streamsPerUser", int64(l.StreamsPerUser)}, {"streams", int64(l.Streams)},
		{"tailsPerUser", int64(l.TailsPerUser)}, {"tails", int64(l.Tails)},
		{"tailMaxDuration", int64(l.TailMaxDuration)}, {"sseWriteTimeout", int64(l.SSEWriteTimeout)},
		{"bodyReadTimeout", int64(l.BodyReadTimeout)}, {"maxHeaderBytes", int64(l.MaxHeaderBytes)},
	} {
		if c.n <= 0 {
			v.fail("server.limits."+c.path, "must be greater than zero")
		}
	}
	if l.StreamsPerUser > l.Streams {
		v.fail("server.limits.streamsPerUser", "must not be more than server.limits.streams")
	}
	if l.TailsPerUser > l.Tails {
		v.fail("server.limits.tailsPerUser", "must not be more than server.limits.tails")
	}
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// splitHosts splits a comma-separated list, trimming and lowercasing entries and dropping empty ones.
func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}
