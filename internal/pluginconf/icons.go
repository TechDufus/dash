package pluginconf

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/TechDufus/dash/internal/model"
)

// iconName is the grammar of an icon name (a dashboard-icons name such as "proxmox",
// "unifi-drive" or "grafana.svg"); the SPA's iconSrc resolves it against ui.iconBaseURL.
var iconName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidateIcons checks the icon token of every launcher link against ui.iconBaseURL. A token is
// valid when it is empty, a name, a same-origin path (one leading "/", no backslash, no control
// character), or an absolute http(s) URL whose origin is the base's and whose path lies under the
// base's path. Anything else (another origin or a path outside the base, a "//host/x"
// protocol-relative reference, a javascript: or data: URL) is refused with "entry N: icon …", N
// counting from 1. The error says why and never repeats the token: a URL can carry credentials or
// a query.
func ValidateIcons(links []model.LauncherLink, iconBaseURL string) error {
	base := parseIconBase(iconBaseURL)
	for i, l := range links {
		if reason := iconProblem(strings.TrimSpace(l.Icon), base); reason != "" {
			return fmt.Errorf("entry %d: icon %s", i+1, reason)
		}
	}
	return nil
}

// iconBase is ui.iconBaseURL as it is compared: the origin and the path every icon URL must lie
// under, "/" for an origin-only base.
type iconBase struct {
	scheme, host, port, prefix string
}

// parseIconBase returns nil for an empty base or one that is not an absolute http(s) URL: no
// absolute icon URL is valid then.
func parseIconBase(raw string) *iconBase {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil
	}
	return &iconBase{
		scheme: u.Scheme,
		host:   strings.ToLower(u.Hostname()),
		port:   effectivePort(u),
		prefix: strings.TrimRight(u.Path, "/") + "/",
	}
}

// effectivePort is the URL's port, the scheme's default when it has none.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// iconProblem is why token is not a valid icon, or "" when it is valid.
func iconProblem(token string, base *iconBase) string {
	switch {
	case token == "":
		return ""
	case hasControl(token):
		return "contains a control character"
	case token[0] == '/':
		if strings.HasPrefix(token, "//") || strings.Contains(token, `\`) {
			return "is a protocol-relative or backslash path, not a same-origin path"
		}
		return ""
	case iconName.MatchString(token):
		return ""
	}
	lower := strings.ToLower(token)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "is not an icon name, a same-origin path or an http(s) URL under ui.iconBaseURL"
	}
	if base == nil {
		return "is an image URL but ui.iconBaseURL is not set"
	}
	if strings.Contains(token, `\`) {
		return "is an image URL with a backslash"
	}
	u, err := url.Parse(token)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return "is not a valid image URL"
	}
	if u.User != nil {
		return "is an image URL with credentials"
	}
	if u.Scheme != base.scheme || strings.ToLower(u.Hostname()) != base.host || effectivePort(u) != base.port {
		return "is an image URL outside the origin of ui.iconBaseURL"
	}
	// u.Path is decoded, so "%2e%2e" is caught with "..": a browser resolves both before the
	// request, which would lead out of the base path.
	if !strings.HasPrefix(u.Path, base.prefix) || hasDotSegment(u.Path) || strings.Contains(u.Path, `\`) {
		return "is an image URL outside the path of ui.iconBaseURL"
	}
	return ""
}

// hasControl reports a C0 control character (tab, CR and LF included: a URL parser drops them
// silently) or DEL.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// hasDotSegment reports a "." or ".." path segment.
func hasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}
