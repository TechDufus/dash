// Package topics builds the six integration topics (host, vms, cluster, flux, storage, apps) from
// the Kubernetes informers and the upstream clients, and composes core.overview
// from them and from what plugins contribute. Each topic is published through the Host of the
// plugin that owns it (legacy.go), so a disabled plugin publishes nothing.
package topics

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// ErrNotFound is returned by the detail lookups when the object does not exist.
var ErrNotFound = errors.New("not found")

// Deps are the collaborators of a Manager.
type Deps struct {
	Cfg     config.Config
	Cluster *kube.Cluster
	PVE     *upstream.Proxmox
	// Links builds the Proxmox links (VM console, host shell, Proxmox UI). The links into
	// Headlamp and Grafana come from LinkProviders.
	Links *links.Builder
	// Hosts hands out the Host each topic is published through, and says which plugins run.
	Hosts plugin.HostProvider
	// Contributors lists the running plugins that add to the overview (Supervisor.Contributors);
	// nil means none do.
	Contributors func() []plugin.Contributor
	// Services is Supervisor.Service: what a running plugin exports (svc.Querier,
	// svc.EndpointHealth, …), for the Manager, which is not a plugin and so cannot use
	// Host.Service. The Manager reads them through accessors (querier, endpoints) that return a
	// stub while the plugin is absent. nil means no plugin exports anything.
	Services func(id plugin.ID) (any, bool)
	// LinkProviders is Supervisor.Links: the resolver over the running link providers (grafana,
	// headlamp), read once per rebuild. nil means none: no Grafana or Headlamp link is built.
	LinkProviders func() plugin.LinkResolver
	// Secrets scrubs logged upstream errors; nil only matches credential shapes.
	Secrets *secret.Registry
}

const (
	rebuildEvery = time.Second
	pollTimeout  = 25 * time.Second
)

// Cadences of the upstream pollers.
const (
	hostEvery    = 15 * time.Second
	vmsEvery     = 15 * time.Second
	usageEvery   = 30 * time.Second
	storageEvery = 60 * time.Second
)

type srcState struct {
	ok  bool
	err string
	at  time.Time
}

// Manager owns the pollers and the dirty-topic rebuild loop.
type Manager struct {
	d   Deps
	now func() time.Time

	mu      sync.RWMutex
	srcs    map[string]srcState
	done    map[string]bool // first poll attempt finished
	host    hostSnap
	vms     vmsSnap
	usage   usageSnap
	storage storageSnap

	built   built
	linkSet atomic.Pointer[plugin.LinkResolver] // the provider snapshot of the running rebuild
	tracker *Tracker
	nodeRev atomic.Uint64 // svc.NodeIndex.Revision: bumped when a node is added, removed or changes readiness

	// seenEndpoints and seenRevision are the service of svc.EndpointHealth and its Revision as
	// the last rebuild round saw them (see watchEndpoints); only that round reads or writes them.
	seenEndpoints any
	seenRevision  uint64

	dmu   sync.Mutex
	dirty map[model.Topic]bool

	// badSources are the plugins whose overview sources panicked on the last round (under mu).
	badSources map[plugin.ID]struct{}
}

// built holds the latest typed data of every topic; the overview derives from them.
type built struct {
	host    *model.HostData
	vms     *model.VMsData
	cluster *model.ClusterData
	flux    *model.FluxData
	storage *model.StorageData
	apps    *model.AppsData
}

// New returns a Manager; call Run to start it.
func New(d Deps) *Manager {
	m := &Manager{
		d:          d,
		now:        time.Now,
		srcs:       map[string]srcState{},
		done:       map[string]bool{},
		tracker:    NewTracker(),
		dirty:      map[model.Topic]bool{},
		badSources: map[plugin.ID]struct{}{},
	}
	// What exists now is not a change; Run marks every topic once the caches are filled.
	id, ep, _ := m.lookupEndpoints()
	m.seenEndpoints, m.seenRevision = id, ep.Revision()
	return m
}

// Ready is true once every informer has synced. Upstream outages never affect it.
func (m *Manager) Ready() bool { return m.d.Cluster.HasSynced() }

