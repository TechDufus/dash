package pluginconf

import (
	"net/url"
	"strings"
)

// UI is the top-level ui section: settings of the web UI that the server decides, because the
// browser is not trusted to.
type UI struct {
	// IconBaseURL is the one origin the icons of launcher links and of apps resolve against and the
	// only origin an absolute icon URL may come from. It is read once when dash starts, so a change
	// needs a restart.
	IconBaseURL string `json:"iconBaseURL" dash:"url,restart" doc:"Where icons resolve, for launcher links and for the apps on the Apps page: an icon called proxmox is requested from <iconBaseURL>/proxmox.png (one trailing .png, .svg or .webp on the name is dropped first), and an absolute icon URL is used only when it lies under this URL. Homepage-style names (mdi-, si- and sh- prefixes) show no icon. An http(s) URL with a host, without query or fragment, e.g. https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png. Empty (the default) shows letter tiles for launcher links and apps, so dash makes no third-party request for icons."`
}

// Validate checks that a set IconBaseURL is an http(s) URL with a host and neither query nor
// fragment, which would break the <base>/<name>.png form.
func (u UI) Validate() error {
	var p problems
	if s := u.IconBaseURL; s != "" {
		parsed, err := url.Parse(s)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || strings.ContainsAny(s, "?#") {
			// The message never repeats the value: a URL can carry a password.
			p.add("iconBaseURL", "must be an http(s) URL with a host and without query or fragment, e.g. https://cdn.example.com/icons")
		}
	}
	return p.err()
}
