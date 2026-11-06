package pluginconf

import (
	"strings"

	"github.com/TechDufus/dash/internal/secret"
)

// TLS is how a plugin verifies the TLS certificate of the service it calls. Both fields decide
// where requests go (dest, design D16).
type TLS struct {
	CAFile             string `json:"caFile" dash:"dest" doc:"Path of a PEM file with the certificate authority that signed the service's certificate, added to the system roots. Empty uses the system roots only."`
	InsecureSkipVerify bool   `json:"insecureSkipVerify" dash:"dest" doc:"Skip verifying the service's certificate and host name. Anyone on the network path can then impersonate the service: prefer caFile."`
}

// BasicAuth is a user name and password sent as HTTP Basic authentication.
type BasicAuth struct {
	Username string       `json:"username" doc:"User name."`
	Password secret.Value `json:"password" dash:"secret" doc:"Password: a literal, env:NAME, file:/path or k8s:<namespace>/<name>#<key> (read from that Kubernetes Secret by the kubernetes plugin, which must run and be allowed to read it). Set together with username."`
}

// Auth is how a plugin authenticates to the service it calls: HTTP Basic or a bearer token,
// not both. Neither set sends no credentials.
type Auth struct {
	Basic       BasicAuth    `json:"basic" doc:"HTTP Basic authentication. Not together with bearerToken."`
	BearerToken secret.Value `json:"bearerToken" dash:"secret" doc:"Bearer token sent as Authorization: Bearer: a literal, env:NAME, file:/path or k8s:<namespace>/<name>#<key> (read from that Kubernetes Secret by the kubernetes plugin, which must run and be allowed to read it). Not together with basic."`
}

// validate checks the rules of Auth; the paths are relative to the Auth section. A secret that
// is a reference not read yet (k8s:) counts as given: the load cannot tell yet, and the plugin
// that owns the row says it is waiting.
func (a Auth) validate() error {
	var p problems
	basic := a.Basic.Username != "" || a.Basic.Password.Present()
	switch {
	case a.Basic.Username == "" && a.Basic.Password.Present():
		p.add("basic.username", "is required with basic.password")
	case a.Basic.Username != "" && !a.Basic.Password.Present():
		p.add("basic.password", "is required with basic.username")
	}
	if basic && a.BearerToken.Present() {
		p.add("bearerToken", "cannot be set together with basic")
	}
	return p.err()
}

// The agent policies of a plugin's actions (plugins.<id>.actions.agents, design O.1): what an
// mcp-audience principal's call of one of its action tools does.
const (
	AgentsDeny    = "deny"    // the action tools are hidden and refused
	AgentsApprove = "approve" // the call creates an approval request a person decides
	AgentsAllow   = "allow"   // the call runs at once, except on a target that needs its typed name
)

// Actions is the policy of a plugin's actions (plugins.<id>.actions.*): whether they are
// offered at all, the namespaces they refuse, and what an agent's call does. The core enforces
// all three.
type Actions struct {
	Enabled        bool     `json:"enabled" doc:"Offer the plugin's actions. Off by default: without it the actions are not mounted and the web UI does not show their buttons."`
	DenyNamespaces []string `json:"denyNamespaces" doc:"Namespaces the plugin's actions refuse, whatever the role. A JSON list in the environment."`
	Agents         string   `json:"agents" dash:"enum=deny|approve|allow" doc:"What an AI agent's call of one of the plugin's action tools (MCP, a token with the mcp audience) does: deny hides the tools and refuses them, approve creates an approval request a person approves or denies in dash, allow runs the action at once. A target that needs its name typed (a protected namespace) always waits for a person's approval, even with allow. API tokens and signed-in people are never affected. The default is approve. A change applies without a restart of dash; it restarts the plugin's instance."`
}

// AgentPolicy is the configured agent policy, approve when none is set.
func (a Actions) AgentPolicy() string {
	if a.Agents == "" {
		return AgentsApprove
	}
	return a.Agents
}

// validate checks the rules of Actions; the paths are relative to the actions section.
func (a Actions) validate() error {
	var p problems
	switch a.Agents {
	case "", AgentsDeny, AgentsApprove, AgentsAllow:
	default:
		p.add("agents", "must be one of: %s, %s, %s", AgentsDeny, AgentsApprove, AgentsAllow)
	}
	return p.err()
}

// ActionsToggle is Actions for a plugin whose actions have no namespace, where a deny list
// would do nothing.
type ActionsToggle struct {
	Enabled bool   `json:"enabled" doc:"Offer the plugin's actions. Off by default: without it the actions are not mounted and the web UI does not show their buttons."`
	Agents  string `json:"agents" dash:"enum=deny|approve|allow" doc:"What an AI agent's call of one of the plugin's action tools (MCP, a token with the mcp audience) does: deny hides the tools and refuses them, approve creates an approval request a person approves or denies in dash, allow runs the action at once. A target that needs its name typed (a protected namespace) always waits for a person's approval, even with allow. API tokens and signed-in people are never affected. The default is approve. A change applies without a restart of dash; it restarts the plugin's instance."`
}

// Actions is the toggle as the full policy, with no deny list.
func (a ActionsToggle) Actions() Actions { return Actions{Enabled: a.Enabled, Agents: a.Agents} }

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
