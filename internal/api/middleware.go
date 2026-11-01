package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

type userKey struct{}

type principalKey struct{}

// userFrom returns the authenticated user stored by auth.
func userFrom(ctx context.Context) string {
	u, _ := ctx.Value(userKey{}).(string)
	return u
}

// principalFrom returns the principal auth stored: who the request acts as and with which
// role. A request that did not pass auth has none and gets the zero Principal, whose role
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
				host := r.Host
				if len(host) > 100 {
					host = host[:100]
				}
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
				s.deny(r, "csrf")
				writeErr(w, http.StatusForbidden, model.ErrCodeCSRFFailed, "csrf check failed")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// proxyUser is the user oauth2-proxy forwarded, or "" when it sent none.
func proxyUser(r *http.Request) string {
	if u := strings.TrimSpace(r.Header.Get("X-Forwarded-Preferred-Username")); u != "" {
		return u
	}
	return strings.TrimSpace(r.Header.Get("X-Forwarded-User"))
}

// requestUser is the user a request acts as before auth has run: the forwarded user, or "dev"
// in dev mode. csrf runs ahead of auth, and its denials still name who was refused.
func (s *Server) requestUser(r *http.Request) string {
	if u := proxyUser(r); u != "" {
		return u
	}
	if s.d.Cfg.Dev {
		return "dev"
	}
	return ""
}

// auth resolves the user behind oauth2-proxy for every /api request.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" && !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		user := s.requestUser(r)
		if user == "" {
			s.deny(r, "unauthenticated")
			writeErr(w, http.StatusUnauthorized, model.ErrCodeUnauthenticated, "unauthenticated")
			return
		}
		// G2 gives every authenticated principal the admin role, as before (design E.1); P4.2
		// replaces this with the role mapping.
		p := auth.Principal{Kind: auth.Kind(s.authMode()), User: user, Role: auth.RoleAdmin}
		ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, user), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
