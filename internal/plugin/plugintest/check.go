package plugintest

import (
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
)

// CheckEnv is a plugin.CheckEnv for connection-check tests. HTTPClient records every HTTPConfig
// it is asked for and builds a plain client that applies Timeout (plugin.DefaultHTTPTimeout when
// zero) and Authorize, the latter to a copy of every request as the real client does. It has no
// dial guard: a test's upstream is an httptest server on loopback. Transport, when set, is the
// RoundTripper under Authorize (http.DefaultTransport otherwise); Clock is Now (time.Now when
// nil). The zero value is ready to use.
type CheckEnv struct {
	Clock     func() time.Time
	Transport http.RoundTripper

	mu      sync.Mutex
	configs []plugin.HTTPConfig
}

var _ plugin.CheckEnv = (*CheckEnv)(nil)

// HTTPClient implements plugin.CheckEnv.
func (e *CheckEnv) HTTPClient(c plugin.HTTPConfig) (*http.Client, error) {
	e.mu.Lock()
	e.configs = append(e.configs, c)
	e.mu.Unlock()
	rt := e.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	if c.Authorize != nil {
		rt = authorizing{rt: rt, authorize: c.Authorize}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = plugin.DefaultHTTPTimeout
	}
	return &http.Client{Transport: rt, Timeout: timeout}, nil
}

// Now implements plugin.CheckEnv.
func (e *CheckEnv) Now() time.Time {
	if e.Clock != nil {
		return e.Clock()
	}
	return time.Now()
}

// Configs are the HTTPConfigs HTTPClient was asked for, in order.
func (e *CheckEnv) Configs() []plugin.HTTPConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.configs)
}

// authorizing applies authorize to a copy of every request.
type authorizing struct {
	rt        http.RoundTripper
	authorize func(*http.Request)
}

func (a authorizing) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	a.authorize(req)
	return a.rt.RoundTrip(req)
}
