package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// Values of AuthConfig.Mode.
const (
	AuthModeProxy = "proxy"
	AuthModeNone  = "none"
)

// AuthConfig is the auth section: how dash learns who is making a request and what that person
// may do (design E.2, E.4). Every key is a restart setting except the two admin lists, which a
// reload applies to the next request.
type AuthConfig struct {
	Mode  string          `json:"mode" dash:"restart,enum=proxy|none" doc:"How dash learns who is making a request. proxy: an identity proxy in front of dash (oauth2-proxy, Authelia, Authentik) forwards the user name in a header, and dash believes it only from the peers in auth.proxy.trustedProxies. none: no authentication at all, every request is signed in as an administrator named anonymous; dash warns at every start and shows a banner on every page, so bind server.listen to 127.0.0.1 and keep the port reachable only from the machine itself. It is never the default."`
	Proxy AuthProxyConfig `json:"proxy" doc:"The proxy mode: which headers name the user, which peers are believed, and who is an administrator."`
}

// AuthProxyConfig is the proxy mode of AuthConfig.
type AuthProxyConfig struct {
	UserHeaders    []string `json:"userHeaders" dash:"restart" doc:"Request headers that carry the user name, in order of preference: the first one the proxy sets is the user. A JSON list in the environment."`
	GroupsHeader   string   `json:"groupsHeader" dash:"restart" doc:"The request header the proxy lists the user's groups in, comma-separated (oauth2-proxy: X-Forwarded-Groups). Empty turns groups off; adminGroups needs it."`
	TrustedProxies []string `json:"trustedProxies" dash:"restart" doc:"The peers whose identity headers dash believes: CIDRs or IP addresses of the TCP connection (X-Forwarded-For is never read). A request with identity headers from any other peer gets 401. The default is the loopback, where a sidecar proxy connects from. A JSON list in the environment."`
	AllowAnyPeer   bool     `json:"allowAnyPeer" dash:"restart" doc:"Allow 0.0.0.0/0 or ::/0 in trustedProxies. Every client could then forge an identity, so it needs this explicit opt-in and dash warns at every start."`
	AdminUsers     []string `json:"adminUsers" doc:"User names that are administrators: exact and case-sensitive. [\"*\"] makes every user an administrator (dash warns). With both admin lists empty every user is a viewer: read-only, no actions. A JSON list in the environment. Applies without a restart."`
	AdminGroups    []string `json:"adminGroups" doc:"Groups whose members are administrators; needs groupsHeader. A JSON list in the environment. Applies without a restart."`
}

// DefaultAuth is the built-in layer of the auth section: the proxy mode, believing the
// loopback peers a sidecar proxy connects from, and nobody an administrator.
func DefaultAuth() AuthConfig {
	return AuthConfig{
		Mode: AuthModeProxy,
		Proxy: AuthProxyConfig{
			UserHeaders:    []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User"},
			TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		},
	}
}

// authModes are the accepted values of AuthConfig.Mode, in the order the error lists them; the
// enum tag on the field documents the same set.
var authModes = []string{AuthModeProxy, AuthModeNone}

// Validate reports unusable auth settings, each at its path relative to "auth" (the problem
// format of pluginconf.Problems). A message names the path and the problem and never echoes a
// value beyond the header name or trusted-proxy entry it is about, so no secret can leak into it.
func (a AuthConfig) Validate() error {
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, pluginconf.FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}
	if !slices.Contains(authModes, a.Mode) {
		add("mode", "%q is not an auth mode (one of: %s)", a.Mode, strings.Join(authModes, ", "))
	}

	p := a.Proxy
	users := trimmed(p.UserHeaders)
	groups := strings.TrimSpace(p.GroupsHeader)
	if err := (auth.ProxyIdentity{UserHeaders: users}).Validate(); err != nil {
		add("proxy.userHeaders", "%v", err)
	}
	if groups != "" {
		// The stand-in user header is valid, so an error here is about the groups header alone.
		if err := (auth.ProxyIdentity{UserHeaders: []string{"X-Forwarded-User"}, GroupsHeader: groups}).Validate(); err != nil {
			add("proxy.groupsHeader", "%v", err)
		}
	}

	if len(p.TrustedProxies) == 0 {
		add("proxy.trustedProxies", "must list at least one peer: with none, no request could be signed in (the default is [\"127.0.0.1/32\",\"::1/128\"])")
	}
	var parsed []string
	for _, e := range p.TrustedProxies {
		if _, err := auth.ParseTrust([]string{e}); err != nil {
			add("proxy.trustedProxies", "%v", err)
			continue
		}
		parsed = append(parsed, e)
	}
	if t, err := auth.ParseTrust(parsed); err == nil && t.Wide() && !p.AllowAnyPeer {
		add("proxy.trustedProxies", "0.0.0.0/0 or ::/0 would believe an identity header from every client; set allowAnyPeer to true to accept that")
	}

	for i, u := range trimmed(p.AdminUsers) {
		if u == "" {
			add("proxy.adminUsers", "entry %d is empty", i+1)
		} else if !auth.ValidUserName(u) {
			add("proxy.adminUsers", "entry %d can never match a user: a name is at most 128 bytes of valid UTF-8 without control characters, so no header could carry it", i+1)
		}
	}
	for i, g := range trimmed(p.AdminGroups) {
		switch {
		case g == "":
			add("proxy.adminGroups", "entry %d is empty", i+1)
		case g == "*":
			add("proxy.adminGroups", "entry %d is a wildcard: \"*\" is only valid in adminUsers", i+1)
		case !auth.ValidGroupName(g):
			add("proxy.adminGroups", "entry %d can never match a group: a name is at most 256 bytes of valid UTF-8 without control characters or commas (the groups header is split at commas), so no header could carry it", i+1)
		}
	}
	if len(p.AdminGroups) > 0 && groups == "" {
		add("proxy.adminGroups", "needs auth.proxy.groupsHeader: without the header no user has a group, so nobody would ever be an administrator through it")
	}
	return errors.Join(errs...)
}

// trimmed returns the entries of l without surrounding space or tab, the same rule that trims a
// header value (auth.TrimIdentity), so a padded entry names exactly the user or group a padded
// header value does and a no-break space is never silently dropped from an admin's name.
// normalize belongs to the resolver, which this does not wait for.
func trimmed(l []string) []string {
	out := make([]string, len(l))
	for i, s := range l {
		out[i] = auth.TrimIdentity(s)
	}
	return out
}

// normalize trims what operators paste into the lists the authenticator reads. An empty entry
// stays: Validate reports it.
func (a *AuthConfig) normalize() {
	for _, l := range []*[]string{&a.Proxy.UserHeaders, &a.Proxy.TrustedProxies, &a.Proxy.AdminUsers, &a.Proxy.AdminGroups} {
		if *l != nil {
			*l = trimmed(*l)
		}
	}
}
