package api

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/tokens"
)

// TokenResolver turns a bearer credential into a token principal at an audience (api or mcp):
// internal/tokens.Resolver. Every refusal is an error, answered 401 invalid_token.
type TokenResolver interface {
	Resolve(token, audience string) (auth.Principal, error)
}

// audienceFor is the token audience of a request path (design M.4): /api is api, /mcp is mcp,
// anything else none, where every token is refused.
func audienceFor(path string) string {
	switch {
	case path == "/api" || strings.HasPrefix(path, "/api/"):
		return "api"
	case path == "/mcp" || strings.HasPrefix(path, "/mcp/"):
		return "mcp"
	}
	return ""
}

// hasSessionCookie reports whether r carries dash's own session cookie, under either of its
// names. An identity proxy's cookie is not dash's and does not count.
func hasSessionCookie(r *http.Request) bool {
	return len(r.CookiesNamed(session.CookieName(true))) > 0 || len(r.CookiesNamed(session.CookieName(false))) > 0
}

// bearer is the first step of authentication (design M.1, contract D2): a request with an
// Authorization: Bearer credential is a token principal or nothing. With dash's session cookie
// as well it is 400 ambiguous_credentials; a token that does not resolve at the path's audience
// is 401 invalid_token. ok is false when the request has no bearer credential or the process
// resolves no tokens (auth.mode=none, whose authenticator ignores the header).
func (s *Server) bearer(r *http.Request) (p auth.Principal, fail *auth.Failure, ok bool) {
	if s.d.Tokens == nil {
		return auth.Principal{}, nil, false
	}
	token, present := auth.Bearer(r)
	if !present {
		return auth.Principal{}, nil, false
	}
	if hasSessionCookie(r) {
		return auth.Principal{}, &auth.Failure{Status: http.StatusBadRequest, Code: auth.CodeAmbiguousCredentials, Reason: auth.ReasonAmbiguousCredentials}, true
	}
	p, err := s.d.Tokens.Resolve(token, audienceFor(r.URL.Path))
	if err != nil {
		fail := &auth.Failure{Status: http.StatusUnauthorized, Code: auth.CodeInvalidToken, Reason: auth.ReasonInvalidToken}
		// A valid token at the other surface says so (it names only what its holder knows);
		// every other refusal is the same "invalid token".
		if ae, ok := errors.AsType[*tokens.AudienceError](err); ok {
			fail.Message = ae.Error()
		}
		return auth.Principal{}, fail, true
	}
	return p, nil, true
}

// wwwAuthenticate is the challenge of a 401 at path (design M.1): where an OAuth client learns
// how to authenticate at that surface. The documents themselves arrive with the OAuth server.
func (s *Server) wwwAuthenticate(path string) string {
	aud := audienceFor(path)
	if aud == "" {
		aud = "api"
	}
	return `Bearer resource_metadata="` + strings.TrimSuffix(s.d.Cfg.PublicURL, "/") + `/.well-known/oauth-protected-resource/` + aud + `"`
}

// The rate limit of an api token (design M.5, contract D10): a bucket of tokenBurst requests
// that refills at tokenRate a minute.
const (
	tokenRate  = 300
	tokenBurst = 300
)

// tokenBucket is a token-bucket limiter per key: two numbers per key, however many requests a
// minute it admits. Like rateLimiter it holds at most maxKeys keys and refuses a new key when
// full of live ones; the keys are token IDs, which only an administrator or the store creates.
type tokenBucket struct {
	rate    float64 // per second
	burst   float64
	maxKeys int

	mu      sync.Mutex
	buckets map[string]bucket
	sweep   time.Time
}

type bucket struct {
	level float64
	at    time.Time
}

func newTokenBucket(perMinute, burst, maxKeys int) *tokenBucket {
	return &tokenBucket{rate: float64(perMinute) / 60, burst: float64(burst), maxKeys: maxKeys, buckets: map[string]bucket{}}
}

// allow takes one request from key's bucket at now; when it is empty, retry is how long until
// one is back.
func (l *tokenBucket) allow(key string, now time.Time) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, known := l.buckets[key]
	if !known {
		if len(l.buckets) >= l.maxKeys {
			l.dropFull(now)
			if len(l.buckets) >= l.maxKeys {
				return false, time.Minute
			}
		}
		b = bucket{level: l.burst, at: now}
	}
	if d := now.Sub(b.at); d > 0 {
		b.level = math.Min(l.burst, b.level+d.Seconds()*l.rate)
		b.at = now
	}
	if b.level < 1 {
		l.buckets[key] = b
		return false, max(time.Duration((1-b.level)/l.rate*float64(time.Second)), time.Second)
	}
	b.level--
	l.buckets[key] = b
	return true, 0
}

// dropFull forgets the buckets that have refilled: they hold nothing a new bucket would not.
// It runs only when the table is full, at most every few seconds.
func (l *tokenBucket) dropFull(now time.Time) {
	if now.Sub(l.sweep) < 5*time.Second {
		return
	}
	l.sweep = now
	for k, b := range l.buckets {
		if b.level+now.Sub(b.at).Seconds()*l.rate >= l.burst {
			delete(l.buckets, k)
		}
	}
}
