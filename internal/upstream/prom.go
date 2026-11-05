package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// Sample is one instant-vector element. It is svc.Sample, so *Prom satisfies svc.Querier.
type Sample = svc.Sample

// Prom queries a Prometheus-compatible API (VictoriaMetrics).
type Prom struct{ c client }

var _ svc.Querier = (*Prom)(nil)

// NewProm builds the client for baseURL; an empty URL gives a client that answers ErrDisabled.
// Its requests time out after 5s unless opts say otherwise (Timeout).
func NewProm(baseURL string, rt http.RoundTripper, opts ...Option) *Prom {
	return &Prom{c: newClient(baseURL, rt, 5*time.Second, opts...)}
}

type promResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string    `json:"metric"`
			Value  [2]json.RawMessage   `json:"value"`
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func (p *Prom) query(ctx context.Context, path string, form url.Values, wantType string) (*promResponse, error) {
	hdr := readOnlyPost("application/x-www-form-urlencoded")
	var r promResponse
	if err := p.c.getJSON(ctx, http.MethodPost, path, nil, strings.NewReader(form.Encode()), hdr, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		msg, _ := p.c.cut(r.Error, maxErrorText)
		return nil, fmt.Errorf("%w: query failed: %s %s", ErrInvalidResponse, r.ErrorType, msg)
	}
	if r.Data.ResultType != wantType {
		return nil, fmt.Errorf("%w: unexpected result type %q, want %q", ErrInvalidResponse, r.Data.ResultType, wantType)
	}
	return &r, nil
}

// Instant runs an instant query. Samples whose value is NaN or Inf are dropped
// (they cannot be serialised as JSON).
func (p *Prom) Instant(ctx context.Context, expr string) ([]Sample, error) {
	r, err := p.query(ctx, "/api/v1/query", url.Values{"query": {expr}}, "vector")
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(r.Data.Result))
	for _, s := range r.Data.Result {
		_, v, ok := parsePoint(s.Value)
		if !ok {
			continue
		}
		out = append(out, Sample{Labels: nonNilLabels(s.Metric), Value: v})
	}
	return out, nil
}

// Range runs a range query; NaN/Inf points are skipped.
func (p *Prom) Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error) {
	form := url.Values{
		"query": {expr},
		"start": {unixSeconds(start)},
		"end":   {unixSeconds(end)},
		"step":  {strconv.FormatFloat(step.Seconds(), 'f', -1, 64)},
	}
	r, err := p.query(ctx, "/api/v1/query_range", form, "matrix")
	if err != nil {
		return nil, err
	}
	out := make([]model.SeriesLine, 0, len(r.Data.Result))
	for _, s := range r.Data.Result {
		line := model.SeriesLine{Labels: nonNilLabels(s.Metric), Points: make([][2]float64, 0, len(s.Values))}
		for _, raw := range s.Values {
			if ts, v, ok := parsePoint(raw); ok {
				line.Points = append(line.Points, [2]float64{ts, v})
			}
		}
		out = append(out, line)
	}
	return out, nil
}

func unixSeconds(t time.Time) string {
	return strconv.FormatFloat(float64(t.UnixMilli())/1000, 'f', -1, 64)
}

func nonNilLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// parsePoint decodes a [unixSeconds, "value"] pair; ok is false for malformed
// pairs and non-finite values.
func parsePoint(raw [2]json.RawMessage) (ts, v float64, ok bool) {
	var vs string
	if json.Unmarshal(raw[0], &ts) != nil || json.Unmarshal(raw[1], &vs) != nil {
		return 0, 0, false
	}
	v, err := strconv.ParseFloat(vs, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, 0, false
	}
	return ts, v, true
}
