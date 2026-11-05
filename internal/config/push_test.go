package config

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// push.contact defaults to an https public URL and is otherwise empty (push stays off); a set
// contact is mailto:<address> or an https URL, and a refusal never repeats the value.
func TestPushContact(t *testing.T) {
	for _, tc := range []struct{ public, contact, want string }{
		{"https://dash.example.com", "", "https://dash.example.com"},
		{"http://127.0.0.1:8080", "", ""},
		{"http://127.0.0.1:8080", "mailto:ops@example.com", "mailto:ops@example.com"},
		{"https://dash.example.com", "https://contact.example.com/push", "https://contact.example.com/push"},
	} {
		c := mustLoad(t, map[string]string{"DASH_SERVER_PUBLIC_URL": tc.public, "DASH_PUSH_CONTACT": tc.contact})
		if c.PushContact != tc.want {
			t.Errorf("public %s, contact %q: PushContact = %q, want %q", tc.public, tc.contact, c.PushContact, tc.want)
		}
	}
	for _, bad := range []string{
		"mailto:", "mailto:secret-name", "mailto:a@example.com,b@example.com", "mailto:Secret <a@example.com>",
		"mailto:a@example.com?subject=x", "http://secret.example.com", "https://", "https://u:p@secret.example.com", "secret@example.com",
	} {
		_, err := load(map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com", "DASH_PUSH_CONTACT": bad})
		if err == nil || !strings.Contains(err.Error(), "push.contact") {
			t.Errorf("%q: %v, want a push.contact error", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error repeats the value: %v", bad, err)
		}
	}
}

// push.allowPrivate takes host names, addresses and CIDRs; anything else names the entry.
func TestPushAllowPrivate(t *testing.T) {
	c := mustLoad(t, map[string]string{
		"DASH_SERVER_PUBLIC_URL":  "https://dash.example.com",
		"DASH_PUSH_ALLOW_PRIVATE": `[" NTFY.example.com ", "192.0.2.7", "10.1.2.3/8", "fd00::/8", ""]`,
	})
	if !slices.Equal(c.PushAllow.Hosts, []string{"ntfy.example.com"}) {
		t.Errorf("hosts = %q", c.PushAllow.Hosts)
	}
	want := []netip.Prefix{netip.MustParsePrefix("192.0.2.7/32"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	if !slices.Equal(c.PushAllow.Prefixes, want) {
		t.Errorf("prefixes = %v, want %v", c.PushAllow.Prefixes, want)
	}
	if !Defaults().PushAllow.Empty() {
		t.Error("the default allows something")
	}
	for _, bad := range []string{`["https://ntfy.example.com"]`, `["ntfy.example.com:443"]`, `["10.0.0.0/33"]`, `["*"]`} {
		_, err := load(map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com", "DASH_PUSH_ALLOW_PRIVATE": bad})
		if err == nil || !strings.Contains(err.Error(), "push.allowPrivate") || !strings.Contains(err.Error(), "entry 1") {
			t.Errorf("%s: %v, want a push.allowPrivate error naming entry 1", bad, err)
		}
	}
}
