package flux

import (
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The keys of the plugin's KPIs.
const (
	kpiReady     = "ready"
	kpiTotal     = "total"
	kpiSuspended = "suspended"
	kpiVerified  = "verified"
)

// KPIs implements plugin.KPISource: the Kustomizations and Helm releases that are ready, there
// are and are suspended, and whether the sources that must be signed are verified. It has no
// opinion (nil) while the plugin has not built a payload for the service it reads.
func (p *Plugin) KPIs() []model.KPI {
	b, cfg := p.lastBuilt()
	if b == nil {
		return nil
	}
	return kpisOf(*b, cfg)
}

// kpisOf derives the KPIs from a payload. ready, total and suspended count the Kustomizations
// and Helm releases (the sources are not what "n of m ready" has always meant); the ready tile
// warns while one is not ready. verified follows the signature check's scope (verify.go): it is
// present exactly when a source is pinned, or signed (spec.verify) while requireWhenSpecVerify is
// on, or a pin was not found, and it is 0 (critical) exactly when one of them fails, which is
// exactly when a flux:source-unverified/* attention item exists, and 1 otherwise. With
// requireWhenSpecVerify off only the pins count.
func kpisOf(b built, cfg pluginconf.Flux) []model.KPI {
	ready, total, suspended := 0, 0, 0
	for _, list := range [][]model.FluxObj{b.data.Kustomizations, b.data.HelmReleases} {
		for _, o := range list {
			total++
			if o.Ready {
				ready++
			}
			if o.Suspended {
				suspended++
			}
		}
	}
	h := model.HealthOK
	if ready < total {
		h = model.HealthWarn
	}
	k := func(key, label string, v int, health model.Health) model.KPI {
		return model.KPI{Key: key, Plugin: string(ID), Label: label, Value: float64(v), Health: health}
	}
	out := []model.KPI{
		k(kpiReady, "Flux ready", ready, h),
		k(kpiTotal, "Flux objects", total, model.HealthOK),
		k(kpiSuspended, "Flux suspended", suspended, model.HealthOK),
	}
	if vs := verdicts(b, cfg); len(vs) > 0 {
		v, health := 1, model.HealthOK
		for _, d := range vs {
			if d.failed() {
				v, health = 0, model.HealthCrit
				break
			}
		}
		out = append(out, k(kpiVerified, "Source verified", v, health))
	}
	return out
}
