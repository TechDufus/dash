package plugin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/upstream"
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

// maxCheckBody is how much of a 2xx answer a check reads before it closes the body: enough to
// finish a small answer, so a body that breaks off is seen, and never a whole large one.
const maxCheckBody = 64 << 10

// msgBlocked is the message of a read the dial guard refused. Its category stays
// "unreachable".
const msgBlocked = "blocked address"

// CheckMessage is the fixed text of a check result's category ("" is a passing check). It never
// carries an error's text, a URL, a host or a body.
func CheckMessage(category string) string {
	switch category {
	case "":
		return "connected"
	case upstream.CategoryUnreachable:
		return "upstream unreachable"
	case upstream.CategoryTimeout:
		return "request timed out"
	case upstream.CategoryTLS:
		return "TLS or certificate error"
	case upstream.CategoryAuth:
		return "authentication refused"
	case upstream.CategoryInvalidResponse:
		return "invalid response"
	case upstream.CategoryNotConfigured:
		return "not configured"
	}
	if code, ok := strings.CutPrefix(category, "http_"); ok {
		return "upstream answered HTTP " + code
	}
	return "check failed"
}

// CheckNotConfigured is the result of a check that has nothing to probe: a configuration of
// another type, or one without a URL.
func CheckNotConfigured(name string) CheckResult {
	return failedCheck(name, upstream.CategoryNotConfigured, 0)
}

// CheckRead is the one read of a connection check: a GET of rawURL, with hdr, through a client
// env builds from c. A 2xx answer passes; anything else is reduced to a category of
// internal/upstream with its fixed message, and a refusal of the dial guard to "unreachable"
// with the message "blocked address". LatencyMs is measured with env.Now from the moment the
// request is sent until its answer is read. The answer's body is never read beyond a small
// bound and never reported.
func CheckRead(ctx context.Context, env CheckEnv, name string, c HTTPConfig, rawURL string, hdr http.Header) CheckResult {
	hc, err := env.HTTPClient(c)
	if err != nil {
		// Building a client fails only on the TLS settings (an unreadable or empty CA file).
		return failedCheck(name, upstream.CategoryTLS, 0)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return failedCheck(name, upstream.CategoryNotConfigured, 0)
	}
	// A check's client is built for it alone: no keep-alive connection outlives the read.
	req.Close = true
	for k, v := range hdr {
		req.Header[k] = v
	}
	start := env.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return errCheck(name, err, latencyMs(env, start))
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return failedCheck(name, upstream.HTTPCategory(resp.StatusCode), latencyMs(env, start))
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxCheckBody)); err != nil {
		return errCheck(name, err, latencyMs(env, start))
	}
	return CheckResult{Name: name, OK: true, Message: CheckMessage(""), LatencyMs: latencyMs(env, start)}
}

// errCheck is the result of a read that ended with err.
func errCheck(name string, err error, ms int) CheckResult {
	if errors.Is(err, ErrBlockedAddress) {
		return CheckResult{Name: name, Category: upstream.CategoryUnreachable, Message: msgBlocked, LatencyMs: ms}
	}
	return failedCheck(name, upstream.Category(err), ms)
}

func failedCheck(name, category string, ms int) CheckResult {
	return CheckResult{Name: name, Category: category, Message: CheckMessage(category), LatencyMs: ms}
}

func latencyMs(env CheckEnv, start time.Time) int {
	return int(env.Now().Sub(start).Milliseconds())
}
