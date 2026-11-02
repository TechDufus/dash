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
// run.
func (s *service) APIServerTransport() func(http.RoundTripper) http.RoundTripper {
	r := s.r
	if !r.cfg.UpstreamViaAPIServer || r.conn.REST == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wrap == nil {
		wrap, err := upstream.DevWrap(r.conn.REST)
		if err != nil {
			r.h.Logger().Error("cannot route upstream requests through the API server", "error", r.h.ScrubLog(err.Error()))
			return nil
		}
		r.wrap = wrap
	}
	return r.wrap
}
