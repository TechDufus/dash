package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/svc"
)

func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name, rng := q.Get("name"), q.Get("range")
	args := queries.Args{
		VMID:      q.Get("vmid"),
		Namespace: firstOf(r, "namespace", "ns"),
		Pod:       q.Get("pod"),
		Instance:  q.Get("node"),
	}
	lines, step, f := s.readSeries(r.Context(), name, rng, args)
	if f != nil {
		s.writeFailure(w, f)
		return
	}
	writeJSON(w, http.StatusOK, model.Series{Name: name, Range: rng, StepSeconds: int(step / time.Second), Series: lines})
}

// failure is why a read the API composes failed: the status, code and message of its answer,
// or, with Upstream set, the category-only 502 of an upstream failure (Message is its subject).
type failure struct {
	Status   int
	Code     string
	Message  string
	Upstream error
}

func (s *Server) writeFailure(w http.ResponseWriter, f *failure) {
	if f.Upstream != nil {
		s.badGateway(w, f.Message, f.Upstream)
		return
	}
	writeErr(w, f.Status, f.Code, f.Message)
}

// readSeries reads a named series over rng (15m, 1h, 6h, 24h or 7d), oldest point first, with
// the step it was read at. args.Instance is a node's name, which the series' plugin resolves.
func (s *Server) readSeries(ctx context.Context, name, rng string, args queries.Args) ([]model.SeriesLine, time.Duration, *failure) {
	window, step, ok := queries.Range(rng)
	if !ok {
		return nil, 0, &failure{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "unknown range"}
	}
	// The node is named, not resolved: the series that reads it (a plugin's) turns the name into
	// its instance and answers plugin.ErrNotFound for a node it does not know.
	if args.Instance != "" && !queries.ValidDNS(args.Instance) {
		return nil, 0, &failure{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: "invalid node"}
	}
	expr, err := s.seriesExpr(name, args)
	switch {
	case errors.Is(err, plugin.ErrNotFound):
		return nil, 0, &failure{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: err.Error()}
	case err != nil:
		return nil, 0, &failure{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Message: err.Error()}
	}
	prom, running := s.querier()
	if !running {
		// Everything above is validated: only now does it matter that no prometheus runs.
		return nil, 0, &failure{Status: http.StatusNotFound, Code: model.ErrCodeNotFound, Message: "unknown series"}
	}
	end := s.now()
	if c := s.d.Cfg.DemoClock; !c.IsZero() {
		end = c // demo mode's fixed clock (DASH_DEMO_CLOCK): the same chart on every run
	}
	lines, err := prom.Range(ctx, expr, end.Add(-window), end, step)
	if err != nil {
		return nil, 0, &failure{Message: "metrics unavailable", Upstream: err}
	}
	if lines == nil {
		lines = []model.SeriesLine{}
	}
	return lines, step, nil
}

// querier is the running prometheus plugin's svc.Querier. With the plugin off (disabled,
// unconfigured or failed) no series can be answered, so there is none.
func (s *Server) querier() (svc.Querier, bool) {
	if s.d.Plugins == nil {
		return nil, false
	}
	v, ok := s.d.Plugins.Service("prometheus")
	if !ok {
		return nil, false
	}
	q, ok := v.(svc.Querier)
	return q, ok
}

// seriesExpr builds the query of a named series from the running plugin that defines it: dash
// defines none itself, so any other name is a bad series request.
func (s *Server) seriesExpr(name string, a queries.Args) (string, error) {
	if s.d.Plugins != nil {
		if sp, _, ok := s.d.Plugins.Series(name); ok {
			return sp.Expr(a)
		}
	}
	return "", fmt.Errorf("%w: unknown series %q", queries.ErrBadSeries, name)
}
