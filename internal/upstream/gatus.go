package upstream

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

const gatusPageSize = 30

// Gatus reads endpoint statuses from the Gatus API.
type Gatus struct{ c client }

func NewGatus(baseURL string, rt http.RoundTripper) *Gatus {
	return &Gatus{c: newClient(baseURL, rt, 5*time.Second)}
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

// Statuses lists every endpoint with its recent results (oldest first).
// Gatus does not return the configured endpoint URL, so URL carries the host
// that the latest result probed (a bare hostname or IP, no scheme).
func (g *Gatus) Statuses(ctx context.Context) ([]model.Endpoint, error) {
	out := []model.Endpoint{}
	for page := 1; page <= 10; page++ {
		q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(gatusPageSize)}}
		var raw []gatusStatus
		if err := g.c.getJSON(ctx, http.MethodGet, "/api/v1/endpoints/statuses", q, nil, nil, &raw); err != nil {
			return nil, err
		}
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
		if len(raw) < gatusPageSize {
			break
		}
	}
	return out, nil
}
