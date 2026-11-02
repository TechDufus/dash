package pluginconf

import "github.com/TechDufus/dash/internal/secret"

// Proxmox is plugins.proxmox: the Proxmox API. TokenID and TokenSecret are the two halves of an
// API token ("user@realm!name" and its secret), so that only the secret is secret.
type Proxmox struct {
	PluginBase
	URL         string       `json:"url" dash:"url" doc:"Base URL of the Proxmox API, e.g. https://pve.example.com:8006. Required with a token."`
	Node        string       `json:"node" doc:"The Proxmox node to read. Empty is discovered from the API when the cluster has exactly one node; set it for several nodes or to skip discovery."`
	TokenID     string       `json:"tokenID" doc:"API token ID, user@realm!tokenname. Set together with tokenSecret. Empty turns Proxmox off."`
	TokenSecret secret.Value `json:"tokenSecret" dash:"secret" doc:"API token secret: a literal, env:NAME or file:/path. Set together with tokenID."`
}

// Validate checks that the two halves of the token come together and that a token has a URL.
func (p Proxmox) Validate() error {
	var pr problems
	if (p.TokenID == "") != !p.TokenSecret.IsSet() {
		pr.add("tokenSecret", "tokenID and tokenSecret must be set together")
	}
	if p.TokenID != "" && p.URL == "" {
		pr.add("url", "is required with a Proxmox token")
	}
	return pr.err()
}
