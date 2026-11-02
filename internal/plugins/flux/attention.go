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
//   - a source that fails the signature check (verdicts, verify.go) is critical, and so is a pin
//     that no source matches ("pinned source not found") unless the list of its kind could not be
//     read.
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

	failing := map[model.FluxSourceRef]verdict{}
	var missing []verdict
	for _, v := range verdicts(b, cfg) {
		switch {
		case !v.failed():
		case v.obj == nil:
			missing = append(missing, v)
		default:
			failing[v.ref] = v
		}
	}
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
			v, ok := failing[model.FluxSourceRef{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}]
			if !ok || !verifiable(o.Kind) {
				continue
			}
			var since time.Time
			if o.LastTransition != nil {
				since = *o.LastTransition
			}
			add("flux:source-unverified/"+path, model.HealthCrit, name+" signature not verified", v.detail, since, o)
		}
	}
	for _, v := range missing {
		k, ns, n := v.ref.Kind, v.ref.Namespace, v.ref.Name
		items = append(items, model.AttentionItem{
			ID: fmt.Sprintf("flux:source-unverified/%s/%s/%s", k, ns, n), Severity: model.SeverityCrit,
			Source: string(ID), Plugin: string(ID), Title: fmt.Sprintf("%s %s/%s signature not verified", k, ns, n),
			Detail: v.detail, Route: "/gitops", Links: []model.Link{},
			Ref: plugin.Ref(ID, string(k), ns, n),
		})
	}
	return items
}

// firstLine is s up to its first line break, trimmed.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