// Run starts the informers, the pollers and the rebuild loop; it does not block.
func (m *Manager) Run(ctx context.Context) {
	m.d.Cluster.OnChange(m.onKubeChange)
	m.d.Cluster.Start(ctx)

	m.every(ctx, "host", hostEvery, m.pollHost)
	m.every(ctx, "vms", vmsEvery, m.pollVMs)
	m.every(ctx, "usage", usageEvery, m.pollUsage)
	m.every(ctx, "storage", storageEvery, m.pollStorage)

	go func() {
		// Everything is dirty once the caches are filled, so the first full state is published.
		if m.d.Cluster.WaitForSync(ctx) {
			m.markAll()
		}
	}()
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

func (m *Manager) every(ctx context.Context, name string, d time.Duration, fn func(context.Context)) {
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("poller panic", "poller", name, "panic", r)
					}
				}()
				if !m.anyRunning(pollUsers[name]) {
					return // nothing that reads this source runs: no request is made
				}
				pctx, cancel := context.WithTimeout(ctx, pollTimeout)
				defer cancel()
				fn(pctx)
			}()
			m.mu.Lock()
			m.done[name] = true
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (m *Manager) pollDone(names ...string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, n := range names {
		if !m.done[n] {
			return false
		}
	}
	return true
}

// setSource records the outcome of one upstream call; key is "<scope>:<source>". Viewers get
// only the error category (design C.6, K.3): the detail names URLs and hosts, so it goes to the
// log, scrubbed, once per change of category rather than on every poll.
func (m *Manager) setSource(key string, err error) {
	s := srcState{ok: err == nil, at: m.now().UTC(), err: upstream.Category(err)}
	m.mu.Lock()
	prev := m.srcs[key]
	m.srcs[key] = s
	m.mu.Unlock()
	if err != nil && s.err != upstream.CategoryNotConfigured && (prev.ok || prev.err != s.err) {
		slog.Warn("upstream source failed", "source", key, "category", s.err, "error", m.d.Secrets.Scrub(err.Error()))
	}
}

// sources lists SourceStatus for keys; a plain name without ':' is the Kubernetes informers.
// Keys written by a poller that is not running (see polled) are left out: they would stay "not
// polled yet" forever.
func (m *Manager) sources(keys ...string) []model.SourceStatus {
	keys = m.polledKeys(keys)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.SourceStatus, 0, len(keys))
	seen := map[string]int{}
	for _, k := range keys {
		name := k
		if _, after, found := strings.Cut(k, ":"); found {
			name = after
		}
		var s model.SourceStatus
		if k == "kubernetes" {
			s = model.SourceStatus{Name: k, OK: m.d.Cluster.HasSynced(), CheckedAt: m.now().UTC()}
			if !s.OK {
				s.Error = "informers not synced"
			}
		} else {
			st := m.srcs[k]
			s = model.SourceStatus{Name: name, OK: st.ok, Error: st.err, CheckedAt: st.at}
			if st.at.IsZero() {
				s.OK, s.Error, s.CheckedAt = false, "not polled yet", m.now().UTC()
			}
		}
		if i, dup := seen[name]; dup {
			if !s.OK && out[i].OK {
				out[i] = s
			}
			continue
		}
		seen[name] = len(out)
		out = append(out, s)
	}
	return out
}

func (m *Manager) mark(topics ...model.Topic) {
	m.dmu.Lock()
	for _, t := range topics {
		m.dirty[t] = true
	}
	m.dmu.Unlock()
}

func (m *Manager) markAll() {
	m.mark(model.TopicHost, model.TopicVMs, model.TopicCluster, model.TopicFlux, model.TopicStorage,
		model.TopicApps)
}

