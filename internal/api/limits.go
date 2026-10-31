package api

import (
	"net/http"
	"sync"
)

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
