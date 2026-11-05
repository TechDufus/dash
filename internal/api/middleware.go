package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

type userKey struct{}

type principalKey struct{}

// userFrom returns the authenticated user stored by authn.
func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
}

// principalFrom returns the principal authn stored: who the request acts as and with which
// role. A request that did not pass authn has none and gets the zero Principal, whose role
// grants nothing.
func principalFrom(ctx context.Context) auth.Principal {
	p, _ := ctx.Value(principalKey{}).(auth.Principal)
	return p
}

// hostWarnEvery spaces the log lines about requests for unknown hosts.
const hostWarnEvery = 10 * time.Second

// hosts answers 421 to requests addressed to a Host name dash is not configured for, which
// stops DNS-rebinding: a hostile page whose name resolves to dash's address cannot read
// responses from it.
func (s *Server) hosts(next http.Handler) http.Handler {
	allowed := s.d.Cfg.HostMatcher()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r.Host) {
			now, last := s.now().UnixNano(), s.hostWarn.Load()
			if now-last >= int64(hostWarnEvery) && s.hostWarn.CompareAndSwap(last, now) {
				host, _ := s.d.Secrets.Cut(r.Host, 100)
				slog.Warn("rejected request for unknown host", "host", host, "requestId", requestIDFrom(r.Context()))
			}
			writeErr(w, http.StatusMisdirectedRequest, model.ErrCodeUnknownHost, "unknown host; set server.publicURL or server.allowedHosts")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf requires the custom header and the public Origin on every state-changing request.
func (s *Server) csrf(next http.Handler) http.Handler {
	origin := s.d.Cfg.PublicOrigin()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("X-Dash-Csrf") != "1" || r.Header.Get("Origin") != origin {
				s.deny(r, obs.AuthCSRF)
				writeErr(w, http.StatusForbidden, model.ErrCodeCSRFFailed, "csrf check failed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
