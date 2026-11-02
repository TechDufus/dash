package auth

import (
	"slices"
	"testing"
)

func TestFixedNoneReportsTheAuthNoneWarning(t *testing.T) {
	a := Fixed(Principal{Kind: KindNone, User: "anonymous", Role: RoleAdmin})
	w, ok := a.(Warner)
	if !ok {
		t.Fatalf("%T is not a Warner", a)
	}
	if got := w.Warnings(); !slices.Equal(got, []string{"auth_none"}) {
		t.Errorf("warnings = %q, want [auth_none]", got)
	}
	got, f := a.Authenticate(request("192.0.2.1:1", "X-Forwarded-User", "mallory", "Authorization", "Bearer x"))
	if f != nil || got.Kind != KindNone || got.User != "anonymous" || got.Role != RoleAdmin || got.Via != "fixed" || !got.Interactive() {
		t.Errorf("principal = %+v %+v, want the interactive anonymous admin via fixed", got, f)
	}
}

// Dev and demo are fixed too but have nothing to warn about: only auth.mode=none is a Warner.
func TestFixedDevAndDemoHaveNoWarnings(t *testing.T) {
	for _, k := range []Kind{KindDev, KindDemo} {
		if _, ok := Fixed(Principal{Kind: k, User: string(k), Role: RoleAdmin}).(Warner); ok {
			t.Errorf("Fixed(%s) is a Warner", k)
		}
	}
}
