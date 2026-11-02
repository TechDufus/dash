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
// warns while one is not ready. verified is present only when a source is pinned or signed
// (spec.verify), whatever requireWhenSpecVerify says (that setting is the attention rule's): 1
// when every one of them is verified, 0 otherwise, and a pinned source that does not exist
// counts as not verified, unless the list of its kind could not be read. At 0 it is critical.
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
	if checked, ok := verification(b, cfg); checked {
		v, health := 1, model.HealthOK
		if !ok {
			v, health = 0, model.HealthCrit
		}
		out = append(out, k(kpiVerified, "Source verified", v, health))
	}
	return out
}

// verification says whether any source is pinned or signed and, when one is, whether all of
// them are verified: a pin needs spec.verify and SourceVerified=True, a source that is only
// signed needs SourceVerified=True.
func verification(b built, cfg pluginconf.Flux) (checked, allVerified bool) {
	pinned := pinsOf(cfg)
	allVerified = true
	found := map[model.FluxSourceRef]bool{}
	for _, s := range b.data.Sources {
		if !verifiable(s.Kind) {
			continue
		}
		ref := model.FluxSourceRef{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name}
		found[ref] = true
		if !pinned[ref] && !s.SpecVerify {
			continue
		}
		checked = true
		if (s.Verified == nil || !*s.Verified) || (pinned[ref] && !s.SpecVerify) {
			allVerified = false
		}
	}
	for ref := range pinned {
		if found[ref] || b.unknown[ref.Kind] {
			continue
		}
		checked, allVerified = true, false
	}
	return checked, allVerified
}
