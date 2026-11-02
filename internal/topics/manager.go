// Package topics builds the integration topic that still runs in the dash process (flux) from the
// kubernetes plugin's informer caches, and composes core.overview from it and from what plugins
// contribute. The topic is published through the Host of the plugin that owns it (legacy.go), so a
// disabled plugin publishes nothing.
package topics

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// Deps are the collaborators of a Manager.
type Deps struct {
	Cfg config.Config
	// Cluster is the kubernetes plugin's svc.Cluster while the plugin runs (the accessor reads
	// Supervisor.Service per call: the plugin restarts on a reload and its service changes with
	// it), else false. nil means no cluster. The Manager compares the values it gets, so a
	// service must be a comparable value such as a pointer.
	Cluster func() (svc.Cluster, bool)
	// Hosts hands out the Host each topic is published through, and says which plugins run.
	Hosts plugin.HostProvider
	// Contributors lists the running plugins that add to the overview (Supervisor.Contributors);
	// nil means none do.
	Contributors func() []plugin.Contributor
	// LinkProviders is Supervisor.Links: the resolver over the running link providers (grafana,
	// headlamp), read once per rebuild. nil means none: no Grafana or Headlamp link is built.
	LinkProviders func() plugin.LinkResolver
}

const rebuildEvery = time.Second

// Manager owns the dirty-topic rebuild loop.
type Manager struct {
	d   Deps
	now func() time.Time

	mu    sync.RWMutex
	built built

	linkSet atomic.Pointer[plugin.LinkResolver] // the provider snapshot of the running rebuild
	tracker *Tracker

	// seenCluster, seenSynced, seenListed and seenRevision are the kubernetes service and what
	// watchCluster read of it in the last rebuild round; only that round reads or writes them.
	seenCluster  svc.Cluster
	seenSynced   bool
	seenListed   bool
	seenRevision uint64

	dmu   sync.Mutex
	dirty map[model.Topic]bool

	// badSources are the plugins whose overview sources panicked on the last round (under mu).
	badSources map[plugin.ID]struct{}
}

// built holds the latest typed data of every topic; the overview derives from them.
type built struct {
	flux *model.FluxData
}

// New returns a Manager; call Run to start it.
func New(d Deps) *Manager {
	return &Manager{
		d:          d,
		now:        time.Now,
		tracker:    NewTracker(),
		dirty:      map[model.Topic]bool{},
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

// safeRebuild runs one rebuild; a panicking builder is logged and never kills the process.
// The dirty set was already consumed, so the affected topics rebuild on their next change.
func (m *Manager) safeRebuild() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rebuild panic", "panic", r)
		}
	}()
	m.rebuild()
}

// kubeSources is the status of the kubernetes plugin's informers as the Manager's topics see it:
// the plugin's service is absent (not configured, disabled or failed to start), or it has not
// synced, or it has.
func (m *Manager) kubeSources() []model.SourceStatus {
	s := model.SourceStatus{Name: "kubernetes", CheckedAt: m.now().UTC()}
	switch c, ok := m.cluster(); {
	case !ok:
		s.Error = upstream.CategoryNotConfigured
	case !c.Synced():
		s.Error = "informers not synced"
	default:
		s.OK = true
	}
	return []model.SourceStatus{s}
}

func (m *Manager) mark(topics ...model.Topic) {
	m.dmu.Lock()
	for _, t := range topics {
		m.dirty[t] = true
	}
	m.dmu.Unlock()
}

// watchCluster marks flux when the kubernetes service, its Synced state, its flux resources'
// revision or the listed state of the five flux resources differs from the last round's. A plugin
// cannot mark the Manager's topics, and a callback registered once would not survive a reload
// (which builds a new service), so the Manager compares on its own tick. It asks each new
// service to watch the flux resources once. It runs at the top of rebuild, ahead of the early
// return for a quiet round.
func (m *Manager) watchCluster() {
	c, ok := m.cluster()
	var (
		synced, listed bool
		rev            uint64
	)
	if ok {
		if c != m.seenCluster {
			c.Watch(fluxGVRs...)
		}
		synced, listed, rev = c.Synced(), fluxListed(c), c.GVRRevision(fluxGVRs...)
	} else if m.seenCluster != nil {
		m.withdrawFlux()
	}
	if c == m.seenCluster && synced == m.seenSynced && listed == m.seenListed && rev == m.seenRevision {
		return
	}
	m.seenCluster, m.seenSynced, m.seenListed, m.seenRevision = c, synced, listed, rev
	m.mark(model.TopicFlux)
}

