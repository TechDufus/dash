package config

import (
	"regexp"
	"strings"
)

// originRE is a browser Origin as server.corsOrigins lists it: http or https, a host name or a
// bracketed IPv6 address, an optional port, nothing after it. A browser sends it lowercased.
var originRE = regexp.MustCompile(`^https?://(\[[0-9a-f:.]+\]|[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*)(:[0-9]{1,5})?$`)

// normalizeOrigins trims, lowercases and drops a trailing slash from each entry, and drops the
// empty ones; Validate checks what is left.
func normalizeOrigins(in []string) []string {
	var out []string
	for _, o := range in {
		if o = strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/")); o != "" {
			out = append(out, o)
		}
	}
	return out
}
