package flux

import (
	"fmt"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// Attention implements plugin.AttentionSource: the rules over the payload the plugin published
// last. It has no opinion (nil) while the plugin has not built a payload for the service it
// reads, and an empty, non-nil list once it has and there is nothing to raise, so a resumed
// object prunes its first sighting and a second suspension gets a new time. An item without a
// time of its own is stamped by the supervisor with the time it was first seen.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	b, cfg := p.lastBuilt()
	if b == nil {
		return nil
	}
	return attentionOf(*b, cfg)
}

// lastBuilt is the last real payload and the settings it was judged by, nil while there is none
// or while the row is hidden: what a hidden plugin last built is not shown, whatever the
// supervisor's own guard does (the unavailable state describes a cluster, or a payload, that
// must not reach the overview).
func (p *Plugin) lastBuilt() (*built, pluginconf.Flux) {
	r := p.rt.Load()
	if r == nil {
		return nil, pluginconf.Flux{}
	}
	r.mu.RLock()
	b := r.last
	r.mu.RUnlock()
	if b == nil || !plugin.Visible(r.status().State) {
		return nil, pluginconf.Flux{}
	}
	return b, r.cfg
}

// attentionOf applies the rules (design F.0, O-3) to a payload, in payload order (the sources,
// the Kustomizations, the Helm releases) and then the pins that were not found:
//
//   - a suspended object, of any of the five kinds, is a warning with the reason annotation's text
//     ("suspended" without one); it is not also "not ready";
//   - an object that is not ready and not suspended is a warning, since its last transition;
//   - a GitRepository or OCIRepository is critical when it is pinned and is not both signed
//     (spec.verify) and verified (SourceVerified=True), or when requireWhenSpecVerify is on and it
//     is signed and not verified;
//   - a pin that no source matches is critical ("pinned source not found"), unless the list of
//     that kind could not be read.
func attentionOf(b built, cfg pluginconf.Flux) []model.AttentionItem {
	items := []model.AttentionItem{}
	add := func(id string, sev model.Health, title, detail string, since time.Time, o model.FluxObj) {
		links := o.Links
		if links == nil {
			links = []model.Link{}
		}
		items = append(items, model.AttentionItem{
			ID: id, Severity: model.Severity(sev), Source: string(ID), Plugin: string(ID), Title: title, Detail: detail,
			Since: since, Route: "/gitops", Links: links, Ref: o.Ref,
		})
	}

	pinned := pinsOf(cfg)
	found := map[model.FluxSourceRef]bool{}
	for _, list := range [][]model.FluxObj{b.data.Sources, b.data.Kustomizations, b.data.HelmReleases} {
		for _, o := range list {
			name := fmt.Sprintf("%s %s/%s", o.Kind, o.Namespace, o.Name)
			path := fmt.Sprintf("%s/%s/%s", o.Kind, o.Namespace, o.Name)
			switch {
			case o.Suspended:
				// No time of its own: the supervisor stamps the first sighting.
				detail := o.SuspendReason
				if detail == "" {
					detail = "suspended"
				}
				add("flux:suspended/"+path, model.HealthWarn, name+" suspended", detail, time.Time{}, o)
			case !o.Ready:
				var since time.Time
				if o.LastTransition != nil {
					since = *o.LastTransition
				}
				add("flux:"+path, model.HealthWarn, name+" not ready", firstLine(o.Message), since, o)
			}
			if !verifiable(o.Kind) {
				continue
			}
			ref := model.FluxSourceRef{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
			found[ref] = true
			verified := o.Verified != nil && *o.Verified
			var detail string
			switch {
			case pinned[ref] && !o.SpecVerify:
				detail = "the pinned source has no spec.verify"
			case (pinned[ref] || cfg.Verify.RequireWhenSpecVerify && o.SpecVerify) && !verified:
				detail = "SourceVerified is not True"
			default:
				continue
			}
			var since time.Time
			if o.LastTransition != nil {
				since = *o.LastTransition
			}
			add("flux:source-unverified/"+path, model.HealthCrit, name+" signature not verified", detail, since, o)
		}
	}
	for _, p := range cfg.Verify.Sources {
		ref := model.FluxSourceRef{Kind: model.FluxKind(p.Kind), Namespace: p.Namespace, Name: p.Name}
		if found[ref] || b.unknown[ref.Kind] {
			continue
		}
		items = append(items, model.AttentionItem{
			ID: fmt.Sprintf("flux:source-unverified/%s/%s/%s", p.Kind, p.Namespace, p.Name), Severity: model.SeverityCrit,
			Source: string(ID), Plugin: string(ID), Title: fmt.Sprintf("%s %s/%s signature not verified", p.Kind, p.Namespace, p.Name),
			Detail: "pinned source not found", Route: "/gitops", Links: []model.Link{},
			Ref: plugin.Ref(ID, p.Kind, p.Namespace, p.Name),
		})
	}
	return items
}

// verifiable reports that a source of kind can carry spec.verify and a SourceVerified condition.
func verifiable(kind model.FluxKind) bool {
	return kind == model.FluxKindGitRepository || kind == model.FluxKindOCIRepository
}

// pinsOf is the configured pins as a set.
func pinsOf(cfg pluginconf.Flux) map[model.FluxSourceRef]bool {
	out := map[model.FluxSourceRef]bool{}
	for _, p := range cfg.Verify.Sources {
		out[model.FluxSourceRef{Kind: model.FluxKind(p.Kind), Namespace: p.Namespace, Name: p.Name}] = true
	}
	return out
}

// firstLine is s up to its first line break, trimmed.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
