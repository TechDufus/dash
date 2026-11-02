package config

// Values of AuthConfig.Mode.
const AuthModeProxy = "proxy"

// AuthConfig is the auth section: how dash learns who is making a request and what that person
// may do (design E.2, E.4). Every key is a restart setting except the two admin lists, which a
// reload applies to the next request.
type AuthConfig struct {
	Mode  string          `json:"mode" dash:"restart,enum=proxy" doc:"How dash learns who is making a request. proxy: an identity proxy in front of dash (oauth2-proxy, Authelia, Authentik) forwards the user name in a header, and dash believes it only from the peers in auth.proxy.trustedProxies."`
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

// Validate reports unusable auth settings, each at its full path under "auth" (the problem
// format of pluginconf.Problems). The scaffold accepts everything.
func (a AuthConfig) Validate() error { return nil }
