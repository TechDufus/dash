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

		// Paths that pass the raw checks but that a browser normalizes (dot segments removed,
		// encoded dots too) into the protocol-relative "//evil.example/x".
		{"/.//evil.example/x", false},
		{"/%2e//evil.example/x", false},
		{"/%2E//evil.example/x", false},
		{"/a/..//evil.example/x", false},
		{"/%2E%2E//evil.example/x", false},
		{"/a/%2e%2e//evil.example/x", false},
		{"/./%09/evil.example", false}, // an encoded tab, dropped by a browser
		{"/%5C/evil.example", false},   // an encoded backslash, read as "/"
		// Dot segments that stay on the origin are fine.
		{"/./alerts", true},
		{"/a/../alerts", true},
		{"/%2F%2Fevil", true}, // an encoded slash is part of one segment, never a host
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
