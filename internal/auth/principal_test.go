package auth

import "testing"

func TestPrincipalInteractive(t *testing.T) {
	for _, c := range []struct {
		kind Kind
		want bool
	}{
		{KindProxy, true},
		{KindSession, true},
		{KindDev, true},
		{KindDemo, true},
		{KindNone, true},
		{KindToken, false},
		{KindAnonymous, false},
		{"", false},
		{"bogus", false},
	} {
		if got := (Principal{Kind: c.kind}).Interactive(); got != c.want {
			t.Errorf("Principal{Kind: %q}.Interactive() = %v, want %v", c.kind, got, c.want)
		}
	}
}

// A token's actionNamespaces are exact names or prefix* globs: an exact entry is not a prefix, a
// glob matches from the start of the name only, an empty list admits everything, and a non-empty
// list refuses a target without a namespace unless it holds "*".
func TestActionNamespaceAllowed(t *testing.T) {
	tok := func(ns ...string) Principal { return Principal{Kind: KindToken, ActionNamespaces: ns} }
	for _, c := range []struct {
		p    Principal
		ns   string
		want bool
	}{
		{tok("media"), "media", true},
		{tok("media"), "media-prod", false},
		{tok("media"), "xmedia", false},
		{tok("apps-*"), "apps-web", true},
		{tok("apps-*"), "apps-", true},
		{tok("apps-*"), "my-apps-x", false},
		{tok("apps-*"), "apps", false},
		{tok(), "anything", true},
		{tok(), "", true},
		{tok("media"), "", false},
		{tok("apps-*"), "", false},
		{tok("media", "*"), "", true},
		{tok("*"), "kube-system", true},
		{Principal{Kind: KindProxy}, "", true},
	} {
		if got := c.p.ActionNamespaceAllowed(c.ns); got != c.want {
			t.Errorf("%v admits %q = %v, want %v", c.p.ActionNamespaces, c.ns, got, c.want)
		}
	}
}

func TestKindValues(t *testing.T) {
	for k, want := range map[Kind]string{
		KindProxy: "proxy", KindSession: "session", KindToken: "token", KindAnonymous: "anonymous",
		KindDev: "dev", KindDemo: "demo", KindNone: "none",
	} {
		if string(k) != want {
			t.Errorf("kind %q, want %q", k, want)
		}
	}
}
