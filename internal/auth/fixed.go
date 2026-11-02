package auth

import "net/http"

// Fixed is the authenticator of the process modes that sign every request in as one identity
// (DASH_DEV, DASH_DEMO, auth.mode=none): it ignores identity headers, cookies and the peer. Via
// is "fixed" unless p sets it.
func Fixed(p Principal) Authenticator {
	if p.Via == "" {
		p.Via = "fixed"
	}
	return fixed{p: p}
}

type fixed struct{ p Principal }

func (f fixed) Authenticate(*http.Request) (Principal, *Failure) { return f.p, nil }
