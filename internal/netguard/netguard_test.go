package netguard

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
)

// Only global unicast addresses pass; allowPrivate adds private and loopback ones, never
// link-local (the metadata service), unspecified, multicast or special-purpose ranges.
func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		addr          string
		global, allow bool // passes without and with allowPrivate
	}{
		{"8.8.8.8", true, true},
		{"2606:4700::1111", true, true},
		{"::ffff:8.8.8.8", true, true},
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"::ffff:127.0.0.1", false, true},
		{"::ffff:100.64.0.1", false, true}, // CGNAT, judged as the IPv4 address it maps
		{"::ffff:198.18.0.1", false, false},
		{"10.1.2.3", false, true},
		{"172.16.0.1", false, true},
		{"192.168.1.1", false, true},
		{"100.64.0.1", false, true},
		{"fd00::1", false, true},
		{"fd00:ec2::254", false, true},
		{"169.254.169.254", false, false},
		{"fe80::1", false, false},
		{"0.0.0.0", false, false},
		{"::", false, false},
		{"0.1.2.3", false, false},
		{"224.0.0.1", false, false},
		{"ff02::1", false, false},
		{"255.255.255.255", false, false},
		{"192.0.2.1", false, false},
		{"198.18.0.1", false, false},
		{"2001:db8::1", false, false},
		{"64:ff9b::a00:1", false, false},
		{"2002:a00:1::1", false, false},
		{"2001:0:a00:1::1", false, false},
		// IPv6 passes only inside 2000::/3: the IPv4-compatible ::/96 and IPv4-translated
		// ::ffff:0:0:0/96 embed an IPv4 address, and the rest is unassigned.
		{"::a00:1", false, false},
		{"::808:808", false, false},
		{"::ffff:0:a00:1", false, false},
		{"::ffff:0:808:808", false, false},
		{"4000::1", false, false},
		{"1000::1", false, false},
	} {
		ip := netip.MustParseAddr(tc.addr)
		if got := Check(ip, false) == nil; got != tc.global {
			t.Errorf("%s: passes %v, want %v", tc.addr, got, tc.global)
		}
		if got := Check(ip, true) == nil; got != tc.allow {
			t.Errorf("%s with allowPrivate: passes %v, want %v", tc.addr, got, tc.allow)
		}
		if err := Check(ip, false); err != nil && (!errors.Is(err, ErrRefused) || strings.Contains(err.Error(), tc.addr)) {
			t.Errorf("%s: error %v is not ErrRefused or repeats the address", tc.addr, err)
		}
	}
}

// The client refuses a loopback server at dial time, reaching nothing; with allowPrivate it
// gets through.
func TestClientRefusesAtDialTime(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	_, err := NewClient(Options{}).Get(srv.URL)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("loopback: %v, want ErrRefused", err)
	}
	// A host name resolving to loopback is refused as well: the guard sees the dialled address.
	_, err = NewClient(Options{}).Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("localhost: %v, want ErrRefused", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server saw %d requests", n)
	}
	resp, err := NewClient(Options{AllowPrivate: true}).Get(srv.URL)
	if err != nil {
		t.Fatalf("allowPrivate: %v", err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 {
		t.Error("allowPrivate did not reach the server")
	}
}

// PrivateHosts admit one host name at private and loopback addresses, PrivatePrefixes the
// private and loopback addresses inside them whatever the name; anything else stays refused,
// and link-local addresses never pass. RootCAs is the trust a TLS client uses.
func TestClientPrivateHostsAndPrefixes(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()
	viaName := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)
	for _, tc := range []struct {
		name string
		o    Options
		url  string
		ok   bool
	}{
		{"nothing allowed", Options{}, srv.URL, false},
		{"the host by name", Options{PrivateHosts: []string{"localhost"}}, viaName, true},
		{"another host", Options{PrivateHosts: []string{"ntfy.example.com"}}, viaName, false},
		{"the host by name, dialled by address", Options{PrivateHosts: []string{"localhost"}}, srv.URL, false},
		{"a prefix holding it", Options{PrivatePrefixes: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}, viaName, true},
		{"another prefix", Options{PrivatePrefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}, srv.URL, false},
	} {
		before := hits.Load()
		resp, err := NewClient(tc.o).Get(tc.url)
		if err == nil {
			_ = resp.Body.Close()
		}
		if got := err == nil && hits.Load() == before+1; got != tc.ok {
			t.Errorf("%s: reached %v (%v), want %v", tc.name, got, err, tc.ok)
		}
		if !tc.ok && !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", tc.name, err)
		}
	}
	// RootCAs is the trust of a TLS client; without it the system's applies, which does not hold
	// the test server's CA.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsSrv.Close()
	pool := tlsSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	if resp, err := NewClient(Options{AllowPrivate: true, RootCAs: pool}).Get(tlsSrv.URL); err != nil {
		t.Errorf("with the test CA: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	if _, err := NewClient(Options{AllowPrivate: true}).Get(tlsSrv.URL); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("without the test CA: %v, want a TLS error", err)
	}
	o := Options{PrivatePrefixes: []netip.Prefix{netip.MustParsePrefix("169.254.0.0/16")}, PrivateHosts: []string{"metadata"}}
	if o.allowed(netip.MustParseAddr("169.254.169.254"), true) == nil {
		t.Error("a link-local address passed")
	}
}

