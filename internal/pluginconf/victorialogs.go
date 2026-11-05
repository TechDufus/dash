package pluginconf

import "regexp"

// fieldNameRE is what a LogsQL field name may be. The names are concatenated into the query,
// so anything else could inject LogsQL.
var fieldNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// LogFields maps the pod a log line belongs to onto the field names of the log records.
type LogFields struct {
	Namespace string `json:"namespace" doc:"Field holding the pod's namespace."`
	Pod       string `json:"pod" doc:"Field holding the pod's name."`
	Container string `json:"container" doc:"Field holding the container's name."`
}

// VictoriaLogs is plugins.victorialogs: the pod logs the UI shows.
type VictoriaLogs struct {
	PluginBase
	URL            string    `json:"url" dash:"url" doc:"Base URL of the service, e.g. http://host:port. Empty turns the source off: dash makes no request and its panels show \"source not configured\"."`
	Auth           Auth      `json:"auth" doc:"How dash authenticates to VictoriaLogs. Neither basic nor bearerToken sends no credentials."`
	TLS            TLS       `json:"tls" doc:"How dash verifies VictoriaLogs's certificate."`
	Fields         LogFields `json:"fields" doc:"The field names of the log records that identify the pod. Letters, digits, underscores and dots, not starting with a digit or a dot."`
	Limit          int       `json:"limit" doc:"Most log lines one query returns. Greater than zero."`
	DenyNamespaces []string  `json:"denyNamespaces" doc:"Namespaces whose logs dash refuses to show, whatever the role. A JSON list in the environment."`
}

// DefaultVictoriaLogs is the section with nothing set.
func DefaultVictoriaLogs() VictoriaLogs {
	return VictoriaLogs{
		Fields: LogFields{Namespace: "kubernetes.pod_namespace", Pod: "kubernetes.pod_name", Container: "kubernetes.container_name"},
		Limit:  500,
	}
}

// Validate checks what the schema cannot: the auth rules, field names that are safe to put in a
// query, and a positive limit.
func (v VictoriaLogs) Validate() error {
	var p problems
	p.addAll("auth", v.Auth.validate())
	for _, f := range []struct{ path, name string }{
		{"fields.namespace", v.Fields.Namespace}, {"fields.pod", v.Fields.Pod}, {"fields.container", v.Fields.Container},
	} {
		if !fieldNameRE.MatchString(f.name) {
			p.add(f.path, "is not a field name (letters, digits, _ and ., not starting with a digit or .)")
		}
	}
	if v.Limit <= 0 {
		p.add("limit", "must be greater than zero")
	}
	return p.err()
}

// ReadDenyConfig implements the route policy hook: the namespaces the read routes refuse.
func (v VictoriaLogs) ReadDenyConfig() []string { return v.DenyNamespaces }
