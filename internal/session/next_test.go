package session_test

import (
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/session"
)

func TestNextIsValidatedAgainstOpenRedirects(t *testing.T) {
	for _, tc := range []struct {
		next string
		ok   bool
	}{
		{"/", true},
		{"/alerts", true},
		{"/plugins/kubernetes?ns=default&tab=pods#row-3", true},
		{"/settings/audit/", true},
		{"/a/b%2Fc", true},
		{"/" + strings.Repeat("a", 1023), true},

		{"//evil", false},
		{"//evil.example/path", false},
		{"/\\evil", false},
		{"\\\\evil", false},
		{"/ok\\..\\evil", false},
		{"https://evil", false},
		{"http://evil.example/", false},
		{"HTTPS://evil", false},
		{"javascript:alert(1)", false},
		{"JaVaScRiPt:alert(1)", false},
		{"data:text/html,<script>", false},
		{"evil", false},
		{"", false},
		{" /alerts", false},
		{"/\t/evil", false},
		{"/\n/evil", false},
		{"/\r\nLocation: https://evil", false},
		{"/a\x00b", false},
		{"/a\x7fb", false},
		{"/a\u0085b", false}, // C1 control
		{"/a\xffb", false},   // invalid UTF-8
		{"/" + strings.Repeat("a", 1024), false},
	} {
		if got := session.ValidNext(tc.next); got != tc.ok {
			t.Errorf("ValidNext(%q) = %v, want %v", tc.next, got, tc.ok)
		}
		want := "/"
		if tc.ok {
			want = tc.next
		}
		if got := session.SafeNext(tc.next); got != want {
			t.Errorf("SafeNext(%q) = %q, want %q", tc.next, got, want)
		}
	}
}
