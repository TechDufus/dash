package pluginconf

// Grafana is plugins.grafana: links to dashboards.
type Grafana struct {
	PluginBase
	URL string `json:"url" dash:"url" doc:"Base URL of the service, e.g. http://host:9090. Empty turns the source off: dash makes no request and its panels show \"source not configured\"."`
}
