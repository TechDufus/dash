package kubernetes

import (
	"time"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// status is design 6.3: unavailable while the probe has failed for the debounce; error when the
// typed informers have not synced in time; starting until they have and the first discovery and
// polls have ended; then degraded (a wanted group is missing or failed) or healthy. The messages
// carry categories and setting names only.
func (r *runtime) status() plugin.Status {
	if r.twin != nil {
		return plugin.Status{State: plugin.StateHealthy}
	}
	now := r.h.Now()
	srcs := r.rowSources(now)
	if r.isUnavailable(now) {
		return plugin.Status{State: plugin.StateUnavailable, Message: "api server unreachable", Sources: srcs}
	}
	synced := r.cl.HasSynced()
	if !synced && now.Sub(r.started) >= r.t.typedSyncTimeout {
		cat := "timeout"
		if err := r.cl.SyncError(); err != nil {
			cat = kube.Category(err)
		}
		return plugin.Status{State: plugin.StateError, Message: "the informers did not sync: " + cat, Sources: srcs}
	}
	if !synced || !r.firstRoundsEnded() {
		return plugin.Status{State: plugin.StateStarting, Message: "starting", Sources: srcs}
	}
	if msg := r.degraded(); msg != "" {
		return plugin.Status{State: plugin.StateDegraded, Message: msg, Sources: srcs}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: srcs}
}

// firstRoundsEnded: the first discovery and the first usage and storage polls have ended,
// whether or not they worked (a poll skipped for lack of prometheus counts).
func (r *runtime) firstRoundsEnded() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.disc.ended && r.usageOut.polled && r.storageOut.polled
}

// rowSources are the sources of the row: the cluster and the metrics.
func (r *runtime) rowSources(now time.Time) []model.SourceStatus {
	return []model.SourceStatus{r.kubeSource(now), r.promSource(now)}
}

// kubeSource is the informers' source: ok while they have synced and the API server answers.
func (r *runtime) kubeSource(now time.Time) model.SourceStatus {
	s := model.SourceStatus{Name: sourceKubernetes, OK: r.cl.HasSynced(), CheckedAt: now.UTC()}
	switch {
	case r.isUnavailable(now):
		s.OK, s.Error = false, upstream.CategoryUnreachable
	case !s.OK:
		s.Error = "informers not synced"
	}
	return s
}

// promSource is the metrics source: how the usage and storage polls ended, worst first.
func (r *runtime) promSource(now time.Time) model.SourceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s := model.SourceStatus{Name: sourcePrometheus, OK: true, CheckedAt: now.UTC()}
	if !r.usageOut.polled && !r.storageOut.polled {
		s.OK, s.Error = false, "not polled yet"
		return s
	}
	for _, o := range []outcome{r.usageOut, r.storageOut} {
		if o.polled && o.err != nil {
			s.OK, s.Error, s.CheckedAt = false, upstream.Category(o.err), o.at.UTC()
			return s
		}
	}
	return s
}

// publishLoop rebuilds the dirty topics every t.tick.
func (r *runtime) publishLoop() { r.every(r.t.tick, r.round) }

// round publishes every dirty topic whose inputs are ready; one that is not stays dirty. While
// the API server is unreachable nothing is published (the row is hidden, and the cached state
// cannot have changed).
func (r *runtime) round() {
	r.watchEndpoints()
	if r.isUnavailable(r.h.Now()) {
		return
	}
	d := r.dirty.Swap(0)
	if d&dirtyCluster != 0 && !r.publishCluster() {
		r.mark(dirtyCluster)
	}
	if d&dirtyStorage != 0 && !r.publishStorage() {
		r.mark(dirtyStorage)
	}
	if d&dirtyApps != 0 && !r.publishApps() {
		r.mark(dirtyApps)
	}
}

// The publish gates: each topic waits for its own inputs, because a dynamic informer never gates
// HasSynced. A resource discovery says is absent (or whose group failed) counts as listed.

func (r *runtime) clusterReady() bool {
	return r.cl.HasSynced() && r.cl.Listed(kube.GVRCertificates) && r.cl.Listed(kube.GVRExternalSecrets)
}

func (r *runtime) storageReady() bool {
	r.mu.RLock()
	polled := r.storageOut.polled
	r.mu.RUnlock()
	return r.cl.HasSynced() && r.cl.Listed(kube.GVRCNPGClusters) && polled
}

func (r *runtime) appsReady() bool {
	if !r.cl.HasSynced() || !r.cl.Listed(kube.GVRHTTPRoutes) || !r.endpointsReady() {
		return false
	}
	// Ingresses are read only on request, and may be refused: apps waits for them for as long as
	// the informers get to sync, then publishes without them (and the row is degraded).
	return !r.cfg.Apps.Ingress || r.cl.Listed(kube.GVRIngresses) || r.h.Now().Sub(r.started) >= r.t.typedSyncTimeout
}