// An IPv6 unique local prefix (RFC 4193) in PrivatePrefixes admits the addresses inside it;
// another ULA prefix and IPv6 link-local addresses stay refused.
func TestPrivatePrefixesIPv6ULA(t *testing.T) {
	o := Options{PrivatePrefixes: []netip.Prefix{netip.MustParsePrefix("fd12:3456:789a::/48")}}
	for _, tc := range []struct {
		ip string
		ok bool
	}{
		{"fd12:3456:789a::10", true},
		{"fd12:3456:789a:1::1", true},
		{"fd12:3456:789b::10", false},
		{"fe80::1", false},
	} {
		err := o.allowed(netip.MustParseAddr(tc.ip), false)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v, want allowed %v", tc.ip, err, tc.ok)
		}
		if !tc.ok && !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", tc.ip, err)
		}
	}
}

// No redirect is followed, even to the same host.
func TestClientFollowsNoRedirect(t *testing.T) {
	var target atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			target.Add(1)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()
	_, err := NewClient(Options{AllowPrivate: true}).Get(srv.URL + "/start")
	if !errors.Is(err, ErrRedirect) || target.Load() != 0 {
		t.Errorf("redirect: %v, target hit %d times; want ErrRedirect and no hit", err, target.Load())
	}
}

// A proxy from the environment is never used: the guard would only see the proxy's address.
// Go never proxies a loopback target, so the target is a name that cannot resolve (RFC 6761
// .invalid): with the proxy the request would reach it, without it the lookup fails. net/http
// reads the proxy variables once per process, so the request runs in a child test process that
// starts with them set.
func TestClientIgnoresTheEnvironmentProxy(t *testing.T) {
	if os.Getenv("NETGUARD_PROXY_CHILD") == "1" {
		resp, err := NewClient(Options{AllowPrivate: true}).Get("http://netguard-test.invalid/")
		if err == nil {
			_ = resp.Body.Close()
			t.Fatal("the request got an answer")
		}
		return
	}
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestClientIgnoresTheEnvironmentProxy$", "-test.count=1")
	cmd.Env = append(os.Environ(), "NETGUARD_PROXY_CHILD=1", "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "http_proxy="+proxy.URL, "https_proxy="+proxy.URL, "NO_PROXY=", "no_proxy=")
	out, err := cmd.CombinedOutput()
	if err != nil || proxied.Load() != 0 {
		t.Errorf("child: %v, proxy hits %d; want a failed direct lookup and no proxy:\n%s", err, proxied.Load(), out)
	}
}

func TestReadBodyBound(t *testing.T) {
	if b, err := ReadBody(strings.NewReader(strings.Repeat("a", 64)), 64); err != nil || len(b) != 64 {
		t.Errorf("at the bound: %d bytes, %v", len(b), err)
	}
	if _, err := ReadBody(strings.NewReader(strings.Repeat("a", 65)), 64); !errors.Is(err, ErrTooLarge) {
		t.Errorf("past the bound: %v, want ErrTooLarge", err)
	}
}
