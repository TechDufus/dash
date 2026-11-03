package auth

import "net/http"

// Fixed is the authenticator of the process modes that sign every request in as one identity
// (DASH_DEV, DASH_DEMO, auth.mode=none): it ignores identity headers, cookies and the peer. Via
// is "fixed" unless p sets it. For KindNone it also implements Warner, reporting auth_none, so
// the API can tell the SPA to show its banner.
func Fixed(p Principal) Authenticator {
	if p.Via == "" {
		p.Via = "fixed"
	}
	if p.Kind == KindNone {
		return fixedNone{fixed{p: p}}
	}
	return fixed{p: p}
}

type fixed struct{ p Principal }

func (f fixed) Authenticate(*http.Request) (Principal, *Failure) { return f.p, nil }

// fixedNone is the fixed identity of auth.mode=none: an administrator that nobody authenticated.
type fixedNone struct{ fixed }

// Warnings implements Warner.
func (fixedNone) Warnings() []string { return []string{WarningAuthNone} }

var (
	_ Warner = fixedNone{}
	_ Kinder = fixedNone{}
	_ Kinder = fixed{}
)

// Kind implements Kinder.
func (f fixed) Kind() Kind { return f.p.Kind }
