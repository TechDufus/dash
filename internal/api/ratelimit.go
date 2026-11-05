package api

import (
	"container/list"
	"sync"
	"time"
)

// The action rate limit (design A.8 step 5): a principal may start this many plugin actions
// in any window. Constants until the layered config makes them settings.
const (
	actionRateLimit  = 10
	actionRateWindow = time.Minute
	maxRateKeys      = 4096
)

// ActionRateLimit is the action rate limit: how many plugin actions one user may start in any
// window, across every plugin. The generated plugin docs state it.
func ActionRateLimit() (n int, window time.Duration) { return actionRateLimit, actionRateWindow }

// The stream-open audit records (design F.5): a tail is audited at every open, but a client chooses
// what it opens, so the log keeps at most one record per user and target a minute, and a user's
// opens as a whole are capped too. A refused record never refuses the stream.
const (
	openRecordWindow  = time.Minute
	openRecordPerUser = 30
)

// The connection tests of the settings API (design D17): a principal may run this many in any
// window, so the button cannot turn dash into a scanner of the network it can reach.
const (
	settingsTestRateLimit  = 10
	settingsTestRateWindow = time.Minute
)

// rateLimiter is a sliding-window limiter: at most limit events per key in any window. It
// holds at most maxKeys keys; the keys are authenticated users, so a full table means the
// identity provider hands out thousands of names, not that a client is flooding it, and a
// new key is then refused rather than evicting a key someone is rate limited under.
type rateLimiter struct {
	limit   int
	window  time.Duration
	maxKeys int

	mu    sync.Mutex
	hits  map[string][]time.Time // oldest first, all within window of the last allow
	sweep time.Time              // last full sweep of expired keys
}

func newRateLimiter(limit int, window time.Duration, maxKeys int) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, maxKeys: maxKeys, hits: map[string][]time.Time{}}
}

// allow records an event of key at now and reports whether it is within the limit. When it is
// not, nothing is recorded and retry is how long until the oldest event leaves the window.
func (l *rateLimiter) allow(key string, now time.Time) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.window)
	h, known := l.hits[key]
	if !known && len(l.hits) >= l.maxKeys {
		l.dropExpired(now, cutoff)
		if len(l.hits) >= l.maxKeys {
			return false, l.window
		}
	}
	i := 0
	for i < len(h) && !h[i].After(cutoff) {
		i++
	}
	h = h[i:]
	if len(h) >= l.limit {
		l.hits[key] = h
		return false, max(h[0].Sub(cutoff), time.Second)
	}
	l.hits[key] = append(h, now)
	return true, 0
}

// dropExpired deletes the keys with no event in the window. It runs only when the table is
// full, and at most once per window-eighth so a full table of live keys is not rescanned by
// every request.
func (l *rateLimiter) dropExpired(now, cutoff time.Time) {
	if now.Sub(l.sweep) < l.window/8 {
		return
	}
	l.sweep = now
	for k, h := range l.hits {
		if len(h) == 0 || !h[len(h)-1].After(cutoff) {
			delete(l.hits, k)
		}
	}
}

// evictingLimiter is a sliding-window limiter like rateLimiter whose keys anyone can choose (the
// OAuth endpoints, contract G7-R3 fix A1): a full table never refuses a new key, it forgets the
// key used longest ago, which is an expired one when there is any. A forgotten key starts over,
// so a flood of new keys can only give a key more room, never take a key's room away.
type evictingLimiter struct {
	limit   int
	window  time.Duration
	maxKeys int

	mu    sync.Mutex
	keys  map[string]*list.Element
	order *list.List // of *limitedKey, the key used longest ago first
}

type limitedKey struct {
	key  string
	hits []time.Time // oldest first
}

func newEvictingLimiter(limit int, window time.Duration, maxKeys int) *evictingLimiter {
	return &evictingLimiter{limit: limit, window: window, maxKeys: maxKeys, keys: map[string]*list.Element{}, order: list.New()}
}

// allow is rateLimiter.allow without the refusal of a full table.
func (l *evictingLimiter) allow(key string, now time.Time) (ok bool, retry time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, known := l.keys[key]
	if !known {
		for len(l.keys) >= l.maxKeys {
			oldest := l.order.Front()
			delete(l.keys, oldest.Value.(*limitedKey).key)
			l.order.Remove(oldest)
		}
		e = l.order.PushBack(&limitedKey{key: key})
		l.keys[key] = e
	}
	l.order.MoveToBack(e)
	k := e.Value.(*limitedKey)
	cutoff := now.Add(-l.window)
	i := 0
	for i < len(k.hits) && !k.hits[i].After(cutoff) {
		i++
	}
	k.hits = k.hits[i:]
	if len(k.hits) >= l.limit {
		return false, max(k.hits[0].Sub(cutoff), time.Second)
	}
	k.hits = append(k.hits, now)
	return true, 0
}
