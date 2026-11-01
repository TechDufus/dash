package pluginconf

import (
	"net/url"
	"strings"
)

// UI is the top-level ui section: settings of the web UI that the server decides, because the
// browser is not trusted to.
type UI struct {
	// IconBaseURL is the one origin icon names resolve against and the only origin an icon URL
	// may come from. It feeds the Content-Security-Policy img-src, so a change needs a restart.
	IconBaseURL string `json:"iconBaseURL" dash:"url,restart" doc:"Where icon names resolve: an icon called proxmox is requested from <iconBaseURL>/proxmox.png, and an icon URL is only used when it lies under this URL. An http(s) URL with a host, without query or fragment, e.g. https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png. Empty requests no icon from any third party: an icon name shows a letter tile."`
}

// Validate checks that a set IconBaseURL is an http(s) URL with a host and neither query nor
// fragment, which would break the <base>/<name>.png form.
func (u UI) Validate() error {
	var p problems
	if s := u.IconBaseURL; s != "" {
		parsed, err := url.Parse(s)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || strings.ContainsAny(s, "?#") {
			p.add("iconBaseURL", "%q must be an http(s) URL with a host and without query or fragment, e.g. https://cdn.example.com/icons", s)
		}
	}
	return p.err()
}
