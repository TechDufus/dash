package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrAmbiguousIdentity is returned when an identity header arrives as more than one header line.
// A proxy that appends instead of replacing would put the client's own value first, so the
// request is refused rather than guessing which line is trustworthy.
var ErrAmbiguousIdentity = errors.New("identity header carries more than one value")

const (
	maxUserBytes    = 128
	maxGroupBytes   = 256
	maxGroupEntries = 64
)

// ProxyIdentity names the headers a trusted identity proxy sets. The caller must only call From
// for a peer it trusts (see Trust).
type ProxyIdentity struct {
	// UserHeaders are consulted in order; the first with a non-empty value is the identity.
	UserHeaders []string
	// GroupsHeader carries a comma-separated group list; empty turns groups off.
	GroupsHeader string
}

// Validate checks the configured header names: HTTP tokens, none of Authorization, Cookie, Host
// or X-Dash-Csrf (case-insensitive), at least one user header. GroupsHeader may be empty.
func (p ProxyIdentity) Validate() error {
	if len(p.UserHeaders) == 0 {
		return errors.New("at least one user header is required")
	}
	for _, n := range p.UserHeaders {
		if err := validHeaderName(n); err != nil {
			return fmt.Errorf("user header %q: %w", n, err)
		}
	}
	if p.GroupsHeader != "" {
		if err := validHeaderName(p.GroupsHeader); err != nil {
			return fmt.Errorf("groups header %q: %w", p.GroupsHeader, err)
		}
	}
	return nil
}

func validHeaderName(n string) error {
	if n == "" {
		return errors.New("name is empty")
	}
	for i := range len(n) {
		if !isTokenChar(n[i]) {
			return errors.New("is not a valid HTTP header name")
		}
	}
	for _, f := range []string{"Authorization", "Cookie", "Host", "X-Dash-Csrf"} {
		if strings.EqualFold(n, f) {
			return fmt.Errorf("may not be %s", f)
		}
	}
	return nil
}

// isTokenChar reports whether c is an RFC 9110 tchar.
func isTokenChar(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

// From extracts the identity from h. UserHeaders are tried in order; a header that is absent or
// whose value is empty after trimming is skipped, the first non-empty one is THE identity and
// later headers are not consulted. If that header has more than one line, From returns
// ErrAmbiguousIdentity. A value that is over 128 bytes, not valid UTF-8 or holds a control
// character yields no identity (empty user, nil error), without falling through to the next
// header. via is the canonical name of the header that supplied the user. Groups are read only
// when an identity exists and GroupsHeader is set.
func (p ProxyIdentity) From(h http.Header) (user string, groups []string, via string, err error) {
	for _, name := range p.UserHeaders {
		name = http.CanonicalHeaderKey(name)
		lines := h.Values(name)
		present := false
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		if len(lines) > 1 {
			return "", nil, "", ErrAmbiguousIdentity
		}
		v := strings.TrimSpace(lines[0])
		if !cleanValue(v, maxUserBytes) {
			return "", nil, "", nil
		}
		groups, err = p.groups(h)
		if err != nil {
			return "", nil, "", err
		}
		return v, groups, name, nil
	}
	return "", nil, "", nil
}

// groups parses the groups header. More than one header line is refused as ambiguous: a
// deliberate fail-closed choice, since a client-supplied first line could otherwise grant
// admin-group membership.
func (p ProxyIdentity) groups(h http.Header) ([]string, error) {
	if p.GroupsHeader == "" {
		return nil, nil
	}
	lines := h.Values(http.CanonicalHeaderKey(p.GroupsHeader))
	if len(lines) > 1 {
		return nil, ErrAmbiguousIdentity
	}
	if len(lines) == 0 {
		return nil, nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, g := range strings.Split(lines[0], ",") {
		g = strings.TrimSpace(g)
		if g == "" || !cleanValue(g, maxGroupBytes) {
			continue
		}
		if _, dup := seen[g]; dup {
			continue
		}
		if len(out) == maxGroupEntries {
			break
		}
		seen[g] = struct{}{}
		out = append(out, g)
	}
	return out, nil
}

// cleanValue reports whether v is at most limit bytes, valid UTF-8 and free of control
// characters (including DEL and the C1 range).
func cleanValue(v string, limit int) bool {
	if len(v) > limit || !utf8.ValidString(v) {
		return false
	}
	return !strings.ContainsFunc(v, unicode.IsControl)
}
