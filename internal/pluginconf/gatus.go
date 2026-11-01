package pluginconf

// Gatus is plugins.gatus: endpoint health.
type Gatus struct {
	PluginBase
	URL            string   `json:"url" dash:"url" doc:"Base URL of the service, e.g. http://host:9090. Empty turns the source off: dash makes no request and its panels show \"source not configured\"."`
	Auth           Auth     `json:"auth" doc:"How dash authenticates to Gatus. Neither basic nor bearerToken sends no credentials."`
	TLS            TLS      `json:"tls" doc:"How dash verifies Gatus's certificate."`
	CriticalGroups []string `json:"criticalGroups" doc:"Gatus groups whose failing checks are critical (compared without regard to case); a failing check in any other group is a warning. The Status page lists these groups first. Empty makes every failure a warning. A JSON list in the environment."`
	PageSize       int      `json:"pageSize" doc:"Endpoints requested from Gatus per page. Greater than zero."`
}

// DefaultGatus is the section with nothing set.
func DefaultGatus() Gatus { return Gatus{PageSize: 30} }

// Validate checks what the schema cannot: the auth rules and a positive page size.
func (g Gatus) Validate() error {
	var p problems
	p.addAll("auth", g.Auth.validate())
	if g.PageSize <= 0 {
		p.add("pageSize", "must be greater than zero")
	}
	return p.err()
}
