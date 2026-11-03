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
