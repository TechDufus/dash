// Package upstream holds the HTTP clients for everything dash reads from or
// writes to besides the Kubernetes API: VictoriaMetrics, Alertmanager,
// VictoriaLogs, Gatus and Proxmox.
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	// ErrInvalid wraps input validation failures; the API answers 400.
	ErrInvalid = errors.New("invalid request")
	// ErrDisabled is returned by a client whose source is not configured.
	ErrDisabled = errors.New("source not configured")
)

// StatusError is a non-2xx answer from an upstream.
type StatusError struct {
	Code int
	Body string // at most 200 characters
}

func (e *StatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("upstream answered %d", e.Code)
	}
	return fmt.Sprintf("upstream answered %d: %s", e.Code, e.Body)
}

const maxJSONBody = 32 << 20

type client struct {
	base string
	hc   *http.Client
}

// newClient builds a client for base; def is the default timeout of its requests, which opts may
// replace. A timeout of 0 means none (streaming requests end with their context).
func newClient(base string, rt http.RoundTripper, def time.Duration, opts ...Option) client {
	cfg := clientConfig{timeout: def}
	for _, o := range opts {
		o(&cfg)
	}
	return client{base: strings.TrimRight(base, "/"), hc: &http.Client{Transport: rt, Timeout: cfg.timeout, CheckRedirect: cfg.checkRedirect}}
}

// newStreamClient is newClient for requests that end with their context: no timeout, whatever
// opts say, so a Timeout option never cuts a stream short.
func newStreamClient(base string, rt http.RoundTripper, opts ...Option) client {
	c := newClient(base, rt, 0, opts...)
	c.hc.Timeout = 0
	return c
}

// open sends the request and returns the response with its body still open;
// a non-2xx answer is closed and returned as *StatusError. path must already
// be escaped.
func (c client) open(ctx context.Context, method, path string, q url.Values, body io.Reader, hdr http.Header) (*http.Response, error) {
	if c.base == "" {
		return nil, ErrDisabled
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Code: resp.StatusCode, Body: truncate(strings.TrimSpace(string(b)), 200)}
	}
	return resp, nil
}

// getJSON decodes the 2xx answer body into out (out may be nil to discard).
func (c client) getJSON(ctx context.Context, method, path string, q url.Values, body io.Reader, hdr http.Header, out any) error {
	resp, err := c.open(ctx, method, path, q, body, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxJSONBody))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBody)).Decode(out); err != nil {
		return fmt.Errorf("%w: decode %s: %w", ErrInvalidResponse, path, err)
	}
	return nil
}

// truncate cuts s to at most n runes.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
