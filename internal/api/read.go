package api

import (
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
	window, step, ok := queries.Range(rng)
	if !ok {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "unknown range")
		return
	}
	args := queries.Args{
		VMID:      q.Get("vmid"),
		Namespace: firstOf(r, "namespace", "ns"),
		Pod:       q.Get("pod"),
	}
	// The node is named, not resolved: the series that reads it (a plugin's) turns the name into
	// its instance and answers plugin.ErrNotFound for a node it does not know.
	if node := q.Get("node"); node != "" {
		if !queries.ValidDNS(node) {
			writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid node")
			return
		}
		args.Instance = node
	}
	expr, err := s.seriesExpr(name, args)
	switch {
	case errors.Is(err, plugin.ErrNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, err.Error())
		return
	case err != nil:
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
		return
	}
	prom, running := s.querier()
	if !running {
		// Everything above is validated: only now does it matter that no prometheus runs.
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "unknown series")
		return
	}
	end := s.now()
	if c := s.d.Cfg.DemoClock; !c.IsZero() {
		end = c // demo mode's fixed clock (DASH_DEMO_CLOCK): the same chart on every run
	}
	lines, err := prom.Range(r.Context(), expr, end.Add(-window), end, step)
	if err != nil {
		s.badGateway(w, "metrics unavailable", err)
		return
	}
	if lines == nil {
		lines = []model.SeriesLine{}
	}
	writeJSON(w, http.StatusOK, model.Series{Name: name, Range: rng, StepSeconds: int(step / time.Second), Series: lines})
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
