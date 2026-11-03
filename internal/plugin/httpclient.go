package plugin

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// DefaultHTTPTimeout is HTTPConfig.Timeout when it is zero: the poll timeout dash has always
// used.
const DefaultHTTPTimeout = 25 * time.Second

// HTTPConfig is how a plugin wants its upstream client built (design A.3).
type HTTPConfig struct {
	Timeout            time.Duration       // whole-request cap; 0 = DefaultHTTPTimeout
	CAFile             string              // extra root CAs (PEM), added to the system roots
	InsecureSkipVerify bool                // logs a startup WARN (F.6, K.11)
	Authorize          func(*http.Request) // sets credentials per request, so a rotated secret applies at the next request
	FromUI             bool                // the URL came from the UI layer: loopback dials are refused too (A.3, K.22)
}

// metadataAddr is the IPv6 address of the AWS instance metadata service.
var metadataAddr = netip.MustParseAddr("fd00:ec2::254")

// ErrBlockedAddress is what every refusal of the dial guard matches (errors.Is), through the
// dial and redirect errors that wrap it, so that a connection check can say "blocked address"
// without reading the error's text.
var ErrBlockedAddress = errors.New("blocked address")

// blockedError is a refusal of the dial guard: Error() is the text the logs have always shown,
// and errors.Is(err, ErrBlockedAddress) holds.
type blockedError string

func (e blockedError) Error() string        { return string(e) }
func (e blockedError) Is(target error) bool { return target == ErrBlockedAddress }

// guardAddr is the dial guard: it inspects the address a connection is about to be made to,
// after name resolution, so a host name that resolves into a refused range is stopped too. It
// always refuses link-local addresses (169.254.0.0/16 and fe80::/10, which include the cloud
// metadata service) and fd00:ec2::254; with fromUI it also refuses loopback and the
// unspecified address (which connects to the local host), so that a URL saved from the UI
// cannot reach dash's own listeners or its node. Every refusal is ErrBlockedAddress.
func guardAddr(ip netip.Addr, fromUI bool) error {
	ip = ip.Unmap()
	switch {
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip == metadataAddr:
		return blockedError("address is link-local or the metadata service: refused")
	case fromUI && (ip.IsLoopback() || ip.IsUnspecified()):
		return blockedError("loopback address: refused for a URL set from the UI")
	}
	return nil
}

func guardControl(fromUI bool) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("dial guard: cannot read address: %w", err)
		}
		return guardAddr(ap.Addr(), fromUI)
	}
}

// newHTTPClient builds the guarded client. It sends no request through an HTTP proxy from the
// environment: the guard sees only the address it dials, which would then be the proxy's.
func newHTTPClient(c HTTPConfig) (*http.Client, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureSkipVerify} //nolint:gosec // an explicit, logged operator choice (F.6)
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA file %s holds no PEM certificate", c.CAFile)
		}
		tlsCfg.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: guardControl(c.FromUI)}
	var rt http.RoundTripper = &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
	}
	if c.Authorize != nil {
		rt = authorizing{rt: rt, authorize: c.Authorize}
	}
	return &http.Client{
		Transport: rt,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return errors.New("redirect to another host refused")
			}
			// The credentials are added to every hop, so a hop from https to plain http would send
			// them in clear text.
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("redirect from https to http refused")
			}
			if ip, err := netip.ParseAddr(req.URL.Hostname()); err == nil {
				return guardAddr(ip, c.FromUI)
			}
			return nil
		},
	}, nil
}

// authorizing applies Authorize to a copy of every request, redirects included.
type authorizing struct {
	rt        http.RoundTripper
	authorize func(*http.Request)
}

func (a authorizing) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	a.authorize(req)
	return a.rt.RoundTrip(req)
}