func (r *runtime) publishCluster() bool {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	if !r.clusterReady() {
		return false
	}
	now := r.h.Now()
	data := buildCluster(clusterInput{c: r.cl, conv: r.cfg.Conventions, usage: r.usageSnapshot(), links: r.h.Links(), now: now})
	r.mu.Lock()
	r.cluster = &data
	r.mu.Unlock()
	srcs := append([]model.SourceStatus{r.kubeSource(now), r.promSourceOf(true, now)}, r.notes(dirtyCluster, now)...)
	r.h.Publish(topicCluster, data, srcs)
	return true
}

func (r *runtime) publishStorage() bool {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	if !r.storageReady() {
		return false
	}
	now := r.h.Now()
	r.mu.RLock()
	snap := r.storage
	r.mu.RUnlock()
	data := buildStorage(storageInput{c: r.cl, snap: snap, cfg: r.cfg, links: r.h.Links(), now: now})
	r.mu.Lock()
	r.storageD = &data
	r.mu.Unlock()
	srcs := append([]model.SourceStatus{r.kubeSource(now), r.promSourceOf(false, now)}, r.notes(dirtyStorage, now)...)
	r.h.Publish(topicStorage, data, srcs)
	return true
}

func (r *runtime) publishApps() bool {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	if !r.appsReady() {
		return false
	}
	now := r.h.Now()
	ep := r.endpoints()
	data := buildApps(appsInput{c: r.cl, cfg: r.cfg, ep: ep, infos: namespaceInfosOf(r.cl.ListNamespaces(), r.cfg.Conventions)})
	srcs := append([]model.SourceStatus{r.kubeSource(now)}, r.notes(dirtyApps, now)...)
	srcs = append(srcs, ep.SourceStatuses()...)
	r.h.Publish(topicApps, data, srcs)
	return true
}

// promSourceOf is the prometheus source of one topic: the usage poll for the cluster, the
// storage poll for storage.
func (r *runtime) promSourceOf(usage bool, now time.Time) model.SourceStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	o := r.storageOut
	if usage {
		o = r.usageOut
	}
	s := model.SourceStatus{Name: sourcePrometheus, OK: true, CheckedAt: o.at.UTC()}
	switch {
	case !o.polled:
		s.OK, s.Error, s.CheckedAt = false, "not polled yet", now.UTC()
	case o.err != nil:
		s.OK, s.Error = false, upstream.Category(o.err)
	}
	return s
}

// builtPayloads are the last published cluster and storage payloads, which the overview derives
// from; either is nil before its first publish.
func (r *runtime) builtPayloads() (*model.ClusterData, *model.StorageData) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cluster, r.storageD
}

// clusterInputNow is the input buildCluster and the pod detail read, for the current moment.
func (r *runtime) clusterInput() clusterInput {
	return clusterInput{c: r.cl, conv: r.cfg.Conventions, usage: r.usageSnapshot(), links: r.h.Links(), now: r.h.Now()}
}

// noEndpoints is what the plugin sees while the gatus plugin is not running: it knows no check,
// never changes, and keeps the gatus chip on the Apps page, reading "not configured".
type noEndpoints struct{}

// noGatus is the one stub, so its identity never differs between two lookups.
var noGatus = &noEndpoints{}

func (*noEndpoints) ByHost(string) (model.Health, bool) { return "", false }
func (*noEndpoints) ByName(string) (up, known bool)     { return false, false }
func (*noEndpoints) Revision() uint64                   { return 0 }
func (*noEndpoints) SourceStatuses() []model.SourceStatus {
	return []model.SourceStatus{{Name: "gatus", Error: upstream.CategoryNotConfigured, CheckedAt: time.Now().UTC()}}
}

var _ svc.EndpointHealth = (*noEndpoints)(nil)

// lookupEndpoints is the gatus plugin's service with its identity (which differs after every
// restart of gatus), or the stub and false while gatus does not run.
func (r *runtime) lookupEndpoints() (id any, ep svc.EndpointHealth, running bool) {
	if v, ok := r.h.Service("gatus"); ok {
		if ep, ok := v.(svc.EndpointHealth); ok {
			return v, ep, true
		}
	}
	return noGatus, noGatus, false
}

// endpoints is gatus's svc.EndpointHealth while it runs, otherwise the stub. It is looked up per
// call: gatus restarts on a reload and its service changes with it.
func (r *runtime) endpoints() svc.EndpointHealth {
	_, ep, _ := r.lookupEndpoints()
	return ep
}

// endpointsReady is the apps gate: gatus is absent, or its first poll has finished (Revision is
// above 0 then, whether the poll succeeded or not).
func (r *runtime) endpointsReady() bool {
	_, ep, running := r.lookupEndpoints()
	return !running || ep.Revision() > 0
}

// watchEndpoints marks apps when the gatus service or its Revision differs from the last
// round's: gatus cannot mark this plugin's topics, and a callback registered once would not
// survive a reload of gatus, so the plugin compares on its own tick.
func (r *runtime) watchEndpoints() {
	id, ep, _ := r.lookupEndpoints()
	rev := ep.Revision()
	if id == r.seenEndpoints && rev == r.seenRevision {
		return
	}
	r.seenEndpoints, r.seenRevision = id, rev
	r.mark(dirtyApps)
}
