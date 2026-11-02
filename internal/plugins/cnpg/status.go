package cnpg

import (
	"fmt"
	"slices"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// The messages of the rows the plugin reports. They name no URL, host or secret.
const (
	msgWaitingForKubernetes = "waiting for the kubernetes service"
	msgListing              = "listing the CloudNativePG clusters"
	msgAbsent               = "the CloudNativePG CRD is not installed"
	msgWaitingForPoll       = "waiting for the first backup-age poll"
)

// sig is everything one evaluation reads: the round does nothing when it equals the last one's.
// The cluster list follows informer events (GVRRevision) and the backup ages follow the polls
// (gen); nothing else moves the payload.
type sig struct {
	cl         any // the kubernetes service
	rev        uint64
	gen        uint64
	synced     bool
	has        bool
	listed     bool
	failed     bool // the CRD's group failed discovery
	refused    bool
	refusedCat string
}

// watchLoop evaluates at once and then every tm.watch, and ends with the plugin.
func (p *Plugin) watchLoop(rt *runtime) {
	t := time.NewTicker(p.tm.watch)
	defer t.Stop()
	for {
		p.evaluate(rt)
		select {
		case <-rt.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// evaluate is one look at the kubernetes service. The plugin acts on what it sees:
//
//   - no service (kubernetes not running, unavailable or in error): the row is starting and
//     nothing is published; the loop looks again next round, so recovery is the service coming
//     back (Start does not fail without it);
//   - the informers not synced, or the CRD neither listed nor refused: starting;
//   - the CRD served by nobody: unavailable (hidden), with no payload;
//   - its group failing discovery, or its list refused: degraded, the topic published with no
//     clusters and a cnpg note naming the category;
//   - otherwise, once the first backup-age poll has ended: the clusters published, and the row
//     healthy, or degraded when the Prometheus query failed.
//
// The row turns visible only after the payload that justifies it is published, and turns hidden
// before the payload is emptied, so a hidden row is never the latest event's last word.
func (p *Plugin) evaluate(rt *runtime) {
	cl := rt.cluster()
	if cl == nil {
		rt.seenCl, rt.last = nil, sig{}
		p.setView(view{state: plugin.StateStarting, message: msgWaitingForKubernetes})
		return
	}
	if cl != rt.seenCl {
		rt.seenCl = cl
		cl.Watch(gvr)
	}

	s := p.read(rt, cl)
	absent := s.synced && s.listed && !s.has && !s.failed
	if !absent && s.synced && (s.listed || s.refused) {
		p.pollIfDue(rt)
	}
	p.mu.Lock()
	s.gen = p.gen
	p.mu.Unlock()
	if s == rt.last {
		return
	}
	rt.last = s

	switch {
	case !s.synced || !(s.listed || s.refused):
		p.setView(view{state: plugin.StateStarting, message: msgListing})
	case absent:
		p.hide(rt, view{state: plugin.StateUnavailable, message: msgAbsent})
	default:
		p.publish(rt, cl, s)
	}
}

// read is the cluster's answer about the resource.
func (p *Plugin) read(rt *runtime, cl svc.Cluster) sig {
	s := sig{cl: cl, rev: cl.GVRRevision(gvr), synced: cl.Synced(), has: cl.Has(gvr), listed: cl.Listed(gvr)}
	s.failed = slices.Contains(cl.FailedGroups(), gvr.GroupVersion())
	s.refusedCat, s.refused = cl.Refused(gvr)
	return s
}

// hide turns the row hidden, then empties what the plugin published.
func (p *Plugin) hide(rt *runtime, v view) {
	p.setView(v)
	p.mu.Lock()
	had := p.published
	p.published = false
	p.mu.Unlock()
	if had {
		rt.h.Publish(topicClusters, model.CNPGClustersData{Clusters: []model.CNPGCluster{}}, p.sources(rt.h.Now(), nil))
	}
	rt.warn("")
}

// publish builds the payload and publishes it, then decides the row. Before the first poll ended
// the row waits: starting is a visible state, and a payload sent now would show ages that are
// not there yet.
func (p *Plugin) publish(rt *runtime, cl svc.Cluster, s sig) {
	p.mu.Lock()
	polled, cat, ages := p.polled, p.cat, p.ages
	p.mu.Unlock()
	if !polled {
		p.setView(view{state: plugin.StateStarting, message: msgWaitingForPoll})
		return
	}

	now := rt.h.Now()
	var note *model.SourceStatus
	state, msg := plugin.StateHealthy, ""
	data := model.CNPGClustersData{Clusters: []model.CNPGCluster{}}
	switch {
	case s.failed:
		// A group whose discovery failed keeps its last answer in the cluster, so its cached
		// clusters are still listed; the row says why the list may be stale.
		data.Clusters = clustersOf(cl.List(gvr), ages, rt.h.Links())
		note = &model.SourceStatus{Name: sourceCNPG, Error: upstream.CategoryUnreachable, CheckedAt: now.UTC()}
		state, msg = plugin.StateDegraded, "discovery failed for: "+sourceCNPG
		rt.warn("discovery:" + upstream.CategoryUnreachable)
	case s.refused:
		note = &model.SourceStatus{Name: sourceCNPG, Error: s.refusedCat, CheckedAt: now.UTC()}
		state, msg = plugin.StateDegraded, fmt.Sprintf("cannot list: %s (%s)", sourceCNPG, s.refusedCat)
		rt.warn("list:" + s.refusedCat)
	default:
		data.Clusters = clustersOf(cl.List(gvr), ages, rt.h.Links())
		rt.warn("")
		// A failing query degrades the row; a Prometheus that is not running is only a note on
		// the envelope (the age is optional, as it is on the kubernetes plugin's rows).
		if cat != "" && cat != upstream.CategoryNotConfigured {
			state, msg = plugin.StateDegraded, sourcePrometheus+": "+cat
		}
	}
	srcs := p.sources(now, note)
	rt.h.Publish(topicClusters, data, srcs)
	p.mu.Lock()
	p.published = true
	p.mu.Unlock()
	p.setView(view{state: state, message: msg, sources: srcs})
}

// sources are the envelope's sources: the kubernetes plugin, which answered, the age query's
// outcome and the note of a CRD that could not be read.
func (p *Plugin) sources(now time.Time, note *model.SourceStatus) []model.SourceStatus {
	p.mu.Lock()
	polled, cat, at := p.polled, p.cat, p.at
	p.mu.Unlock()
	out := []model.SourceStatus{{Name: sourceKubernetes, OK: true, CheckedAt: now.UTC()}}
	prom := model.SourceStatus{Name: sourcePrometheus, OK: cat == "", Error: cat, CheckedAt: at.UTC()}
	if !polled {
		prom.OK, prom.Error, prom.CheckedAt = false, "not polled yet", now.UTC()
	}
	out = append(out, prom)
	if note != nil {
		out = append(out, *note)
	}
	return out
}

func (p *Plugin) setView(v view) {
	p.mu.Lock()
	p.view = v
	p.mu.Unlock()
}

// warn logs a failure of the CRD once per change: the category only, since the detail names the
// API server. "" clears it.
func (rt *runtime) warn(key string) {
	if key == rt.warned {
		return
	}
	rt.warned = key
	if key != "" {
		rt.h.Logger().Warn("CloudNativePG clusters cannot be read", "reason", key)
	}
}
