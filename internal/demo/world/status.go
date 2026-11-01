package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Status is the gatus payload: twelve endpoints with forty minutes of check history each. The
// infrastructure group is the one the demo twin lists as critical.
func (w *World) Status() model.StatusData { return w.status(w.clock()) }

func (w *World) status(now time.Time) model.StatusData {
	type epSpec struct {
		group, name string
		ms          float64
	}
	var d model.StatusData
	for _, e := range []epSpec{
		{"platform", "dash", 42}, {"platform", "grafana", 88}, {"platform", "headlamp", 61},
		{"platform", "registry", 35}, {"apps", "homepage", 54}, {"apps", "gatus", 12},
		{"tenants", "acme-web", 120}, {"tenants", "acme-api", 74}, {"edge", "public-site", 210},
		{"infrastructure", "gateway", 8}, {"infrastructure", "dns", 6}, {"infrastructure", "nas", 18},
	} {
		ep := model.Endpoint{Group: e.group, Name: e.name, Key: e.group + "_" + e.name,
			URL: "https://" + e.name + ".example.com", Healthy: true}
		for i := 39; i >= 0; i-- {
			t := now.Add(-time.Duration(i) * time.Minute).Truncate(time.Minute)
			ok := !((e.name == "acme-api" && (i == 31 || i == 30)) || (e.name == "nas" && i == 22))
			ep.Results = append(ep.Results, model.CheckResult{Success: ok, Timestamp: t.UTC(),
				DurationMs: Around(e.name+t.String(), t, e.ms, e.ms*0.3)})
		}
		d.Endpoints = append(d.Endpoints, ep)
	}
	return d
}
