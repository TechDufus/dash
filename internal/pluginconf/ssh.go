package pluginconf

import (
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

// Limits of the ssh section; Validate enforces them.
const (
	// MaxSSHHosts is the most hosts plugins.ssh.hosts may list: one connection each.
	MaxSSHHosts = 64
	// MaxSSHHostName is the longest host name, in bytes: it titles a card and is part of a ref
	// and a route.
	MaxSSHHostName = 64
	// MinSSHInterval and MaxSSHInterval bound plugins.ssh.intervalSeconds.
	MinSSHInterval = 2
	MaxSSHInterval = 60
	// DefaultSSHPort is the port of an address that names none.
	DefaultSSHPort = 22
)

// sshUserRE is the shape of a user name. dash never puts the user in a shell (it is the SSH
// protocol's user field), so the rule only catches typos: the POSIX portable name set, as
// useradd accepts it, with an optional trailing $ (a machine account).
var sshUserRE = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}\$?$`)

// SSH is plugins.ssh: the Linux machines dash reads over SSH, with a key pair of its own.
type SSH struct {
	PluginBase
	// Hosts are the machines, in the order the Hosts page shows them.
	Hosts []SSHHost `json:"hosts" dash:"dial" doc:"The Linux machines dash reads over SSH, as [{name, address, user, hostKey}], shown in this order: address and user are required, at most 64 hosts. dash signs in with its own key pair (its public key is on the plugin's settings page) and runs one fixed read-only script; it installs nothing, never asks for sudo and holds no password. An address saved from the settings page may not reach a loopback address; one in the config file or the environment may. A JSON list in the environment."`
	// IntervalSeconds is how often a host that is up is sampled.
	IntervalSeconds int `json:"intervalSeconds" doc:"How often, in seconds, a host that is up sends a sample: 2 to 60. The inventory (disks, file systems, containers, services) is read about once a minute whatever this says."`
}

// SSHHost is one machine dash reads over SSH.
type SSHHost struct {
	Name    string `json:"name" doc:"The host's name in the web UI and in its ref and page route: at most 64 bytes, unique ignoring case, no / and no control characters. Empty uses the host part of the address."`
	Address string `json:"address" doc:"host or host:port (port 22 when it names none); an IPv6 address in brackets, [2001:db8::1]:22. Link-local addresses are always refused."`
	User    string `json:"user" doc:"The user dash signs in as: a dedicated unprivileged account is enough (letters, digits, _, . and -, starting with a lower-case letter or _)."`
	HostKey string `json:"hostKey" doc:"The host's public key in authorized_keys form (ssh-ed25519 AAAA...), which dash requires the host to present. Empty: dash reads the key the host offers, shows its fingerprint on the plugin's settings page and sends the host nothing until an administrator trusts it, which saves it here."`
}

// DefaultSSH is the section with nothing set: no host, a sample every 5 seconds.
func DefaultSSH() SSH { return SSH{IntervalSeconds: 5} }

// Normalized returns s with the whitespace of every field trimmed and every empty name replaced
// by the host part of its address, so that the plugin reads one form. s and its slice are not
// changed.
func (s SSH) Normalized() SSH {
	s.Hosts = append([]SSHHost(nil), s.Hosts...)
	for i := range s.Hosts {
		h := &s.Hosts[i]
		h.Name, h.Address, h.User, h.HostKey = strings.TrimSpace(h.Name), strings.TrimSpace(h.Address), strings.TrimSpace(h.User), strings.TrimSpace(h.HostKey)
		if h.Name == "" {
			if host, _, err := SplitSSHAddress(h.Address); err == nil {
				h.Name = host
			}
		}
	}
	return s
}

// Validate checks the host list and the interval. Its messages name the entry and never repeat a
// value.
func (s SSH) Validate() error {
	var p problems
	if len(s.Hosts) > MaxSSHHosts {
		p.add("hosts", "at most %d hosts", MaxSSHHosts)
	}
	names := map[string]bool{}
	for i, h := range s.Normalized().Hosts {
		_, _, addrErr := SplitSSHAddress(h.Address)
		switch {
		case h.Address == "":
			p.add("hosts", "entry %d: address is required", i)
		case addrErr != nil:
			p.add("hosts", "entry %d: address %s", i, addrErr.Error())
		}
		switch {
		case h.User == "":
			p.add("hosts", "entry %d: user is required", i)
		case !sshUserRE.MatchString(h.User):
			p.add("hosts", "entry %d: user is not a user name (letters, digits, _, . and -, starting with a lower-case letter or _, at most 32)", i)
		}
		switch name := h.Name; {
		case len(name) > MaxSSHHostName:
			p.add("hosts", "entry %d: name is longer than %d bytes", i, MaxSSHHostName)
		case strings.Contains(name, "/") || strings.ContainsFunc(name, unicode.IsControl) || !utf8.ValidString(name):
			p.add("hosts", "entry %d: name may not hold / or control characters", i)
		case name != "" && names[strings.ToLower(name)]:
			p.add("hosts", "entry %d: name repeats an earlier host (names are compared ignoring case)", i)
		}
		if h.Name != "" {
			names[strings.ToLower(h.Name)] = true
		}
		if h.HostKey != "" {
			if _, err := ParseSSHHostKey(h.HostKey); err != nil {
				p.add("hosts", "entry %d: hostKey is not a public key in authorized_keys form (ssh-ed25519 AAAA...)", i)
			}
		}
	}
	if s.IntervalSeconds < MinSSHInterval || s.IntervalSeconds > MaxSSHInterval {
		p.add("intervalSeconds", "must be %d to %d", MinSSHInterval, MaxSSHInterval)
	}
	return p.err()
}

// ParseSSHHostKey reads a hostKey value: one public key in authorized_keys form, without
// options. A comment after the key is allowed and ignored.
func ParseSSHHostKey(s string) (ssh.PublicKey, error) {
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return nil, err
	}
	if len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errSSHKeyShape
	}
	return key, nil
}

type sshAddrError string

func (e sshAddrError) Error() string { return string(e) }

const errSSHKeyShape = sshAddrError("one key without options")

// SplitSSHAddress reads an address of plugins.ssh.hosts: host or host:port, an IPv6 address in
// brackets with or without a port. The port is DefaultSSHPort when the address names none. The
// error says what is wrong in words that never repeat the value.
func SplitSSHAddress(addr string) (host string, port int, err error) {
	if addr == "" {
		return "", 0, sshAddrError("is empty")
	}
	host, portStr := addr, ""
	switch {
	case strings.HasPrefix(addr, "[") && strings.HasSuffix(addr, "]"):
		host = addr[1 : len(addr)-1]
	case strings.Count(addr, ":") == 1 || strings.HasPrefix(addr, "["):
		h, ps, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return "", 0, sshAddrError("is not host or host:port (an IPv6 address goes in brackets)")
		}
		host, portStr = h, ps
	case strings.Contains(addr, ":"):
		return "", 0, sshAddrError("is not host or host:port (an IPv6 address goes in brackets)")
	}
	port = DefaultSSHPort
	if portStr != "" {
		n, convErr := strconv.Atoi(portStr)
		if convErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != portStr {
			return "", 0, sshAddrError("has a port that is not 1 to 65535")
		}
		port = n
	}
	if host == "" {
		return "", 0, sshAddrError("has no host")
	}
	ip := net.ParseIP(host)
	if strings.HasPrefix(addr, "[") && (ip == nil || ip.To4() != nil) {
		return "", 0, sshAddrError("has brackets around something that is not an IPv6 address")
	}
	if ip != nil {
		return host, port, nil
	}
	if strings.Contains(host, ":") {
		return "", 0, sshAddrError("has an IPv6 address that does not parse")
	}
	if len(host) > 253 || !validHostname(host) {
		return "", 0, sshAddrError("has a host that is not a host name or an IP address")
	}
	return host, port, nil
}

// validHostname reports whether h is a DNS name: labels of letters, digits, - and _ (seen in
// the wild in local names), not starting or ending with -.
func validHostname(h string) bool {
	for label := range strings.SplitSeq(strings.TrimSuffix(h, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range []byte(label) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
