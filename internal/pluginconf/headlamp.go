package pluginconf

// Headlamp is plugins.headlamp: deep links into the Kubernetes UI.
type Headlamp struct {
	PluginBase
	URL     string `json:"url" dash:"url" doc:"Base URL of Headlamp, e.g. https://headlamp.example.com. Empty turns the deep links off."`
	Cluster string `json:"cluster" doc:"Name of the cluster in Headlamp's URLs."`
}

// DefaultHeadlamp is the section with nothing set.
func DefaultHeadlamp() Headlamp { return Headlamp{Cluster: "main"} }
