package upstream

import (
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/secret"
)

// Option adjusts how a client's HTTP client is built. A plugin gets its transport from
// Host.HTTPClient, which also decides the timeout and the redirect policy; passing only the
// transport would lose both, so the plugin passes them as options.
type Option func(*clientConfig)

// cutFunc cuts text an upstream sent to n runes (Cut).
type cutFunc = func(s string, n int) (string, bool)

// shapeCut is the cut of a client without the Cut option: the token shapes that straddle the
// cut are redacted whole, no registered secret is known.
var shapeCut cutFunc = (*secret.Registry)(nil).CutRunes

type clientConfig struct {
	timeout       time.Duration
	checkRedirect func(req *http.Request, via []*http.Request) error
	cut           cutFunc
}

// Timeout sets the whole-request timeout of the client's short requests, replacing the client's
// default. A streaming request is not bound by it: it ends with its context.
func Timeout(d time.Duration) Option {
	return func(c *clientConfig) { c.timeout = d }
}

// CheckRedirect sets the redirect policy, typically Host.HTTPClient's own (same host only, the
// dial guard's address rules); without it net/http's default policy applies.
func CheckRedirect(fn func(req *http.Request, via []*http.Request) error) Option {
	return func(c *clientConfig) { c.checkRedirect = fn }
}

// Cut sets how the client cuts the text an upstream sent (an error body, a query's error, a log
// message) before a log or a response shows it, typically Host.CutLog: a registered secret or a
// token that straddles the cut is redacted whole, since a scrubber that sees only its first part
// cannot match it. Without the option only the token shapes are.
func Cut(fn func(s string, n int) (string, bool)) Option {
	return func(c *clientConfig) {
		if fn != nil {
			c.cut = fn
		}
	}
}
