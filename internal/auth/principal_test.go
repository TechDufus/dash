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
