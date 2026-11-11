package pluginconf

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testHostKey is a host key in authorized_keys form, from a fixed seed.
var testHostKey = func() string {
	pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
}()

func TestSSHDefaultsAreValid(t *testing.T) {
	d := DefaultSSH()
	if err := d.Validate(); err != nil || d.IntervalSeconds != 5 || len(d.Hosts) != 0 || d.Enabled != nil {
		t.Fatalf("defaults %+v: %v", d, err)
	}
}

// An empty name is the host part of the address; whitespace is trimmed; the section itself is
// not changed.
func TestSSHNormalized(t *testing.T) {
	in := SSH{Hosts: []SSHHost{
		{Address: " nas.example.com:2222 ", User: " alice "},
		{Address: "[2001:db8::1]:22", User: "bob"},
		{Address: "[2001:db8::2]", User: "bob"},
		{Name: " Pi ", Address: "192.0.2.7", User: "bob", HostKey: " " + testHostKey + " "},
	}}
	got := in.Normalized().Hosts
	want := []SSHHost{
		{Name: "nas.example.com", Address: "nas.example.com:2222", User: "alice"},
		{Name: "2001:db8::1", Address: "[2001:db8::1]:22", User: "bob"},
		{Name: "2001:db8::2", Address: "[2001:db8::2]", User: "bob"},
		{Name: "Pi", Address: "192.0.2.7", User: "bob", HostKey: testHostKey},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("host %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if in.Hosts[0].Name != "" {
		t.Error("Normalized changed the section")
	}
}

func TestSSHAddresses(t *testing.T) {
	for addr, want := range map[string]struct {
		host string
		port int
	}{
		"nas":                {"nas", 22},
		"nas.example.com:22": {"nas.example.com", 22},
		"192.0.2.1:2222":     {"192.0.2.1", 2222},
		"[2001:db8::1]":      {"2001:db8::1", 22},
		"[2001:db8::1]:2200": {"2001:db8::1", 2200},
		"host_1.lan":         {"host_1.lan", 22},
	} {
		h, p, err := SplitSSHAddress(addr)
		if err != nil || h != want.host || p != want.port {
			t.Errorf("%s = %s %d %v", addr, h, p, err)
		}
	}
	for _, bad := range []string{"", "2001:db8::1", "nas:0", "nas:65536", "nas:ssh", "nas:+22", ":22", "[]:22", "[nas]:22", "a b", "-nas", "nas/x", "ssh://nas", "[2001:db8::1", "nas:22:22"} {
		if _, _, err := SplitSSHAddress(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("%q: the message repeats the value: %v", bad, err)
		}
	}
}

// Every rule of the section, each named by its entry and never repeating a value.
func TestSSHValidate(t *testing.T) {
	ok := SSHHost{Name: "nas", Address: "192.0.2.10", User: "alice", HostKey: testHostKey}
	if err := (SSH{Hosts: []SSHHost{ok, {Address: "pi.example.com:2222", User: "dash-reader"}, {Address: "192.0.2.11", User: "machine$"}}, IntervalSeconds: 60}).Validate(); err != nil {
		t.Fatalf("valid section: %v", err)
	}
	many := make([]SSHHost, MaxSSHHosts+1)
	for i := range many {
		many[i] = SSHHost{Name: "h" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)), Address: "192.0.2.1", User: "alice"}
	}
	for name, tc := range map[string]struct {
		s    SSH
		want string
	}{
		"too many":        {SSH{Hosts: many, IntervalSeconds: 5}, "at most 64 hosts"},
		"no address":      {SSH{Hosts: []SSHHost{{Name: "a", User: "alice"}}, IntervalSeconds: 5}, "entry 0: address is required"},
		"bad address":     {SSH{Hosts: []SSHHost{{Address: "evil-canary:99999", User: "alice"}}, IntervalSeconds: 5}, "entry 0: address has a port"},
		"no user":         {SSH{Hosts: []SSHHost{{Address: "a"}}, IntervalSeconds: 5}, "entry 0: user is required"},
		"bad user":        {SSH{Hosts: []SSHHost{{Address: "a", User: "Evil Canary"}}, IntervalSeconds: 5}, "entry 0: user is not a user name"},
		"long user":       {SSH{Hosts: []SSHHost{{Address: "a", User: strings.Repeat("a", 33)}}, IntervalSeconds: 5}, "user is not a user name"},
		"duplicate name":  {SSH{Hosts: []SSHHost{{Name: "NAS", Address: "a", User: "alice"}, {Name: "nas", Address: "b", User: "alice"}}, IntervalSeconds: 5}, "entry 1: name repeats an earlier host"},
		"implicit dup":    {SSH{Hosts: []SSHHost{{Address: "nas", User: "alice"}, {Address: "NAS:2222", User: "alice"}}, IntervalSeconds: 5}, "entry 1: name repeats"},
		"slash in name":   {SSH{Hosts: []SSHHost{{Name: "a/b", Address: "a", User: "alice"}}, IntervalSeconds: 5}, "entry 0: name may not hold /"},
		"control in name": {SSH{Hosts: []SSHHost{{Name: "a\nb", Address: "a", User: "alice"}}, IntervalSeconds: 5}, "name may not hold"},
		"long name":       {SSH{Hosts: []SSHHost{{Name: strings.Repeat("n", 65), Address: "a", User: "alice"}}, IntervalSeconds: 5}, "longer than 64 bytes"},
		"bad host key":    {SSH{Hosts: []SSHHost{{Address: "a", User: "alice", HostKey: "ssh-ed25519 canaryAAAA"}}, IntervalSeconds: 5}, "entry 0: hostKey is not a public key"},
		"key options":     {SSH{Hosts: []SSHHost{{Address: "a", User: "alice", HostKey: "restrict " + testHostKey}}, IntervalSeconds: 5}, "hostKey is not a public key"},
		"two keys":        {SSH{Hosts: []SSHHost{{Address: "a", User: "alice", HostKey: testHostKey + "\n" + testHostKey}}, IntervalSeconds: 5}, "hostKey is not a public key"},
		"interval low":    {SSH{IntervalSeconds: 1}, "intervalSeconds: must be 2 to 60"},
		"interval high":   {SSH{IntervalSeconds: 61}, "intervalSeconds: must be 2 to 60"},
	} {
		err := tc.s.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "canary") {
			t.Errorf("%s: the message repeats the value: %v", name, err)
		}
	}
	if err := (SSH{Hosts: []SSHHost{{Address: "a", User: "alice", HostKey: testHostKey + " root@example.com"}}, IntervalSeconds: 5}).Validate(); err != nil {
		t.Errorf("a key with a comment: %v", err)
	}
}
