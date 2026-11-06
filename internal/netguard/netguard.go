// Package netguard builds HTTP clients for addresses that someone other than the operator
// chose: an OAuth client ID metadata document (design M.6) and, later, a Web Push endpoint. The
// guard runs at dial time, after name resolution, so a host name that resolves into a refused
// range (or rebinds into one) is stopped too. The clients use no proxy from the environment
// (the guard would see the proxy's address), follow no redirect and read a bounded body.
package netguard

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrRefused is what every refusal of the dial guard matches (errors.Is), through the dial and
// client errors that wrap it.
var ErrRefused = errors.New("netguard: address refused")

// ErrRedirect is the error of a response that redirects: the clients follow none.
var ErrRedirect = errors.New("netguard: redirect refused")

// ErrTooLarge is the error of a body longer than the caller's bound.
var ErrTooLarge = errors.New("netguard: body too large")

// refused is a refusal naming the class of the address, never the address itself.
type refused string

func (e refused) Error() string        { return "netguard: " + string(e) + " address refused" }
func (e refused) Is(target error) bool { return target == ErrRefused }

// cgnat is the shared address space (RFC 6598): private to a carrier, so private here.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// special are the IANA special-purpose ranges (RFC 6890 and its updates) that are not global,
// besides the classes netip names itself: they reach nothing on the public internet, or embed
// an IPv4 address the guard cannot see (NAT64, 6to4, Teredo).
var special = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, and the limited broadcast
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments, Teredo included
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
	netip.MustParsePrefix("3fff::/20"),       // documentation
	netip.MustParsePrefix("5f00::/16"),       // segment routing SIDs
}

// global6 is the IPv6 global unicast space (RFC 4291 2.4): every other IPv6 address that is
// neither loopback nor private (handled before) is refused, the IPv4-compatible ::/96 and the
// IPv4-translated ::ffff:0:0:0/96 among them, which embed an IPv4 address the guard cannot see.
var global6 = netip.MustParsePrefix("2000::/3")

// Check reports why a connection to ip is refused, or nil. Only global unicast addresses pass.
// allowPrivate (auth.oauth.cimdAllowPrivate) also passes private (RFC 1918, CGNAT, ULA) and
// loopback addresses; link-local addresses, which hold the cloud metadata service, the
// unspecified address, multicast and the special ranges never pass.
func Check(ip netip.Addr, allowPrivate bool) error {
	ip = ip.Unmap()
	switch {
	case !ip.IsValid():
		return refused("invalid")
	case ip.IsUnspecified():
		return refused("unspecified")
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return refused("link-local")
	case ip.IsMulticast() || ip.IsInterfaceLocalMulticast():
		return refused("multicast")
	case ip.IsLoopback():
		if allowPrivate {
			return nil
		}
		return refused("loopback")
	case ip.IsPrivate() || cgnat.Contains(ip):
		if allowPrivate {
			return nil
		}
		return refused("private")
	}
	for _, p := range special {
		if p.Contains(ip) {
			return refused("special-purpose")
		}
	}
	if !ip.IsGlobalUnicast() || (ip.Is6() && !global6.Contains(ip)) {
		return refused("non-global")
	}
	return nil
}

// Options shape a guarded client.
type Options struct {
	// AllowPrivate also lets the client reach private and loopback addresses (Check).
	AllowPrivate bool
	// Timeout bounds the whole exchange, body included; zero means 5 seconds.
	Timeout time.Duration
	// RootCAs are the certificate authorities the client trusts; nil means the system's.
	RootCAs *x509.CertPool
}

// NewClient builds a client that dials only the addresses Check passes, uses no proxy, follows
// no redirect (ErrRedirect) and gives up after the timeout. Read a body with ReadBody.
func NewClient(o Options) *http.Client {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: cannot read the dialled address", ErrRefused)
		}
		return Check(ap.Addr(), o.AllowPrivate)
	}}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // never the environment's: the guard would check the proxy
			DialContext:           dialer.DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.RootCAs},
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			IdleConnTimeout:       30 * time.Second,
		},
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
}

// ReadBody reads at most limit bytes of r; a longer body is ErrTooLarge.
func ReadBody(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}
