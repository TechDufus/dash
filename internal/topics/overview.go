package topics

import (
	"log/slog"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// orZero is *p, or the zero value while the topic is not built (its plugin is disabled).
func orZero[T any](p *T) T {
	if p == nil {
		var z T
		return z
	}
	return *p
}

// buildOverview composes core.overview from the legacy contribution (the Manager's rules over the
// snapshots that exist, zero values for the rest) and what each running plugin in cs adds.
func (m *Manager) buildOverview(b built, cs []plugin.Contributor, now time.Time) model.OverviewData {
	in := AttentionInput{Flux: orZero(b.flux), Resolver: m.resolver(), Now: now}
	contributions := []contribution{{attention: legacyAttention(in, m.tracker)}}
	for _, c := range cs {
		contributions = append(contributions, m.contribution(c, now))
	}
	return compose(contributions, legacyOverview{kpis: kpis(in)})
}

// contributors lists the running plugins that implement an overview source.
func (m *Manager) contributors() []plugin.Contributor {
	if m.d.Contributors == nil {
		return nil
	}
	return m.d.Contributors()
}

// contribution reads one plugin's sources. A plugin that panics contributes nothing that round and
// is logged once until it answers again; it never stops the overview.
func (m *Manager) contribution(c plugin.Contributor, now time.Time) (out contribution) {
	defer func() {
		r := recover()
		m.mu.Lock()
		_, was := m.badSources[c.ID]
		if r == nil {
			delete(m.badSources, c.ID)
		} else {
			m.badSources[c.ID] = struct{}{}
		}
		m.mu.Unlock()
		if r != nil {
			out = contribution{}
			if !was {
				slog.Error("overview contributor panicked", "plugin", c.ID, "panic", r)
			}
		}
	}()
	out.plugin = c.ID
	if c.Attention != nil {
		out.attention = c.Attention.Attention(now)
	}
	if c.KPIs != nil {
		out.kpis = c.KPIs.KPIs()
	}
	if c.Topology != nil {
		t := c.Topology.Topology()
		out.topology = &t
	}
	return out
}

// contributorSources is the source health of the running plugins that contribute to the overview
// (Contributor.Sources), so a plugin's failing upstream shows on the Overview panels.
func (m *Manager) contributorSources(cs []plugin.Contributor) []model.SourceStatus {
	var out []model.SourceStatus
	for _, c := range cs {
		if c.Sources != nil {
			out = append(out, c.Sources()...)
		}
	}
	return out
}

func kpis(in AttentionInput) model.Kpis {
	k := model.Kpis{}
	count := func(l []model.FluxObj) {
		for _, o := range l {
			k.FluxTotal++
			if o.Ready {
				k.FluxReady++
			}
			if o.Suspended {
				k.FluxSuspended++
			}
		}
	}
	count(in.Flux.Kustomizations)
	count(in.Flux.HelmReleases)
	for _, s := range in.Flux.Sources {
		if s.Namespace == "flux-system" && s.Name == "flux-system" && s.Kind == model.FluxKindGitRepository {
			k.SourceVerified = s.Verified != nil && *s.Verified
		}
	}
	return k
}
