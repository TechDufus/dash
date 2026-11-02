package kubernetes

import (
	"net/http"

	"github.com/TechDufus/dash/internal/upstream"
)

// APIServerTransport is what cmd/dash's transport hook asks the plugin's service for, per request
// (Supervisor.Service("kubernetes"), which is nil while the plugin is not running):
// with plugins.kubernetes.upstreamViaAPIServer a wrap that sends requests for in-cluster service
// URLs through the API server's service proxy with the plugin's credentials, and nil otherwise
// (the hook then leaves the transport alone). The wrap is built on first use and kept for the
// run. A wrap that cannot be built is tried again by the next request, and its error is logged
// once per change, not once per request.
func (s *service) APIServerTransport() func(http.RoundTripper) http.RoundTripper {
	r := s.r
	if !r.cfg.UpstreamViaAPIServer || r.conn.REST == nil {
		return nil
	}
	r.mu.Lock()
	wrap := r.wrap
	if wrap == nil {
		var err error
		if wrap, err = upstream.DevWrap(r.conn.REST); err == nil {
			r.wrap = wrap
		}
		r.mu.Unlock()
		if err != nil {
			msg := r.h.ScrubLog(err.Error())
			if r.changed("apiserver-transport", msg) {
				r.h.Logger().Error("cannot route upstream requests through the API server", "error", msg)
			}
			return nil
		}
		return wrap
	}
	r.mu.Unlock()
	return wrap
}
