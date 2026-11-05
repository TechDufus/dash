package pluginconf

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// Prometheus is plugins.prometheus: VictoriaMetrics or Prometheus, the series behind charts,
// KPIs and host metrics.
type Prometheus struct {
	PluginBase
	URL     string            `json:"url" dash:"url" doc:"Base URL of the service, e.g. http://host:9090. Empty turns the source off: dash makes no request and its panels show \"source not configured\"."`
	Timeout Duration          `json:"timeout" doc:"How long one query may take before dash gives up on it."`
	Auth    Auth              `json:"auth" doc:"How dash authenticates to the service. Neither basic nor bearerToken sends no credentials."`
	Headers map[string]string `json:"headers" doc:"Extra headers sent with every request, as {name: value}. Plain configuration, not secrets: put credentials in auth. Authorization, Proxy-Authorization, Cookie and Host cannot be set. A JSON object in the environment."`
	TLS     TLS               `json:"tls" doc:"How dash verifies the service's certificate."`
}

// DefaultPrometheus is the section with nothing set.
func DefaultPrometheus() Prometheus { return Prometheus{Timeout: Duration(5 * time.Second)} }

// forbiddenHeaders are set by dash from auth or by the HTTP client; a configured value would
// either leak a credential outside the secret handling or change where the request goes.
var forbiddenHeaders = []string{"authorization", "proxy-authorization", "cookie", "host"}

// Validate checks what the schema cannot: basic auth needs both halves and excludes the bearer
// token, and every header name is a valid token that dash does not set itself.
func (p Prometheus) Validate() error {
	var pr problems
	pr.addAll("auth", p.Auth.validate())
	// A header is named by neither its name nor its value: the map is in the operator's words,
	// and a credential pasted into a name would otherwise reach the form and the audit.
	for _, name := range slices.Sorted(maps.Keys(p.Headers)) {
		switch {
		case !isToken(name):
			pr.add("headers", "has a name that is not a valid HTTP header name (letters, digits and !#$%%&'*+-.^_`|~ only)")
		case slices.Contains(forbiddenHeaders, strings.ToLower(name)):
			pr.add("headers", "sets Authorization, Proxy-Authorization, Cookie or Host, which dash sets itself (use auth for credentials)")
		}
	}
	return pr.err()
}
