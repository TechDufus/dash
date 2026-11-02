package api

import (
	"net"
	"net/http"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// maxAuditAction caps the request line recorded for a denial: it comes from the client.
const maxAuditAction = 200

// authMode names how requests are authenticated, for the audit log.
func (s *Server) authMode() string {
	switch {
	case s.d.Cfg.Demo:
		return "demo"
	case s.d.Cfg.Dev:
		return "dev"
	}
	return "proxy"
}

// peerOf is the host of the TCP peer: behind oauth2-proxy that is the proxy, which is what a
// denial rate limit can key on without trusting a header.
func peerOf(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// auditRecord names the user authn stored; a refusal by authn itself, or by the host check,
// has none.
func (s *Server) auditRecord(r *http.Request, action string, t audit.Target) audit.Record {
	user := userFrom(r.Context())
	return audit.Record{
		Action: action, Target: t, Peer: peerOf(r), AuthMode: s.authMode(), RequestID: requestIDFrom(r.Context()),
		Actor: audit.Actor{Type: s.authMode(), User: user, Surface: "ui"},
	}
}

func (s *Server) beginRecord(w http.ResponseWriter, rec audit.Record) (audit.Pending, bool) {
	p, err := s.d.Audit.Intent(rec)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable, "audit log unavailable")
		return audit.Pending{}, false
	}
	return p, true
}

// deny records a refused request and counts it; the log keeps at most one record per user (per
// peer when there is no user) and reason a minute, so a flood of refusals cannot fill the disk.
// The reason is a value of obs.AuthReason: a reason the metric does not know cannot be passed.
func (s *Server) deny(r *http.Request, reason obs.AuthReason) {
	s.d.Metrics.AuthFailure(reason)
	s.refuse(r, reason.String())
}
