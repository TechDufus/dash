package auth

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// Trust is a set of peer address prefixes whose identity headers are believed. The zero value
// trusts nothing.
type Trust struct {
	prefixes []netip.Prefix
}

// ParseTrust parses CIDRs and bare IPs (/32 or /128). Entries are trimmed; an empty or invalid
// entry is an error naming it. Prefixes are masked, and a v4-in-v6 prefix of 96 bits or more is
// stored as the v4 prefix, so Trusts only ever compares unmapped addresses.
func ParseTrust(entries []string) (Trust, error) {
	var t Trust
	for _, e := range entries {
		s := strings.TrimSpace(e)
		if s == "" {
			return Trust{}, fmt.Errorf("trusted proxy entry %q is empty", e)
		}
		var p netip.Prefix
		if strings.Contains(s, "/") {
			var err error
			if p, err = netip.ParsePrefix(s); err != nil {
				return Trust{}, fmt.Errorf("trusted proxy entry %q is not a CIDR or IP address", s)
			}
		} else {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return Trust{}, fmt.Errorf("trusted proxy entry %q is not a CIDR or IP address", s)
			}
			if a.Zone() != "" {
				return Trust{}, fmt.Errorf("trusted proxy entry %q must not carry a zone", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		t.prefixes = append(t.prefixes, p.Masked())
	}
	return t, nil
}

// Trusts reports whether a falls in any prefix. v4-in-v6 is unmapped and the zone dropped first.
func (t Trust) Trusts(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if !a.IsValid() {
		return false
	}
	for _, p := range t.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Wide reports whether any prefix has zero bits (0.0.0.0/0 or ::/0), i.e. trusts a whole family.
func (t Trust) Wide() bool {
	for _, p := range t.prefixes {
		if p.Bits() == 0 {
			return true
		}
	}
	return false
}

// Broad returns the prefixes that trust a large part of an address family: shorter than /8 for
// IPv4, shorter than /16 for IPv6 (the zero-bit prefixes included). The caller warns about them.
func (t Trust) Broad() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range t.prefixes {
		if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
			out = append(out, p)
		}
	}
	return out
}

// Len is the number of prefixes.
func (t Trust) Len() int { return len(t.prefixes) }

// PeerOf parses r.RemoteAddr (the TCP peer, the only trust input), unmapped and without zone.
// An unparsable peer reports false and is untrusted; no header is ever consulted.
func PeerOf(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap().WithZone(""), true
}
