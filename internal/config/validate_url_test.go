package config

import (
	"strings"
	"testing"
)

// An invalid URL is reported at its path with fixed text: the message never repeats the value,
// which can carry a password in its userinfo, and so neither do dash config validate, the
// startup error or the log.
func TestInvalidURLErrorNeverRepeatsTheURL(t *testing.T) {
	const pw = "URLPASSWORD"
	for _, tc := range []struct {
		path string
		set  func(*Tree, string)
		url  string
	}{
		{"plugins.proxmox.url", func(t *Tree, u string) { t.Plugins.Proxmox.URL = u }, "ftp://admin:" + pw + "@pve.example.com:8006"},
		{"plugins.prometheus.url", func(t *Tree, u string) { t.Plugins.Prometheus.URL = u }, "http://admin:" + pw + "@"},
		{"plugins.gatus.url", func(t *Tree, u string) { t.Plugins.Gatus.URL = u }, "http://admin:" + pw + "@[::1"},
		{"ui.iconBaseURL", func(t *Tree, u string) { t.UI.IconBaseURL = u }, "ftp://admin:" + pw + "@cdn.example.com/png"},
		{"ui.iconBaseURL", func(t *Tree, u string) { t.UI.IconBaseURL = u }, "https://admin:" + pw + "@cdn.example.com/png?x=1"},
	} {
		tree := DefaultTree()
		tc.set(&tree, tc.url)
		err := validate(tree, false, true, nil)
		if err == nil {
			t.Errorf("%s = %q: no error", tc.path, tc.url)
			continue
		}
		if strings.Contains(err.Error(), pw) {
			t.Errorf("%s: the error repeats the URL: %v", tc.path, err)
		}
		found := false
		for _, fe := range FieldErrors(err) {
			if strings.Contains(fe.Path, pw) || strings.Contains(fe.Message, pw) {
				t.Errorf("%s: a FieldError repeats the URL: %+v", tc.path, fe)
			}
			found = found || fe.Path == tc.path
		}
		if !found {
			t.Errorf("%s: no FieldError at the path in %q", tc.path, FieldErrors(err))
		}
	}
}

// An invalid server.allowedHosts entry is named by its position, never by its text: a URL pasted
// there can carry a password, which config validate, the startup error and the log would show.
// The position counts the entries after the split at commas, without the empty ones.
func TestInvalidAllowedHostIsNamedByItsPosition(t *testing.T) {
	const pw = "HOSTPASSWORD"
	for _, tc := range []struct {
		hosts, want string
	}{
		{`["ok.example.com", "https://admin:` + pw + `@dash.example.com"]`, "entry 2 is not a host name or IP address (no scheme or port)"},
		{`["admin:` + pw + `@dash.example.com:443"]`, "entry 1 is not a host name or IP address (no scheme or port)"},
		{`["", "ok.example.com", "a/` + pw + `"]`, "entry 2 is not a host name or IP address (no scheme or port)"},          // the empty entry is dropped
		{`["a.example.com,b.example.com", "a/` + pw + `"]`, "entry 3 is not a host name or IP address (no scheme or port)"}, // split at the comma
	} {
		_, err := load(map[string]string{"DASH_SERVER_PUBLIC_URL": "https://d", "DASH_SERVER_ALLOWED_HOSTS": tc.hosts})
		if err == nil {
			t.Errorf("%s: no error", tc.hosts)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(pw)) {
			t.Errorf("%s: the error repeats the entry: %v", tc.hosts, err)
		}
		found := false
		for _, fe := range FieldErrors(err) {
			found = found || (fe.Path == "server.allowedHosts" && fe.Message == tc.want)
		}
		if !found {
			t.Errorf("%s: field errors %+v, want server.allowedHosts: %q", tc.hosts, FieldErrors(err), tc.want)
		}
	}
}
