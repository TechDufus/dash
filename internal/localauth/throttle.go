package localauth

import "time"

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
type Throttle struct{}

// NewThrottle returns the throttle of the local account named account, on the clock now (nil =
// time.Now).
func NewThrottle(account string, now func() time.Time) *Throttle { return &Throttle{} }

// Allow reports whether an attempt from peer for user may be tried now; when not, retryAfter is
// the time until it may (the Retry-After of the 429). An empty user checks the peer key alone
// (the setup token, which names no user).
func (t *Throttle) Allow(peer, user string) (retryAfter time.Duration, ok bool) { return 0, false }

// Fail counts a failed attempt against peer and user (the peer alone when user is empty).
func (t *Throttle) Fail(peer, user string) {}

// Succeed clears the keys of peer and user after a successful login.
func (t *Throttle) Succeed(peer, user string) {}
