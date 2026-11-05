package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// Values of AuthConfig.Mode.
const (
	AuthModeProxy = "proxy"
	AuthModeNone  = "none"
	AuthModeLocal = "local"
	AuthModeOIDC  = "oidc"
)

// AuthConfig is the auth section: how dash learns who is making a request and what that person
// may do (design E.2, E.4). Every key is a restart setting except the admin lists, which a
// reload applies to the next request.
type AuthConfig struct {
	Mode  string          `json:"mode" dash:"restart,enum=proxy|none|local|oidc" doc:"How dash learns who is making a request. proxy: an identity proxy in front of dash (oauth2-proxy, Authelia, Authentik) forwards the user name in a header, and dash believes it only from the peers in auth.proxy.trustedProxies. local: one administrator account (auth.local.adminUser) signs in at /login with a password; the first start prints a one-time setup link, or DASH_ADMIN_PASSWORD_HASH supplies the password hash. oidc: users sign in at an OpenID Connect provider (auth.oidc), admitted only by auth.oidc.allowedUsers or allowedGroups. none: no authentication at all, every request is signed in as an administrator named anonymous; dash warns at every start and shows a banner on every page. It refuses to start unless server.listen is a loopback address such as 127.0.0.1:8080 (or auth.none.allowRemote is true), so keep the port reachable only from the machine itself. It is never the default."`
	Proxy AuthProxyConfig `json:"proxy" doc:"The proxy mode: which headers name the user, which peers are believed, and who is an administrator."`
	Local AuthLocalConfig `json:"local" doc:"The local mode: the one administrator account and the optional read-only access without signing in."`
	OIDC  AuthOIDCConfig  `json:"oidc" doc:"The oidc mode: the OpenID Connect provider, who may sign in, and who is an administrator."`
	None  AuthNoneConfig  `json:"none" doc:"The none mode: whether it may run on a listener other machines can reach."`
}

// AuthNoneConfig is the none mode of AuthConfig.
type AuthNoneConfig struct {
	AllowRemote bool `json:"allowRemote" dash:"restart" doc:"Let auth.mode=none start on a server.listen address that is not loopback (such as the default 0.0.0.0:8080). Anyone who can reach dash is then an administrator; dash warns at every start and refuses settings changes and connection tests. Without it, none on such an address refuses to start. It cannot be set from the settings page."`
}

// AuthOIDCConfig is the oidc mode of AuthConfig. The admin lists and adminRoles apply on a
// reload; everything else needs a restart.
type AuthOIDCConfig struct {
	Issuer        string       `json:"issuer" dash:"restart" doc:"The provider's issuer URL, exactly as its ID tokens name it (e.g. https://login.example.com/realms/home). https unless the host is a loopback address. dash discovers the provider on the first login, so an unreachable provider does not stop dash from starting."`
	ClientID      string       `json:"clientID" dash:"restart" doc:"The client ID dash is registered under at the provider. Its redirect URL is server.publicURL followed by /auth/callback."`
	ClientSecret  secret.Value `json:"clientSecret" dash:"secret,restart" doc:"The client secret: a literal, env:NAME, file:/path or k8s:<namespace>/<name>#<key>. It is read at each login, so a rotated file applies without a restart."`
	Scopes        []string     `json:"scopes" dash:"restart" doc:"The scopes dash asks for; openid is always added. A JSON list in the environment."`
	UserClaim     string       `json:"userClaim" dash:"restart" doc:"The ID token claim that names the user. preferred_username can be changed by the user at some providers: use sub there. email is accepted only with email_verified true."`
	GroupsClaim   string       `json:"groupsClaim" dash:"restart" doc:"The ID token claim that lists the user's groups: a list of strings or one comma-separated string. Empty turns groups off; adminGroups and allowedGroups need it."`
	RoleClaim     string       `json:"roleClaim" dash:"restart" doc:"The ID token claim whose values are matched against adminRoles. Empty turns it off."`
	AdminRoles    []string     `json:"adminRoles" doc:"Values of roleClaim that make the user an administrator. A JSON list in the environment. Applies without a restart."`
	AllowedUsers  []string     `json:"allowedUsers" dash:"restart" doc:"The users who may sign in, exactly as userClaim names them. [\"*\"] admits every account the provider signs in (dash warns at every start). With allowedGroups empty one of the two must be set: dash does not start otherwise. A JSON list in the environment."`
	AllowedGroups []string     `json:"allowedGroups" dash:"restart" doc:"Groups whose members may sign in; needs groupsClaim. A JSON list in the environment."`
	AdminUsers    []string     `json:"adminUsers" doc:"User names that are administrators: exact and case-sensitive. [\"*\"] makes every user who may sign in an administrator (dash warns). A JSON list in the environment. Applies without a restart."`
	AdminGroups   []string     `json:"adminGroups" doc:"Groups whose members are administrators; needs groupsClaim. A group added here applies at the member's next login, a removed one at once. A JSON list in the environment. Applies without a restart."`
}

