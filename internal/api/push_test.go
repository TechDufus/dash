package api

import (
	"testing"

	"github.com/TechDufus/dash/internal/auth"
)

// capabilities.push says whether the caller may register push devices (contract D37, D45): push
// is enabled, and the caller is a named person or a token with the push scope; never the
// anonymous viewer, and never a token without the scope.
func TestMeCapabilitiesPush(t *testing.T) {
	f := newTokenFixture(t)
	withPush, _ := f.pat(t, "bob", "api", []string{"read", "push"})
	readOnly, _ := f.pat(t, "bob", "api", []string{"read"})
	anonymous := auth.Fixed(auth.Principal{Kind: auth.KindAnonymous, User: "anonymous", Role: auth.RoleViewer})
	for _, tc := range []struct {
		name    string
		enabled bool
		authn   auth.Authenticator
		header  map[string]string
		want    bool
	}{
		{"a person", true, nil, nil, true},
		{"push off", false, nil, nil, false},
		{"a token with push", true, nil, bearer(withPush), true},
		{"a token without push", true, nil, bearer(readOnly), false},
		{"the anonymous viewer", true, anonymous, nil, false},
	} {
		s := newServer(func(d *Deps) {
			f.deps(d)
			d.PushEnabled = tc.enabled
			if tc.authn != nil {
				d.Auth = tc.authn
			}
		})
		if got := meOf(t, req{path: "/api/v1/me", header: tc.header}.do(s.Handler())).Capabilities.Push; got != tc.want {
			t.Errorf("%s: capabilities.push = %v, want %v", tc.name, got, tc.want)
		}
	}
}
