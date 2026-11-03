package localauth

import (
	"container/list"
	"sync"
	"time"
)

// The login backoff (contract §4.4 Login, G4-R2-S9).
const (
	// MaxFailures is how many failures a key may have before attempts are refused.
	MaxFailures = 5
	// FirstBackoff is the first refusal window after MaxFailures; each further failure after a
	// window has passed doubles it, up to MaxBackoff.
	FirstBackoff = time.Second
	MaxBackoff   = 15 * time.Minute
	// MaxPeers caps the per-peer table; the oldest key is evicted first.
	MaxPeers = 4096
)

// Throttle is the login backoff. There are two kinds of key: the TCP peer, and the user, where
// only the configured account has a key of its own and every other attempted name shares one
// bucket (so cycling names cannot evict the account's counter from a capped table). A refused
// attempt is neither hashed nor counted. Safe for concurrent use.
type Throttle struct {
	account string
	now     func() time.Time

	mu      sync.Mutex
	peers   map[string]*list.Element // of *peerEntry; front = most recently failed
	order   *list.List
	user    backoff // the configured account
	unknown backoff // every other attempted name
}

// backoff is one key's failure count and refusal window.
type backoff struct {
	fails  int
	window time.Duration // the last refusal window, 0 before MaxFailures
	until  time.Time     // attempts are refused before it
}

type peerEntry struct {
	peer string
	b    backoff
}

// NewThrottle returns the throttle of the local account named account, on the clock now (nil =
// time.Now).
func NewThrottle(account string, now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	return &Throttle{account: account, now: now, peers: make(map[string]*list.Element), order: list.New()}
}

// Allow reports whether an attempt from peer for user may be tried now; when not, retryAfter is
// the time until it may (the Retry-After of the 429). An empty user checks the peer key alone
// (the setup token, which names no user).
func (t *Throttle) Allow(peer, user string) (retryAfter time.Duration, ok bool) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	if e, found := t.peers[peer]; found {
		wait = max(wait, e.Value.(*peerEntry).b.wait(now))
	}
	if b := t.userKey(user); b != nil {
		wait = max(wait, b.wait(now))
	}
	return wait, wait == 0
}

// Fail counts a failed attempt against peer and user (the peer alone when user is empty).
func (t *Throttle) Fail(peer, user string) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	e, found := t.peers[peer]
	if found {
		t.order.MoveToFront(e)
	} else {
		if t.order.Len() >= MaxPeers {
			oldest := t.order.Back()
			t.order.Remove(oldest)
			delete(t.peers, oldest.Value.(*peerEntry).peer)
		}
		e = t.order.PushFront(&peerEntry{peer: peer})
		t.peers[peer] = e
	}
	e.Value.(*peerEntry).b.fail(now)
	if b := t.userKey(user); b != nil {
		b.fail(now)
	}
}

// Succeed clears the keys of peer and user after a successful login.
func (t *Throttle) Succeed(peer, user string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, found := t.peers[peer]; found {
		t.order.Remove(e)
		delete(t.peers, peer)
	}
	if b := t.userKey(user); b != nil {
		*b = backoff{}
	}
}

// userKey is the user's bucket: the account's own, the shared one of every other name, or nil for
// no user.
func (t *Throttle) userKey(user string) *backoff {
	switch user {
	case "":
		return nil
	case t.account:
		return &t.user
	}
	return &t.unknown
}

// wait is how long attempts are still refused.
func (b *backoff) wait(now time.Time) time.Duration {
	if b.fails < MaxFailures || !now.Before(b.until) {
		return 0
	}
	return b.until.Sub(now)
}

// fail counts a failure; from the MaxFailures-th on it opens a refusal window, FirstBackoff and
// then twice the previous one, up to MaxBackoff.
func (b *backoff) fail(now time.Time) {
	b.fails++
	if b.fails < MaxFailures {
		return
	}
	if b.window == 0 {
		b.window = FirstBackoff
	} else {
		b.window = min(2*b.window, MaxBackoff)
	}
	b.until = now.Add(b.window)
}
