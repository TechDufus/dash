package upstream

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// defaultGatusTimeout is the whole-request timeout of a Gatus client unless a Timeout option
// replaces it.
const defaultGatusTimeout = 5 * time.Second

// Gatus reads endpoint statuses from the Gatus API.
type Gatus struct {
	c        client
	pageSize int
}

// NewGatus returns a client for the Gatus at baseURL. pageSize is how many results Gatus returns
// per endpoint (its pageSize query parameter); a value below 1 asks for one result.
func NewGatus(baseURL string, rt http.RoundTripper, pageSize int, opts ...Option) *Gatus {
	return &Gatus{c: newClient(baseURL, rt, defaultGatusTimeout, opts...), pageSize: max(pageSize, 1)}
}

type gatusStatus struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Key     string `json:"key"`
	Results []struct {
		Hostname  string    `json:"hostname"`
		Duration  int64     `json:"duration"` // nanoseconds
		Success   bool      `json:"success"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"results"`
}

// Statuses lists every endpoint with its recent results (oldest first). Gatus pages each
// endpoint's results (not the endpoint list), so one request returns all endpoints with up
// to pageSize results each. Gatus does not return the configured endpoint URL, so URL
// carries the host that the latest result probed (a bare hostname or IP, no scheme).
func (g *Gatus) Statuses(ctx context.Context) ([]model.Endpoint, error) {
	q := url.Values{"page": {"1"}, "pageSize": {strconv.Itoa(g.pageSize)}}
	var raw []gatusStatus
	if err := g.c.getJSON(ctx, http.MethodGet, "/api/v1/endpoints/statuses", q, nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]model.Endpoint, 0, len(raw))
	for _, r := range raw {
		e := model.Endpoint{Group: r.Group, Name: r.Name, Key: r.Key, Results: make([]model.CheckResult, 0, len(r.Results))}
		for _, res := range r.Results {
			e.Results = append(e.Results, model.CheckResult{
				Success: res.Success, Timestamp: res.Timestamp, DurationMs: float64(res.Duration) / 1e6,
			})
		}
		if n := len(r.Results); n > 0 {
			e.Healthy = r.Results[n-1].Success
			e.URL = r.Results[n-1].Hostname
		}
		out = append(out, e)
	}
	return out, nil
}
