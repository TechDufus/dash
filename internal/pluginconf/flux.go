package pluginconf

// Flux is plugins.flux: how dash reads Flux objects.
type Flux struct {
	PluginBase
	// SuspendReasonAnnotation on a Flux object is shown as the reason it is suspended.
	SuspendReasonAnnotation string `json:"suspendReasonAnnotation" doc:"Flux object annotation whose value is shown as the reason the object is suspended. Empty hides the reason."`
}