// AuthLocalConfig is the local mode of AuthConfig.
type AuthLocalConfig struct {
	AdminUser       string `json:"adminUser" dash:"restart" doc:"The name of the one administrator account; a login succeeds for this name only. Changing it after setup makes dash refuse to start until it is set back or credentials.yaml is deleted to run setup again."`
	AnonymousViewer bool   `json:"anonymousViewer" dash:"restart" doc:"Let a visitor who has not signed in read the dashboard as a viewer once the administrator account exists (before setup, a visitor is sent to the setup link instead): GET requests only, never logs, never actions. Only in auth.mode=local; dash warns at every start while it is on and lists anonymous_viewer in an administrator's /api/v1/me warnings."`
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

// DefaultAuth is the built-in layer of the auth section: the local mode (design J.6; v0.11.x
// defaulted to proxy, so an install behind an identity proxy sets DASH_AUTH_MODE=proxy), with an
// account named admin and no anonymous viewer; the proxy mode believes the loopback peers a
// sidecar proxy connects from and makes nobody an administrator.
func DefaultAuth() AuthConfig {
	return AuthConfig{
		Mode: AuthModeLocal,
		Proxy: AuthProxyConfig{
			UserHeaders:    []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User"},
			TrustedProxies: []string{"127.0.0.1/32", "::1/128"},
		},
		Local: AuthLocalConfig{AdminUser: "admin"},
		OIDC: AuthOIDCConfig{
			Scopes:      []string{"openid", "profile", "email"},
			UserClaim:   "preferred_username",
			GroupsClaim: "groups",
			AdminRoles:  []string{"admin"},
		},
	}
}

// authModes are the accepted values of AuthConfig.Mode, in the order the error lists them; the
// enum tag on the field documents the same set.
var authModes = []string{AuthModeProxy, AuthModeNone, AuthModeLocal, AuthModeOIDC}

// Validate reports unusable auth settings, each at its path relative to "auth" (the problem
// format of pluginconf.Problems). A message names the path, the entry's position in a list and
// the problem, and never echoes a value, so no secret pasted into a field can leak into it.
func (a AuthConfig) Validate() error {
	var errs []error
	add := func(path, format string, args ...any) {
		errs = append(errs, pluginconf.FieldError{Path: path, Msg: fmt.Sprintf(format, args...)})
	}
	if !slices.Contains(authModes, a.Mode) {
		add("mode", "must be one of: %s", strings.Join(authModes, ", "))
	}

	p := a.Proxy
	users := trimmed(p.UserHeaders)
	groups := strings.TrimSpace(p.GroupsHeader)
	if len(users) == 0 {
		add("proxy.userHeaders", "must list at least one user header")
	}
	for i, h := range users {
		if problem := proxyHeaderProblem(h); problem != "" {
			add("proxy.userHeaders", "entry %d %s", i+1, problem)
		}
	}
	if groups != "" {
		if problem := proxyHeaderProblem(groups); problem != "" {
			add("proxy.groupsHeader", "%s", problem)
		}
	}

	if len(p.TrustedProxies) == 0 {
		add("proxy.trustedProxies", "must list at least one peer: with none, no request could be signed in (the default is [\"127.0.0.1/32\",\"::1/128\"])")
	}
	var parsed []string
	for i, e := range p.TrustedProxies {
		if _, err := auth.ParseTrust([]string{e}); err != nil {
			if strings.TrimSpace(e) == "" {
				add("proxy.trustedProxies", "entry %d is empty", i+1)
			} else {
				add("proxy.trustedProxies", "entry %d is not a CIDR or IP address without a zone, such as 10.0.0.0/8 or 192.0.2.1", i+1)
			}
			continue
		}
		parsed = append(parsed, e)
	}
	if t, err := auth.ParseTrust(parsed); err == nil && t.Wide() && !p.AllowAnyPeer {
		add("proxy.trustedProxies", "0.0.0.0/0 or ::/0 would believe an identity header from every client; set allowAnyPeer to true to accept that")
	}

	// An admin entry no identity header can carry is refused (it was a startup warning in
	// v0.11.1, which as a patch release added no load error): by position, never by value.
	for i, u := range trimmed(p.AdminUsers) {
		switch {
		case u == "":
			add("proxy.adminUsers", "entry %d is empty", i+1)
		case u != "*" && !auth.ValidUserName(u):
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

	switch u := auth.TrimIdentity(a.Local.AdminUser); {
	case u == "":
		add("local.adminUser", "is empty: the local mode's administrator account needs a name")
	case !auth.ValidUserName(u):
		add("local.adminUser", "can never be a user name: a name is at most 128 bytes of valid UTF-8 without control characters")
	}
	if a.Local.AnonymousViewer && a.Mode != AuthModeLocal {
		add("local.anonymousViewer", "is only available in auth.mode=local: the proxy and oidc modes never sign a visitor in without an identity")
	}
	a.OIDC.validate(a.Mode == AuthModeOIDC, add)
	return errors.Join(errs...)
}

// proxyHeaderProblem is why name cannot carry the proxy's identity, or "" when it can: the rule
// of auth.ProxyIdentity.Validate, whose message would repeat the name.
func proxyHeaderProblem(name string) string {
	if (auth.ProxyIdentity{UserHeaders: []string{name}}).Validate() == nil {
		return ""
	}
	for _, f := range []string{"Authorization", "Cookie", "Host", "X-Dash-Csrf"} {
		if strings.EqualFold(name, f) {
			return "is one of the headers dash refuses there: Authorization, Cookie, Host, X-Dash-Csrf"
		}
	}
	if name == "" {
		return "is empty"
	}
	return "is not a valid HTTP header name (letters, digits and !#$%&'*+-.^_`|~ only)"
}

// validate reports unusable oidc settings through add. The lists are checked in every mode: an
// entry that can never match is a mistake whatever the mode, and it is refused at load from the
// start (these keys are new in v0.12.0, so there is no warning phase). The criteria are what the
// OIDC claim path keeps (internal/oidcauth): a user claim only as a valid user name, group and role
// values only as valid group names (no comma, even from a JSON list). The provider, the client and
// the fail-closed allow-list are required only in the oidc mode. No message echoes a value.
func (o AuthOIDCConfig) validate(active bool, add func(path, format string, args ...any)) {
	users := func(path string, l []string) {
		for i, u := range trimmed(l) {
			switch {
			case u == "":
				add(path, "entry %d is empty", i+1)
			case u != "*" && !auth.ValidUserName(u):
				add(path, "entry %d can never match: dash accepts a user claim only as a name of at most 128 bytes of valid UTF-8 without control characters", i+1)
			}
		}
	}
	values := func(path, what string, l []string) {
		for i, v := range trimmed(l) {
			switch {
			case v == "":
				add(path, "entry %d is empty", i+1)
			case v == "*":
				add(path, "entry %d is a wildcard: \"*\" is only valid in adminUsers and allowedUsers", i+1)
			case !auth.ValidGroupName(v):
				add(path, "entry %d can never match: dash keeps a %s only as at most 256 bytes of valid UTF-8 without control characters or commas", i+1, what)
			}
		}
	}
	users("oidc.adminUsers", o.AdminUsers)
	users("oidc.allowedUsers", o.AllowedUsers)
	values("oidc.adminGroups", "group", o.AdminGroups)
	values("oidc.allowedGroups", "group", o.AllowedGroups)
	values("oidc.adminRoles", "role value", o.AdminRoles)
	if strings.TrimSpace(o.GroupsClaim) == "" {
		for path, l := range map[string][]string{"oidc.adminGroups": o.AdminGroups, "oidc.allowedGroups": o.AllowedGroups} {
			if len(l) > 0 {
				add(path, "needs auth.oidc.groupsClaim: without the claim no user has a group, so nobody would ever match through it")
			}
		}
	}
	if !active {
		return
	}
	if strings.TrimSpace(o.Issuer) == "" {
		add("oidc.issuer", "is required in auth.mode=oidc")
	} else if u, err := url.Parse(strings.TrimSpace(o.Issuer)); err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!(u.Scheme == "https" || (u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		add("oidc.issuer", "must be an https URL with a host and no query (http only for a loopback host such as 127.0.0.1 or localhost)")
	}
	if strings.TrimSpace(o.ClientID) == "" {
		add("oidc.clientID", "is required in auth.mode=oidc")
	}
	if !o.ClientSecret.Present() {
		add("oidc.clientSecret", "is required in auth.mode=oidc")
	}
	if strings.TrimSpace(o.UserClaim) == "" {
		add("oidc.userClaim", "is required in auth.mode=oidc (the default is preferred_username)")
	}
	if len(o.AllowedUsers) == 0 && len(o.AllowedGroups) == 0 {
		add("oidc.allowedUsers", "or auth.oidc.allowedGroups is required in auth.mode=oidc: with neither, every account the provider signs in would be admitted; set allowedUsers to [\"*\"] to admit them all on purpose")
	}
}

// loopbackHost reports whether host is localhost or a loopback IP address.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// normalize trims what operators paste into the lists the authenticators read, the local account
// name and the oidc claim names. An empty entry stays: Validate reports it.
func (a *AuthConfig) normalize() {
	o := &a.OIDC
	for _, l := range []*[]string{
		&a.Proxy.UserHeaders, &a.Proxy.TrustedProxies, &a.Proxy.AdminUsers, &a.Proxy.AdminGroups,
		&o.Scopes, &o.AdminRoles, &o.AllowedUsers, &o.AllowedGroups, &o.AdminUsers, &o.AdminGroups,
	} {
		if *l != nil {
			*l = trimmed(*l)
		}
	}
	a.Local.AdminUser = auth.TrimIdentity(a.Local.AdminUser)
	for _, s := range []*string{&o.Issuer, &o.ClientID, &o.UserClaim, &o.GroupsClaim, &o.RoleClaim} {
		*s = strings.TrimSpace(*s)
	}
}

// SessionMode reports whether mode signs people in with a session cookie of dash's own.
func SessionMode(mode string) bool { return mode == AuthModeLocal || mode == AuthModeOIDC }

// proxyKeysOutsideProxyMode lists the auth.proxy settings a layer set (the file, the environment,
// the UI) while a session mode is selected and dev or demo does not override it: those keys are
// never read there. The admin lists are refused (Validate's caller), because an install that
// relied on the implicit proxy default would otherwise lose its administrators silently; the
// other keys only warn (Resolve). Paths only, in schema order, never a value.
func proxyKeysOutsideProxyMode(t Tree, prov map[string]Provenance, dev, demo bool) (refused, ignored []string) {
	if dev || demo || !SessionMode(t.Auth.Mode) {
		return nil, nil
	}
	for _, l := range schema.leaves {
		if !strings.HasPrefix(l.path, "auth.proxy.") || prov[l.path].Source == SourceDefault || prov[l.path].Source == "" {
			continue
		}
		if l.path == "auth.proxy.adminUsers" || l.path == "auth.proxy.adminGroups" {
			refused = append(refused, l.path)
		} else {
			ignored = append(ignored, l.path)
		}
	}
	return refused, ignored
}
