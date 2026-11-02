package flux

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// view is what the kubernetes service says about the five resources at one moment.
type view struct {
	synced bool
	// pending says some resource is neither listed nor refused yet: the topic is not built from
	// a partial list.
	pending bool
	// served counts the resources the API server serves.
	served int
	// refused are the served resources whose list the plugin may not make, with the category of
	// the watch error.
	refused map[schema.GroupVersionResource]string
	// failed are the names of the Flux API groups whose discovery failed, sorted: their objects
	// are missing from the topic although the cluster has them, and Listed says nothing of it.
	failed   []string
	failedGV map[schema.GroupVersion]bool
}

// look reads the service once.
func look(c svc.Cluster) view {
	v := view{synced: c.Synced(), refused: map[schema.GroupVersionResource]string{}, failedGV: map[schema.GroupVersion]bool{}}
	for _, gv := range c.FailedGroups() {
		for _, g := range gvrs {
			if g.GroupVersion() == gv {
				v.failedGV[gv] = true
				if !slices.Contains(v.failed, gv.Group) {
					v.failed = append(v.failed, gv.Group)
				}
				break
			}
		}
	}
	slices.Sort(v.failed)
	for _, g := range gvrs {
		cat, refused := c.Refused(g)
		if refused {
			v.refused[g] = cat
		}
		if !c.Listed(g) && !refused {
			v.pending = true
		}
		if c.Has(g) {
			v.served++
		}
	}
	return v
}

// ready reports that the topic can be built: the typed informers have synced and every resource
// is listed, absent or refused.
func (v view) ready() bool { return v.synced && !v.pending }

// none reports that the cluster has no Flux: every resource has been evaluated, none is served,
// and no group failed discovery (a group that did not answer is not served either, but its
// resources may well exist).
func (v view) none() bool { return v.ready() && v.served == 0 && len(v.failed) == 0 }

// failedKind reports that the list of kind is unknown: refused, or its group failed discovery.
func (v view) failedKind(kind model.FluxKind) bool {
	g, ok := gvrOf(kind)
	if !ok {
		return false
	}
	_, refused := v.refused[g]
	return refused || v.failedGV[g.GroupVersion()]
}

// signature is the state a round compares with the last one: the service, whether it has
// synced, the listed, served and refused state of each resource, the revision of the five
// informers and the failed groups. A list that completes empty sends no informer event, so the
// per-resource flags are part of it.
type signature struct {
	svc    svc.Cluster
	synced bool
	flags  string
	rev    uint64
	failed string
}

func signatureOf(c svc.Cluster, v view) signature {
	var b strings.Builder
	for _, g := range gvrs {
		listed, served := c.Listed(g), c.Has(g)
		cat, refused := v.refused[g]
		fmt.Fprintf(&b, "%t,%t,%t,%s;", listed, served, refused, cat)
	}
	return signature{svc: c, synced: v.synced, flags: b.String(), rev: c.GVRRevision(gvrs...), failed: strings.Join(v.failed, ",")}
}

// sources are the sources of the topic and the row: the cluster (ok, or the category while the
// informers have not synced), a flux source that is unreachable while a Flux API group's
// discovery fails, and one note per resource that cannot be listed, named by the resource and
// carrying the category of the error.
func sources(v view, now time.Time) []model.SourceStatus {
	k := model.SourceStatus{Name: sourceKubernetes, OK: v.synced, CheckedAt: now.UTC()}
	if !k.OK {
		k.Error = "informers not synced"
	}
	out := []model.SourceStatus{k}
	if len(v.failed) > 0 {
		out = append(out, model.SourceStatus{Name: sourceFlux, Error: upstream.CategoryUnreachable, CheckedAt: now.UTC()})
	}
	for _, g := range gvrs {
		if cat, ok := v.refused[g]; ok {
			out = append(out, model.SourceStatus{Name: g.Resource, Error: cat, CheckedAt: now.UTC()})
		}
	}
	return out
}

// problems is the text of a degraded row: the groups whose discovery failed and the resources
// that cannot be listed. Categories and names of API resources only.
func (v view) problems() string {
	var parts []string
	if len(v.failed) > 0 {
		parts = append(parts, "discovery failed for: "+strings.Join(v.failed, ", "))
	}
	var refused []string
	for _, g := range gvrs {
		if cat, ok := v.refused[g]; ok {
			refused = append(refused, fmt.Sprintf("%s (%s)", g.Resource, cat))
		}
	}
	if len(refused) > 0 {
		slices.Sort(refused)
		parts = append(parts, "cannot list: "+strings.Join(refused, ", "))
	}
	return strings.Join(parts, "; ")
}

// status is design 6.3, in the order the rows are decided:
//
//   - starting while there is no kubernetes service (kubernetes is in error: nothing is claimed
//     about the sources, and nothing is published);
//   - starting until the informers have synced and every resource is listed, absent or refused;
//   - unavailable (hidden) when the cluster serves none of the five;
//   - unavailable (hidden) while the topic holds a payload that is not the one this service
//     builds now (the empty payload of a cluster that had no Flux, the payload of another
//     service), and starting before the first one: a row that turned visible then would show it;
//   - degraded when a Flux API group failed discovery or a resource cannot be listed;
//   - healthy otherwise, some of the five being absent included.
//
// The messages carry categories and API resource names only.
func (r *runtime) status() plugin.Status {
	c := r.lookup()
	if c == nil {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the kubernetes service"}
	}
	now := r.h.Now()
	v := look(c)
	srcs := sources(v, now)

	r.mu.RLock()
	pub := r.pub
	r.mu.RUnlock()
	stale := pub.ok && (pub.cls != classReady || pub.svc != c)

	switch {
	case !v.synced:
		return r.waiting(stale, "informers syncing", srcs)
	case v.pending:
		return r.waiting(stale, "listing the flux resources", srcs)
	case v.none():
		return plugin.Status{State: plugin.StateUnavailable, Message: "the cluster serves no Flux resources", Sources: srcs}
	case !pub.ok || stale:
		return r.waiting(stale, "reading the flux resources", srcs)
	}
	if msg := v.problems(); msg != "" {
		return plugin.Status{State: plugin.StateDegraded, Message: msg, Sources: srcs}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: srcs}
}

// waiting is the row of a plugin that has not built yet: hidden when the hub holds a payload
// that must not be shown, starting otherwise.
func (r *runtime) waiting(stale bool, msg string, srcs []model.SourceStatus) plugin.Status {
	if stale {
		return plugin.Status{State: plugin.StateUnavailable, Message: "reading the flux resources", Sources: srcs}
	}
	return plugin.Status{State: plugin.StateStarting, Message: msg, Sources: srcs}
}
