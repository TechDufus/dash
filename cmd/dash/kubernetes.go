package main

import (
	"net/http"

	"github.com/TechDufus/dash/internal/plugin"
)

// apiServerTransporter is what the kubernetes plugin's service offers the transport hook.
type apiServerTransporter interface {
	APIServerTransport() func(http.RoundTripper) http.RoundTripper
}

// viaAPIServer sends a plugin's requests through the Kubernetes API server's service proxy when
// plugins.kubernetes.upstreamViaAPIServer is on (a laptop against a remote cluster). It asks the
// kubernetes service on every request, so a plugin that starts before the kubernetes plugin, a
// reload of its settings and a restart of it all take effect without restarting the other
// plugins; with the plugin off, unavailable or the flag off, the request goes as it was.
type viaAPIServer struct {
	next    http.RoundTripper
	service func(plugin.ID) (any, bool)
}

func (v viaAPIServer) RoundTrip(r *http.Request) (*http.Response, error) {
	rt := v.next
	if rt == nil {
		rt = http.DefaultTransport
	}
	if s, ok := v.service("kubernetes"); ok {
		if t, ok := s.(apiServerTransporter); ok {
			if wrap := t.APIServerTransport(); wrap != nil {
				rt = wrap(rt)
			}
		}
	}
	return rt.RoundTrip(r)
}
