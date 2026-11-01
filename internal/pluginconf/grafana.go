package pluginconf

// Grafana is plugins.grafana: links to dashboards.
type Grafana struct {
	PluginBase
	URL string `json:"url" dash:"url" doc:"Base URL of Grafana, e.g. https://grafana.example.com. Adds a link to Grafana's alert list on alerts and to Grafana on the host. Empty turns the Grafana links off."`
}
