package pluginconf

import (
	"net/url"
	"strings"
)

// UI is the top-level ui section: settings of the web UI that the server decides, because the
// browser is not trusted to.
type UI struct {
	// IconBaseURL is the one origin the icons of launcher links resolve against and the only
	// origin an absolute launcher icon URL may come from. In this release it covers the launcher
	// only: the icons of the apps on the Apps page still come from the CDN URL the server
	// builds. It is read once when dash starts, so a change needs a restart.
	IconBaseURL string `json:"iconBaseURL" dash:"url,restart" doc:"Where the icons of launcher links resolve: a launcher icon called proxmox is requested from <iconBaseURL>/proxmox.png, and an absolute icon URL on a launcher link is used only when it lies under this URL. App icons on the Apps page are not affected in this release: they still come from the server-built CDN URL until a later release. An http(s) URL with a host, without query or fragment, e.g. https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png. Empty (the default) shows letter tiles for launcher links, so the launcher makes no third-party request."`
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
