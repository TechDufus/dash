package config

import (
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
)

// PushConfig is push: Web Push and UnifiedPush notifications (design P.2, contract D46). Every
// key needs a restart.
type PushConfig struct {
	Contact      string   `json:"contact" dash:"restart" doc:"The contact push services see in dash's VAPID signature (RFC 8292): mailto:<address> or an https URL. Defaults to server.publicURL when that is https; without either, push stays off. A push service may write to it when dash misbehaves."`
	AllowPrivate []string `json:"allowPrivate" dash:"restart" doc:"Push endpoints dash may post to although they resolve to a private or loopback address, such as a UnifiedPush server (ntfy) on the LAN: host names (ntfy.example.com), IP addresses or CIDRs (192.0.2.0/24). Empty (the default): only global addresses. A host entry admits that host name at private and loopback addresses; a CIDR admits the private and loopback addresses inside it, whatever the name. Link-local addresses (the cloud metadata service) never pass, and endpoints stay https. A JSON list in the environment."`
}

// PushAllow is push.allowPrivate parsed: lowercased host names and address prefixes.
type PushAllow struct {
	Hosts    []string
	Prefixes []netip.Prefix
}

// Empty reports whether nothing is allowed beyond global addresses.
func (a PushAllow) Empty() bool { return len(a.Hosts) == 0 && len(a.Prefixes) == 0 }

// parsePushAllow splits push.allowPrivate into host names and prefixes; bad holds the 1-based
// positions of entries that are neither.
func parsePushAllow(list []string) (a PushAllow, bad []int) {
	for i, e := range list {
		switch {
		case strings.Contains(e, "/"):
			p, err := netip.ParsePrefix(e)
			if err != nil {
				bad = append(bad, i+1)
				continue
			}
			a.Prefixes = append(a.Prefixes, p.Masked())
		default:
			if ip, err := netip.ParseAddr(e); err == nil {
				a.Prefixes = append(a.Prefixes, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
				continue
			}
			h := strings.ToLower(strings.TrimSuffix(e, "."))
			if !hostNameRE.MatchString(h) {
				bad = append(bad, i+1)
				continue
			}
			a.Hosts = append(a.Hosts, h)
		}
	}
	return a, bad
}

// pushContactProblem is why c cannot be push.contact, or "" when it can: mailto: with one
// address, or an https URL with a host. The message never repeats the value.
func pushContactProblem(c string) string {
	u, err := url.Parse(c)
	if err != nil {
		return "must be mailto:<address> or an https URL"
	}
	switch strings.ToLower(u.Scheme) {
	case "mailto":
		addr := u.Opaque
		if addr == "" {
			addr = u.Path
		}
		if a, err := mail.ParseAddress(addr); err != nil || a.Name != "" || a.Address != addr || u.RawQuery != "" {
			return "must be mailto: with one e-mail address and nothing else (mailto:ops@example.com)"
		}
	case "https":
		if u.Host == "" || u.User != nil {
			return "must be an https URL with a host and no user information"
		}
	default:
		return "must be mailto:<address> or an https URL: push services refuse other contacts"
	}
	return ""
}

// pushContact is the contact dash signs with: push.contact, else the public URL when it is
// https; "" when there is none, which keeps push off.
func pushContact(t Tree) string {
	if t.Push.Contact != "" {
		return t.Push.Contact
	}
	if u, err := url.Parse(t.Server.PublicURL); err == nil && u.Scheme == "https" && u.Host != "" {
		return t.Server.PublicURL
	}
	return ""
}

// validatePush reports unusable push settings, by position and never by value.
func validatePush(p PushConfig, v *validator) {
	if p.Contact != "" {
		if problem := pushContactProblem(p.Contact); problem != "" {
			v.fail("push.contact", "%s", problem)
		}
	}
	_, bad := parsePushAllow(p.AllowPrivate)
	for _, n := range bad {
		v.fail("push.allowPrivate", "entry %d is not a host name, an IP address or a CIDR (no scheme or port)", n)
	}
}