// withdrawFlux is what the flux topic does when the kubernetes service goes away: flux is
// unavailable while kubernetes is, so the overview stops carrying its attention items and KPIs,
// and the topic no longer holds the last list for a subscriber that opens once the row is
// visible again. It publishes once per withdrawal an empty topic whose source says why.
func (m *Manager) withdrawFlux() {
	m.mu.Lock()
	had := m.built.flux != nil
	m.built.flux = nil
	m.mu.Unlock()
	if had {
		m.publish(model.TopicFlux, model.FluxData{
			Sources: []model.FluxObj{}, Kustomizations: []model.FluxObj{}, HelmReleases: []model.FluxObj{},
		}, m.kubeSources())
	}
}

// publish hands one topic to the Host of the plugin that owns it, which names, types and
// forwards it to the hub. The overview belongs to no plugin: the core publishes it.
func (m *Manager) publish(topic model.Topic, data any, srcs []model.SourceStatus) {
	if o, ok := owners[topic]; ok {
		m.d.Hosts.HostFor(o.plugin).Publish(o.local, data, srcs)
		return
	}
	m.d.Hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(topic), string(plugin.Core)+"."), data, srcs)
}

// resolver is the link resolver of the running rebuild: one snapshot of the providers per
// rebuild, not one per object. Outside a rebuild (a builder called on its own) it reads them
// afresh.
func (m *Manager) resolver() plugin.LinkResolver {
	if r := m.linkSet.Load(); r != nil {
		return *r
	}
	return m.providers()
}

// providers reads the running link providers; none when the Manager has no supervisor.
func (m *Manager) providers() plugin.LinkResolver {
	if m.d.LinkProviders == nil {
		return plugin.NoLinks
	}
	return m.d.LinkProviders()
}

// link is the links of one object into Grafana and Headlamp: always a non-nil slice.
func (m *Manager) link(kind, namespace, name string) []model.Link {
	return m.resolver().Resolve(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}

// rebuild rebuilds every dirty topic whose prerequisites are ready, then composes the overview
// from what exists. The overview waits for no topic: it is recomposed after any change, and on
// every round while a plugin contributes to it (the hub drops an unchanged result).
func (m *Manager) rebuild() {
	m.watchCluster() // before the early return below: a quiet cluster still follows the plugin
	m.dmu.Lock()
	dirty := m.dirty
	m.dirty = map[model.Topic]bool{}
	m.dmu.Unlock()
	contributors := m.contributors()
	if len(dirty) == 0 && len(contributors) == 0 {
		return
	}
	snap := m.providers()
	m.linkSet.Store(&snap)
	defer m.linkSet.Store(nil)
	now := m.now()

	build := func(t model.Topic, ready bool, fn func() (any, []model.SourceStatus)) {
		if !dirty[t] {
			return
		}
		if !m.d.Hosts.Running(owners[t].plugin) {
			return // a disabled plugin publishes nothing, and nothing waits for it
		}
		if !ready {
			m.mark(t)
			return
		}
		data, srcs := fn()
		m.publish(t, data, srcs)
	}

	// The flux build waits for the kubernetes service, its sync and the first list (or the
	// absence) of each of the five flux resources: sourceVerified is never computed from a
	// partial list.
	c, haveCluster := m.cluster()
	build(model.TopicFlux, haveCluster && c.Synced() && fluxListed(c), func() (any, []model.SourceStatus) {
		d := m.buildFlux(c)
		m.mu.Lock()
		m.built.flux = &d
		m.mu.Unlock()
		return d, m.kubeSources()
	})

	m.mu.RLock()
	b := m.built
	m.mu.RUnlock()
	m.publish(model.TopicOverview, m.buildOverview(b, m.withoutHidden(contributors, haveCluster), now),
		m.overviewSources(contributors))
}
