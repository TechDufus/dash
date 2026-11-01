package api

import (
	"cmp"
	"net/http"
	"sync"

	"github.com/TechDufus/dash/internal/config"
)

// withDefaultLimits fills each zero field of l from config.Defaults().Limits. A zero cap would
// refuse every stream (acquire's 0 >= 0), so a Server built from a partial Config must not
// inherit it; per field, so one explicit cap does not switch the others off.
func withDefaultLimits(l config.Limits) config.Limits {
	d := config.Defaults().Limits
	return config.Limits{
		StreamsPerUser:  cmp.Or(l.StreamsPerUser, d.StreamsPerUser),
		Streams:         cmp.Or(l.Streams, d.Streams),
		TailsPerUser:    cmp.Or(l.TailsPerUser, d.TailsPerUser),
		Tails:           cmp.Or(l.Tails, d.Tails),
		TailMaxDuration: cmp.Or(l.TailMaxDuration, d.TailMaxDuration),
		SSEWriteTimeout: cmp.Or(l.SSEWriteTimeout, d.SSEWriteTimeout),
		BodyReadTimeout: cmp.Or(l.BodyReadTimeout, d.BodyReadTimeout),
		MaxHeaderBytes:  cmp.Or(l.MaxHeaderBytes, d.MaxHeaderBytes),
	}
}

// streamLimiter caps concurrent long-lived streams per user and in total.
type streamLimiter struct {
	mu      sync.Mutex
	perUser int
	max     int
	total   int
	byUser  map[string]int
}

func newStreamLimiter(perUser, max int) *streamLimiter {
	return &streamLimiter{perUser: perUser, max: max, byUser: map[string]int{}}
}

// acquire reserves a slot for user. When none is free it answers 429 (user at its cap) or
// 503 (server at its cap) itself and reports false; otherwise the caller must call release.
func (l *streamLimiter) acquire(w http.ResponseWriter, user string) (release func(), ok bool) {
	l.mu.Lock()
	switch {
	case l.byUser[user] >= l.perUser:
		l.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeErr(w, http.StatusTooManyRequests, "too many open streams for this user")
		return nil, false
	case l.total >= l.max:
		l.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeErr(w, http.StatusServiceUnavailable, "too many open streams")
		return nil, false
	}
	l.byUser[user]++
	l.total++
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if l.byUser[user]--; l.byUser[user] <= 0 {
				delete(l.byUser, user)
			}
		})
	}, true
}
