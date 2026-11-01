package pluginconf

import (
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// TLS is how a plugin verifies the TLS certificate of the service it calls.
type TLS struct {
	CAFile             string `json:"caFile" doc:"Path of a PEM file with the certificate authority that signed the service's certificate, added to the system roots. Empty uses the system roots only."`
	InsecureSkipVerify bool   `json:"insecureSkipVerify" doc:"Skip verifying the service's certificate and host name. Anyone on the network path can then impersonate the service: prefer caFile."`
}

// BasicAuth is a user name and password sent as HTTP Basic authentication.
type BasicAuth struct {
	Username string       `json:"username" doc:"User name."`
	Password secret.Value `json:"password" dash:"secret" doc:"Password: a literal, env:NAME or file:/path. Set together with username."`
}

// Auth is how a plugin authenticates to the service it calls: HTTP Basic or a bearer token,
// not both. Neither set sends no credentials.
type Auth struct {
	Basic       BasicAuth    `json:"basic" doc:"HTTP Basic authentication. Not together with bearerToken."`
	BearerToken secret.Value `json:"bearerToken" dash:"secret" doc:"Bearer token sent as Authorization: Bearer: a literal, env:NAME or file:/path. Not together with basic."`
}

// validate checks the rules of Auth; the paths are relative to the Auth section.
func (a Auth) validate() error {
	var p problems
	basic := a.Basic.Username != "" || a.Basic.Password.IsSet()
	switch {
	case a.Basic.Username == "" && a.Basic.Password.IsSet():
		p.add("basic.username", "is required with basic.password")
	case a.Basic.Username != "" && !a.Basic.Password.IsSet():
		p.add("basic.password", "is required with basic.username")
	}
	if basic && a.BearerToken.IsSet() {
		p.add("bearerToken", "cannot be set together with basic")
	}
	return p.err()
}

// Actions is the policy of a plugin's actions (plugins.<id>.actions.*): whether they are
// offered at all, and the namespaces they refuse. The core enforces both.
type Actions struct {
	Enabled        bool     `json:"enabled" doc:"Offer the plugin's actions. Off by default: without it the actions are not mounted and the web UI does not show their buttons."`
	DenyNamespaces []string `json:"denyNamespaces" doc:"Namespaces the plugin's actions refuse, whatever the role. A JSON list in the environment."`
}

// ActionsToggle is Actions for a plugin whose actions have no namespace, where a deny list
// would do nothing.
type ActionsToggle struct {
	Enabled bool `json:"enabled" doc:"Offer the plugin's actions. Off by default: without it the actions are not mounted and the web UI does not show their buttons."`
}

// isToken reports whether s is an HTTP token (RFC 9110 5.6.2), the grammar of a header name.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
