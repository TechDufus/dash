package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
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
		HostNodename: s.d.Cfg.HostNodename,
		VMID:         q.Get("vmid"),
		Namespace:    firstOf(r, "namespace", "ns"),
		Pod:          q.Get("pod"),
	}
	if node := q.Get("node"); node != "" {
		if !queries.ValidDNS(node) {
			writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid node")
			return
		}
		ip, found := s.d.Data.NodeIP(node)
		if !found {
			writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "unknown node")
			return
		}
		args.Instance = ip + ":" + strconv.Itoa(queries.NodeExporterPort)
	}
	expr, err := s.seriesExpr(name, args)
	if err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
		return
	}
	end := s.now()
	lines, err := s.d.Prom.Range(r.Context(), expr, end.Add(-window), end, step)
	if err != nil {
		s.badGateway(w, "metrics unavailable", err)
		return
	}
	if lines == nil {
		lines = []model.SeriesLine{}
	}
	writeJSON(w, http.StatusOK, model.Series{Name: name, Range: rng, StepSeconds: int(step / time.Second), Series: lines})
}

// seriesExpr builds the query of a named series: a running plugin's series first, then dash's
// built-in ones.
func (s *Server) seriesExpr(name string, a queries.Args) (string, error) {
	if s.d.Plugins != nil {
		if sp, _, ok := s.d.Plugins.Series(name); ok {
			return sp.Expr(a)
		}
	}
	return queries.Series(name, a)
}

func (s *Server) vmDetail(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("vmid")
	if !queries.ValidVMID(raw) {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid vmid")
		return
	}
	vmid, _ := strconv.Atoi(raw)
	d, err := s.d.Data.VMDetail(r.Context(), vmid)
	switch {
	case errors.Is(err, topics.ErrNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "vm not found")
	case err != nil:
		s.badGateway(w, "vm details unavailable", err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func (s *Server) podDetail(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("ns"), r.PathValue("name")
	if !queries.ValidDNS(ns) || !queries.ValidDNS(name) {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid namespace or pod name")
		return
	}
	d, err := s.d.Data.PodDetail(r.Context(), ns, name)
	switch {
	case errors.Is(err, topics.ErrNotFound):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "pod not found")
	case err != nil:
		s.badGateway(w, "pod details unavailable", err)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}

func logQueryFrom(r *http.Request) upstream.LogQuery {
	q := r.URL.Query()
	return upstream.LogQuery{
		Namespace: firstOf(r, "ns", "namespace"),
		Pod:       q.Get("pod"),
		Container: q.Get("container"),
		Text:      q.Get("q"),
		Since:     q.Get("since"),
	}
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	lines, err := s.d.VLogs.Query(r.Context(), logQueryFrom(r))
	switch {
	case errInvalid(err):
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
	case err != nil:
		s.badGateway(w, "logs unavailable", err)
	default:
		if lines == nil {
			lines = []model.LogLine{}
		}
		for i := range lines {
			lines[i].Msg = s.d.Secrets.ScrubLogLine(lines[i].Msg)
		}
		writeJSON(w, http.StatusOK, lines)
	}
}
