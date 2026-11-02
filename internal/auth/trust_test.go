package auth

import (
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestTrustParse(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      []string
		want    []string // normalised prefixes
		wantErr string   // substring of the error, "" for success
	}{
		{name: "nil", in: nil, want: nil},
		{name: "cidr v4", in: []string{"127.0.0.1/32"}, want: []string{"127.0.0.1/32"}},
		{name: "cidr v6", in: []string{"::1/128"}, want: []string{"::1/128"}},
		{name: "bare v4", in: []string{"192.0.2.7"}, want: []string{"192.0.2.7/32"}},
		{name: "bare v6", in: []string{"2001:db8::1"}, want: []string{"2001:db8::1/128"}},
		{name: "masked", in: []string{"198.51.100.77/24"}, want: []string{"198.51.100.0/24"}},
		{name: "masked v6", in: []string{"2001:db8::1/32"}, want: []string{"2001:db8::/32"}},
		{name: "spaces", in: []string{"  203.0.113.0/24 ", "\t::1\t"}, want: []string{"203.0.113.0/24", "::1/128"}},
		{name: "several", in: []string{"127.0.0.1", "::1"}, want: []string{"127.0.0.1/32", "::1/128"}},
		{name: "v4-in-v6 prefix becomes v4", in: []string{"::ffff:192.0.2.0/120"}, want: []string{"192.0.2.0/24"}},
		{name: "v4-in-v6 host prefix", in: []string{"::ffff:127.0.0.1/128"}, want: []string{"127.0.0.1/32"}},
		{name: "v4-in-v6 bare", in: []string{"::ffff:127.0.0.1"}, want: []string{"127.0.0.1/32"}},
		{name: "v4-in-v6 exactly 96", in: []string{"::ffff:0.0.0.0/96"}, want: []string{"0.0.0.0/0"}},
		{name: "wide v4", in: []string{"0.0.0.0/0"}, want: []string{"0.0.0.0/0"}},
		{name: "wide v6", in: []string{"::/0"}, want: []string{"::/0"}},
		{name: "empty entry", in: []string{"127.0.0.1", ""}, wantErr: `""`},
		{name: "blank entry", in: []string{"  "}, wantErr: "empty"},
		{name: "garbage", in: []string{"not-an-ip"}, wantErr: `"not-an-ip"`},
		{name: "hostname", in: []string{"proxy.example.com"}, wantErr: `"proxy.example.com"`},
		{name: "bad bits", in: []string{"127.0.0.1/33"}, wantErr: `"127.0.0.1/33"`},
		{name: "bad mask text", in: []string{"127.0.0.1/abc"}, wantErr: `"127.0.0.1/abc"`},
		{name: "octet overflow", in: []string{"256.0.0.1"}, wantErr: `"256.0.0.1"`},
		{name: "zone in entry", in: []string{"fe80::1%eth0"}, wantErr: `"fe80::1%eth0"`},
		{name: "second entry garbage", in: []string{"127.0.0.1", "x"}, wantErr: `"x"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			tr, err := ParseTrust(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range tr.prefixes {
				got = append(got, p.String())
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("prefixes = %v, want %v", got, c.want)
			}
			if tr.Len() != len(c.want) {
				t.Errorf("Len = %d, want %d", tr.Len(), len(c.want))
			}
		})
	}
}

func TestTrustsBoundaries(t *testing.T) {
	parse := func(entries ...string) Trust {
		t.Helper()
		tr, err := ParseTrust(entries)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	for _, c := range []struct {
		name  string
		trust Trust
		addr  string
		want  bool
		wide  bool
		n     int
	}{
		{"zero trusts nothing v4", Trust{}, "127.0.0.1", false, false, 0},
		{"zero trusts nothing v6", Trust{}, "::1", false, false, 0},
		{"empty list trusts nothing", parse(), "127.0.0.1", false, false, 0},
		{"/32 trusts itself", parse("127.0.0.1/32"), "127.0.0.1", true, false, 1},
		{"/32 does not trust neighbour", parse("127.0.0.1/32"), "127.0.0.2", false, false, 1},
		{"/32 does not trust previous", parse("127.0.0.1/32"), "127.0.0.0", false, false, 1},
		{"v4-in-v6 peer unmapped and trusted", parse("127.0.0.1/32"), "::ffff:127.0.0.1", true, false, 1},
		{"v4-in-v6 peer, other host", parse("127.0.0.1/32"), "::ffff:127.0.0.2", false, false, 1},
		{"v4-in-v6 entry trusts plain v4 peer", parse("::ffff:127.0.0.1"), "127.0.0.1", true, false, 1},
		{"::1/128 trusts ::1", parse("::1/128"), "::1", true, false, 1},
		{"::1/128 not ::2", parse("::1/128"), "::2", false, false, 1},
		{"::1/128 does not trust v4 loopback", parse("::1/128"), "127.0.0.1", false, false, 1},
		{"v4 entry does not trust v6", parse("127.0.0.1/32"), "::1", false, false, 1},
		{"cidr v4 edge in", parse("192.0.2.0/24"), "192.0.2.255", true, false, 1},
		{"cidr v4 edge out", parse("192.0.2.0/24"), "192.0.3.0", false, false, 1},
		{"zoned peer against fe80::/10", parse("fe80::/10"), "fe80::1%eth0", true, false, 1},
		{"zoned peer outside prefix", parse("fe80::/10"), "2001:db8::1%eth0", false, false, 1},
		{"zoned peer exact entry", parse("fe80::1"), "fe80::1%eth0", true, false, 1},
		{"multiple entries second matches", parse("127.0.0.1", "198.51.100.0/24"), "198.51.100.9", true, false, 2},
		{"0.0.0.0/0 trusts all v4", parse("0.0.0.0/0"), "203.0.113.9", true, true, 1},
		{"0.0.0.0/0 trusts unmapped v4-in-v6", parse("0.0.0.0/0"), "::ffff:203.0.113.9", true, true, 1},
		{"0.0.0.0/0 does not trust v6", parse("0.0.0.0/0"), "2001:db8::1", false, true, 1},
		{"::/0 trusts all v6", parse("::/0"), "2001:db8::1", true, true, 1},
		{"::ffff:0.0.0.0/96 is wide", parse("::ffff:0.0.0.0/96"), "203.0.113.9", true, true, 1},
		{"wide among narrow", parse("127.0.0.1", "::/0"), "2001:db8::1", true, true, 2},
		{"/1 is not wide", parse("0.0.0.0/1"), "1.2.3.4", true, false, 1},
		{"invalid addr untrusted", parse("0.0.0.0/0", "::/0"), "", false, true, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			var a netip.Addr
			if c.addr != "" {
				a = netip.MustParseAddr(c.addr)
			}
			if got := c.trust.Trusts(a); got != c.want {
				t.Errorf("Trusts(%q) = %v, want %v", c.addr, got, c.want)
			}
			if got := c.trust.Wide(); got != c.wide {
				t.Errorf("Wide() = %v, want %v", got, c.wide)
			}
			if got := c.trust.Len(); got != c.n {
				t.Errorf("Len() = %d, want %d", got, c.n)
			}
		})
	}
}

func TestPeerOf(t *testing.T) {
	for _, c := range []struct {
		name   string
		remote string
		xff    string
		want   string
		ok     bool
	}{
		{name: "v4", remote: "192.0.2.1:1234", want: "192.0.2.1", ok: true},
		{name: "v6", remote: "[::1]:80", want: "::1", ok: true},
		{name: "v4-in-v6 unmapped", remote: "[::ffff:127.0.0.1]:80", want: "127.0.0.1", ok: true},
		{name: "zone dropped", remote: "[fe80::1%eth0]:80", want: "fe80::1", ok: true},
		{name: "empty", remote: "", ok: false},
		{name: "hostname", remote: "localhost:80", ok: false},
		{name: "missing port", remote: "127.0.0.1", ok: false},
		{name: "bare v6 no port", remote: "::1", ok: false},
		{name: "garbage", remote: "@", ok: false},
		{name: "xff ignored", remote: "192.0.2.1:1234", xff: "127.0.0.1", want: "192.0.2.1", ok: true},
		{name: "xff cannot rescue unparsable peer", remote: "", xff: "127.0.0.1", ok: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: c.remote, Header: http.Header{}}
			if c.xff != "" {
				r.Header.Set("X-Forwarded-For", c.xff)
			}
			got, ok := PeerOf(r)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (addr %v)", ok, c.ok, got)
			}
			if !ok {
				if got.IsValid() {
					t.Errorf("addr = %v for an unparsable peer, want zero", got)
				}
				return
			}
			if got.String() != c.want {
				t.Errorf("addr = %v, want %v", got, c.want)
			}
			if got.Zone() != "" || got.Is4In6() {
				t.Errorf("addr %v keeps zone or v4-in-v6 form", got)
			}
		})
	}
}

// An unparsable peer is untrusted even by a /0 set: the zero Addr never matches.
func TestUnparsablePeerUntrusted(t *testing.T) {
	tr, err := ParseTrust([]string{"0.0.0.0/0", "::/0"})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := PeerOf(&http.Request{RemoteAddr: "localhost:80"})
	if ok || tr.Trusts(a) {
		t.Errorf("ok=%v trusts=%v, want an untrusted peer", ok, tr.Trusts(a))
	}
}
