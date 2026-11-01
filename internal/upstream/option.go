package upstream

import (
	"net/http"
	"time"
)

// Option adjusts how a client's HTTP client is built. A plugin gets its transport from
// Host.HTTPClient, which also decides the timeout and the redirect policy; passing only the
// transport would lose both, so the plugin passes them as options.
type Option func(*clientConfig)

type clientConfig struct {
	timeout       time.Duration
	checkRedirect func(req *http.Request, via []*http.Request) error
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
