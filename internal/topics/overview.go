package topics

import (
	"log/slog"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// buildOverview composes core.overview from what each running plugin in cs adds.
func (m *Manager) buildOverview(cs []plugin.Contributor, now time.Time) model.OverviewData {
	contributions := make([]contribution, 0, len(cs))
	for _, c := range cs {
		contributions = append(contributions, m.contribution(c, now))
	}
	return compose(contributions)
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

// overviewSources is core.overview.sources: the source health of the running plugins that
// contribute to the overview (Contributor.Sources), so a plugin's failing upstream shows on the
// Overview panels. A name that two plugins report appears once, the failing entry winning.
func (m *Manager) overviewSources(cs []plugin.Contributor) []model.SourceStatus {
	var own []model.SourceStatus
	for _, c := range cs {
		if c.Sources != nil {
			own = append(own, c.Sources()...)
		}
	}
	return mergeSources(nil, own)
}