func (m *Manager) onKubeChange(k kube.Kind) {
	switch k {
	case kube.KindNodes:
		m.nodeRev.Add(1)
		m.mark(model.TopicCluster, model.TopicVMs)
	case kube.KindNamespaces:
		m.mark(model.TopicCluster, model.TopicApps)
	case kube.KindPods, kube.KindWorkloads, kube.KindEvents, kube.KindCertificates, kube.KindExtSecrets:
		m.mark(model.TopicCluster)
	case kube.KindPVCs, kube.KindCNPG:
		m.mark(model.TopicStorage)
	case kube.KindFlux:
		m.mark(model.TopicFlux)
	case kube.KindHTTPRoutes:
		m.mark(model.TopicApps)
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

// anyRunning reports whether at least one of ids runs; an empty list means "always", and so does
// a Manager without a supervisor (Hosts nil).
func (m *Manager) anyRunning(ids []plugin.ID) bool {
	if len(ids) == 0 || m.d.Hosts == nil {
		return true
	}
	for _, id := range ids {
		if m.d.Hosts.Running(id) {
			return true
		}
	}
	return false
}

// keyPoller names the poller (the name Run and pollUsers use) that writes the source keys with
// a given scope. Keys outside it, the informers' "kubernetes", are always live.
var keyPoller = map[string]string{
	"host": "host", "vms": "vms", "cluster": "usage", "storage": "storage",
}

// polled reports whether source key k is being written: its poller runs only while a plugin that
// reads it does. A key whose poller is skipped never gets a first poll.
func (m *Manager) polled(k string) bool {
	scope, _, _ := strings.Cut(k, ":")
	poller, ok := keyPoller[scope]
	return !ok || m.anyRunning(pollUsers[poller])
}

// polledKeys is keys without the ones nothing polls. It takes no lock: Hosts has its own.
func (m *Manager) polledKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if m.polled(k) {
			out = append(out, k)
		}
	}
	return out
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
	m.watchEndpoints() // before the early return below: a quiet cluster still follows gatus
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
	synced := m.d.Cluster.HasSynced()
	retry := func(t model.Topic) { m.mark(t) }
	now := m.now()

	build := func(t model.Topic, ready bool, fn func() (any, []model.SourceStatus)) {
		if !dirty[t] {
			return
		}
		if !m.d.Hosts.Running(owners[t].plugin) {
			return // a disabled plugin publishes nothing, and nothing waits for it
		}
		if !ready {
			retry(t)
			return
		}
		data, srcs := fn()
		m.publish(t, data, srcs)
	}

	build(model.TopicHost, m.pollDone("host"), func() (any, []model.SourceStatus) {
		d := m.buildHost()
		m.mu.Lock()
		m.built.host = &d
		m.mu.Unlock()
		return d, m.sources("host:proxmox", "host:victoriametrics")
	})
	build(model.TopicCluster, synced, func() (any, []model.SourceStatus) {
		d := m.buildCluster(now)
		m.mu.Lock()
		m.built.cluster = &d
		m.mu.Unlock()
		return d, m.sources("kubernetes", "cluster:victoriametrics")
	})
	build(model.TopicFlux, synced, func() (any, []model.SourceStatus) {
		d := m.buildFlux()
		m.mu.Lock()
		m.built.flux = &d
		m.mu.Unlock()
		return d, m.sources("kubernetes")
	})
	build(model.TopicVMs, synced && m.pollDone("vms"), func() (any, []model.SourceStatus) {
		d := m.buildVMs()
		m.mu.Lock()
		m.built.vms = &d
		m.mu.Unlock()
		return d, append(m.sources("vms:proxmox", "vms:victoriametrics", "kubernetes"), m.endpoints().SourceStatuses()...)
	})
	build(model.TopicStorage, synced && m.pollDone("storage"), func() (any, []model.SourceStatus) {
		d := m.buildStorage(now)
		m.mu.Lock()
		m.built.storage = &d
		m.mu.Unlock()
		return d, m.sources("kubernetes", "storage:victoriametrics")
	})
	build(model.TopicApps, synced && m.endpointsReady(), func() (any, []model.SourceStatus) {
		d := m.buildApps()
		m.mu.Lock()
		m.built.apps = &d
		m.mu.Unlock()
		return d, append(m.sources("kubernetes"), m.endpoints().SourceStatuses()...)
	})

	m.mu.RLock()
	b := m.built
	m.mu.RUnlock()
	srcs := m.sources(
		"kubernetes", "host:proxmox", "host:victoriametrics", "vms:proxmox", "vms:victoriametrics",
		"cluster:victoriametrics", "storage:victoriametrics")
	m.publish(model.TopicOverview, m.buildOverview(b, contributors, now), mergeSources(srcs, m.contributorSources(contributors)))
}
