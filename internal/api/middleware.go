package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type userKey struct{}

// userFrom returns the authenticated user stored by auth.
func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
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
				host := r.Host
				if len(host) > 100 {
					host = host[:100]
				}
				slog.Warn("rejected request for unknown host", "host", host)
			}
			writeErr(w, http.StatusMisdirectedRequest, "unknown host; set DASH_PUBLIC_URL or DASH_ALLOWED_HOSTS")
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
				writeErr(w, http.StatusForbidden, "csrf check failed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// auth resolves the user behind oauth2-proxy for every /api request.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		user := strings.TrimSpace(r.Header.Get("X-Forwarded-Preferred-Username"))
		if user == "" {
			user = strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
		}
		if user == "" && s.d.Cfg.Dev {
			user = "dev"
		}
		if user == "" {
			writeErr(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}
