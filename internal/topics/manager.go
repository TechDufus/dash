// Package topics composes core.overview from what the running plugins contribute (attention
// items, KPIs, topology, source health) and publishes it through the core's Host.
package topics

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Deps are the collaborators of a Manager.
type Deps struct {
	// Hosts hands out the Host the overview is published through.
	Hosts plugin.HostProvider
	// Contributors lists the running plugins that add to the overview (Supervisor.Contributors);
	// nil means none do.
	Contributors func() []plugin.Contributor
}

const rebuildEvery = time.Second

// Manager owns the overview's rebuild loop.
type Manager struct {
	d   Deps
	now func() time.Time

	mu sync.Mutex
	// badSources are the plugins whose overview sources panicked on the last round (under mu).
	badSources map[plugin.ID]struct{}
}

// New returns a Manager; call Run to start it.
func New(d Deps) *Manager {
	return &Manager{
		d:          d,
		now:        time.Now,
		badSources: map[plugin.ID]struct{}{},
	}
}

// Run starts the rebuild loop; it does not block.
func (m *Manager) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(rebuildEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.safeRebuild()
			}
		}
	}()
}

// safeRebuild runs one rebuild; a panicking contributor is logged and never kills the process.
func (m *Manager) safeRebuild() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rebuild panic", "panic", r)
		}
	}()
	m.rebuild()
}

// publish hands one topic of the core to its Host, which names, types and forwards it to the
// hub.
func (m *Manager) publish(topic model.Topic, data any, srcs []model.SourceStatus) {
	m.d.Hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(topic), string(plugin.Core)+"."), data, srcs)
}

// rebuild composes the overview from what the running plugins contribute. It does so on every
// round while one does: the hub drops an unchanged result. With no contributor there is nothing
// to say and nothing is published.
func (m *Manager) rebuild() {
	contributors := m.contributors()
	if len(contributors) == 0 {
		return
	}
	m.publish(model.TopicOverview, m.buildOverview(contributors, m.now()), m.overviewSources(contributors))
}
