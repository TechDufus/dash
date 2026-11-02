// Package overview composes core.overview from what the running plugins contribute (attention
// items, KPIs, topology, source health) and publishes it through the core's Host. It knows no
// plugin: it reads the contributors the plugin framework hands it, in production and in demo mode
// alike.
package overview

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Deps are the collaborators of a Publisher.
type Deps struct {
	// Hosts hands out the Host the overview is published through.
	Hosts plugin.HostProvider
	// Contributors lists the running plugins that add to the overview (Supervisor.Contributors);
	// nil means none do.
	Contributors func() []plugin.Contributor
}

const rebuildEvery = time.Second

// Publisher owns the overview's rebuild loop.
type Publisher struct {
	d   Deps
	now func() time.Time

	mu sync.Mutex
	// badSources are the plugins whose overview sources panicked on the last round (under mu).
	badSources map[plugin.ID]struct{}
}

// New returns a Publisher; call Run to start it.
func New(d Deps) *Publisher {
	return &Publisher{
		d:          d,
		now:        time.Now,
		badSources: map[plugin.ID]struct{}{},
	}
}

// Run starts the rebuild loop; it does not block.
func (p *Publisher) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(rebuildEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.safeRebuild()
			}
		}
	}()
}

// safeRebuild runs one rebuild; a panic is logged and never kills the process.
func (p *Publisher) safeRebuild() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rebuild panic", "panic", r)
		}
	}()
	p.rebuild()
}

// rebuild composes the overview from what the running plugins contribute. It does so on every
// round while one does: the hub drops an unchanged result. With no contributor there is nothing
// to say and nothing is published.
func (p *Publisher) rebuild() {
	contributors := p.contributors()
	if len(contributors) == 0 {
		return
	}
	data := p.buildOverview(contributors, p.now())
	p.d.Hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(model.TopicOverview), string(plugin.Core)+"."), data, p.overviewSources(contributors))
}

// buildOverview composes core.overview from what each running plugin in cs adds.
func (p *Publisher) buildOverview(cs []plugin.Contributor, now time.Time) model.OverviewData {
	contributions := make([]contribution, 0, len(cs))
	for _, c := range cs {
		contributions = append(contributions, p.contribution(c, now))
	}
	return compose(contributions)
}

// contributors lists the running plugins that implement an overview source.
func (p *Publisher) contributors() []plugin.Contributor {
	if p.d.Contributors == nil {
		return nil
	}
	return p.d.Contributors()
}

// contribution reads one plugin's sources. A plugin that panics contributes nothing that round and
// is logged once until it answers again; it never stops the overview.
func (p *Publisher) contribution(c plugin.Contributor, now time.Time) (out contribution) {
	defer func() {
		r := recover()
		p.mu.Lock()
		_, was := p.badSources[c.ID]
		if r == nil {
			delete(p.badSources, c.ID)
		} else {
			p.badSources[c.ID] = struct{}{}
		}
		p.mu.Unlock()
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
func (p *Publisher) overviewSources(cs []plugin.Contributor) []model.SourceStatus {
	var own []model.SourceStatus
	for _, c := range cs {
		if c.Sources != nil {
			own = append(own, c.Sources()...)
		}
	}
	return mergeSources(nil, own)
}
