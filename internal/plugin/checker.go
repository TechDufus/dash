package plugin

import (
	"context"
	"net/http"
	"time"
)

// CheckTimeout bounds one connection check (design A.3): Supervisor.Check gives the Checker a
// context that ends after it, and a Checker asks CheckEnv.HTTPClient for a client with this
// Timeout.
const CheckTimeout = 10 * time.Second

// Checker is a plugin that can test a candidate configuration against its upstream: one cheap
// read, never a write. Like the other optional interfaces it is detected with a type
// assertion; a plugin without it has no connection check.
//
// Check is called on a fresh Plugin from the Factory, which is never started: it builds
// everything it needs from cfg and env. cfg is a pointer of the type Manifest.Config returns,
// already accepted by the schema and by Validate. The report names categories only, never an
// upstream URL, host, header or body.
type Checker interface {
	Check(ctx context.Context, cfg any, env CheckEnv) CheckReport
}

// CheckEnv is what the core gives a Checker.
type CheckEnv interface {
	// HTTPClient builds the client as Host.HTTPClient does (TLS and CA, credentials per
	// request, the dial guard, redirects limited to the same host, no proxy from the
	// environment), with HTTPConfig.FromUI forced on when the candidate came from the UI.
	HTTPClient(c HTTPConfig) (*http.Client, error)
	// Now is the clock a Checker measures latency with.
	Now() time.Time
}

// CheckReport is the outcome of one connection check: one result per probe, in the order they
// ran.
type CheckReport struct {
	Checks []CheckResult
}

// CheckResult is one probe of a check. Category is empty when OK, and otherwise one of the
// categories of internal/upstream ("unreachable", "timeout", "tls", "auth",
// "invalid_response", "http_<code>"). Message is fixed text for the category.
type CheckResult struct {
	Name      string
	OK        bool
	Category  string
	Message   string
	LatencyMs int
}
