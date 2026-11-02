package kubernetes

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

// crd is a custom resource the plugin reads, and how its absence is reported.
type crd struct {
	gvr  schema.GroupVersionResource
	kind kube.Kind
	// name is the source note ("not_configured") a missing one adds to the topic that carries its
	// data, and what the row names when it is degraded.
	name string
	// optional silences the note and the degraded row for an absent CRD (plugins.kubernetes.optional.*).
	optional bool
	// wantedWhenAbsent says that an absent CRD makes the row degraded and adds the note. The CNPG
	// group of the transitional storage extras is wanted only when its discovery is broken: an
	// absent one is not a note.
	wantedWhenAbsent bool
	topic            uint32 // the topic that carries its data (dirtyCluster, dirtyStorage, dirtyApps)
}

// crds are the resources the plugin watches, from the settings.
func (r *runtime) crds() []crd {
	out := []crd{
		{gvr: kube.GVRCertificates, kind: kube.KindCertificates, name: "cert-manager", optional: r.cfg.Optional.Certificates, wantedWhenAbsent: true, topic: dirtyCluster},
		{gvr: kube.GVRExternalSecrets, kind: kube.KindExtSecrets, name: "external-secrets", optional: r.cfg.Optional.ExternalSecrets, wantedWhenAbsent: true, topic: dirtyCluster},
		{gvr: kube.GVRHTTPRoutes, kind: kube.KindHTTPRoutes, name: "gateway-api", optional: r.cfg.Optional.HTTPRoutes, wantedWhenAbsent: true, topic: dirtyApps},
		{gvr: kube.GVRCNPGClusters, kind: kube.KindCNPG, name: "cnpg", optional: true, topic: dirtyStorage},
	}
	if r.cfg.Apps.Ingress {
		out = append(out, crd{gvr: kube.GVRIngresses, kind: kube.KindIngresses, name: "ingress", wantedWhenAbsent: true, topic: dirtyApps})
	}
	return out
}

func (r *runtime) wanted() map[schema.GroupVersionResource]kube.Kind {
	out := map[schema.GroupVersionResource]kube.Kind{}
	for _, c := range r.crds() {
		out[c.gvr] = c.kind
	}
	return out
}

// discState is what the last discovery found.
type discState struct {
	ended  bool     // a discovery has run, whether or not it worked
	failed []string // the group versions of the wanted resources that failed, sorted
	// retry says the last discovery left a wanted resource unknown (it failed altogether, or for
	// a wanted group) and is to be repeated.
	retry bool
}

// discoveryBackoff is the wait before retry number n (1 for the first) of a failed discovery: the
// probe period, doubled each time, up to the discovery period.
func discoveryBackoff(t timing, n int) time.Duration {
	d := t.probe
	for i := 1; i < n && d < t.discovery; i++ {
		d *= 2
	}
	return min(d, max(t.discovery, t.probe))
}

// discoveryLoop rediscovers the served resources at Start, every t.discovery, at once when a
// consumer asks for a resource through svc.Cluster.Watch or the probe sees the API server answer
// again after a discovery that failed, and, while the last discovery left a wanted group
// unknown, with a growing wait (see discoveryBackoff). The wait runs on the host's clock, checked
// every t.tick. Only the attempts that wait starts count towards the growth: a discovery that a
// consumer or the probe asked for, failing, does not push the next retry further out.
func (r *runtime) discoveryLoop() {
	periodic := time.NewTicker(r.t.discovery)
	defer periodic.Stop()
	check := time.NewTicker(r.t.tick)
	defer check.Stop()
	retries := 0 // the retries the backoff timer has started since the last success
	var retryAt time.Time
	for {
		if r.discover() {
			retryAt = r.h.Now().Add(discoveryBackoff(r.t, retries+1))
		} else {
			retries, retryAt = 0, time.Time{}
		}
		for again := false; !again; {
			select {
			case <-r.ctx.Done():
				return
			case <-periodic.C:
				again = true
			case <-r.cl.DiscoveryRequests():
				again = true
			case <-r.rediscover:
				again = true
			case <-check.C:
				if again = !retryAt.IsZero() && !r.h.Now().Before(retryAt); again {
					retries++
				}
			}
		}
	}
}

// discover runs one discovery and records its outcome; it reports that the outcome left a wanted
// resource unknown, so the loop retries. A failing group that is not one the plugin wants (a
// metrics-server APIService that is down, any aggregated API) is logged at INFO, once per
// change, and affects nothing.
func (r *runtime) discover() (retry bool) {
	err := r.cl.Discover(r.wanted())
	if r.ctx.Err() != nil {
		return false
	}
	failed := gvStrings(r.cl.FailedGroups())
	var gd *kube.ErrGroupDiscoveryFailed
	retry = len(failed) > 0 || (err != nil && !errors.As(err, &gd))
	r.mu.Lock()
	first := !r.disc.ended
	changed := first || !slices.Equal(failed, r.disc.failed)
	r.disc = discState{ended: true, failed: failed, retry: retry}
	// A resource that appears after the first discovery has only now got its informer, so its
	// time to list starts now. Every registered resource has the clock, not only the wanted
	// ones: a consumer's resource (svc.Cluster.Watch) is judged the same way.
	now := r.h.Now()
	for _, gvr := range r.cl.Registered() {
		has := r.cl.Has(gvr)
		if has && !r.had[gvr] && !first {
			r.appeared[gvr] = r.markNowLocked(now)
		}
		r.had[gvr] = has
	}
	r.mu.Unlock()

	switch {
	case errors.As(err, &gd):
		var unwanted []string
		for gv := range gd.Groups {
			if !slices.Contains(failed, gv.String()) {
				unwanted = append(unwanted, gv.String())
			}
		}
		slices.Sort(unwanted)
		if r.changed("discovery:unwanted", strings.Join(unwanted, ",")) && len(unwanted) > 0 {
			r.h.Logger().Info("discovery failed for API groups dash does not read", "groups", unwanted)
		}
		if len(failed) > 0 && r.changed("discovery:wanted", strings.Join(failed, ",")) {
			r.h.Logger().Warn("discovery failed for API groups dash reads", "groups", failed)
		}
	case err != nil:
		r.changed("discovery:unwanted", "")
		if r.changed("discovery:wanted", "all:"+kube.Category(err)) {
			r.h.Logger().Warn("discovery failed", "category", kube.Category(err), "error", r.h.ScrubLog(err.Error()))
		}
	default:
		r.changed("discovery:unwanted", "")
		r.changed("discovery:wanted", "")
	}
	if changed {
		r.mark(dirtyAll)
	}
	return retry
}

func gvStrings(gvs []schema.GroupVersion) []string {
	out := make([]string, len(gvs))
	for i, gv := range gvs {
		out[i] = gv.String()
	}
	return out
}

// crdFailure is why a wanted CRD's data is missing from its topic although it should be there.
type crdFailure struct {
	discovery bool   // discovery failed for its group
	list      string // the informer has not listed within the sync timeout: the category of its watch error
}

func (f crdFailure) any() bool { return f.discovery || f.list != "" }

// category is what a viewer reads in the topic's source note.
func (f crdFailure) category() string {
	if f.list != "" {
		return f.list
	}
	return upstream.CategoryUnreachable
}

// crdState is the state of one CRD at the last discovery: absent (served by nobody), or failed
// (its group's discovery failed, or it is served but its list never completes: a refused list,
// a dead conversion webhook).
func (r *runtime) crdState(c crd) (absent bool, fail crdFailure) {
	r.mu.RLock()
	d := r.disc
	r.mu.RUnlock()
	if !d.ended {
		return false, crdFailure{}
	}
	if slices.Contains(d.failed, c.gvr.GroupVersion().String()) {
		return false, crdFailure{discovery: true}
	}
	if !r.cl.Has(c.gvr) {
		return true, crdFailure{}
	}
	if cat, ok := r.refused(c.gvr); ok {
		return false, crdFailure{list: cat}
	}
	return false, crdFailure{}
}

// refused reports a served resource whose list never completes: it has been trying for the sync
// timeout of API-server up-time (stuck), and the category says why, that of the watch's error or
// unreachable when it reported none. It is svc.Cluster.Refused.
func (r *runtime) refused(gvr schema.GroupVersionResource) (category string, ok bool) {
	if !r.stuck(gvr) {
		return "", false
	}
	if err := r.cl.WatchError(gvr); err != nil {
		return kube.Category(err), true
	}
	return upstream.CategoryUnreachable, true
}

// stuck reports a served resource whose informer has not listed within the sync timeout of
// API-server up-time (upSince), counted from the start of the plugin, or from the moment the
// resource appeared when that was later. The clock pauses while the probe fails.
func (r *runtime) stuck(gvr schema.GroupVersionResource) bool {
	if !r.cl.Has(gvr) || r.cl.Listed(gvr) {
		return false
	}
	r.mu.RLock()
	m, ok := r.appeared[gvr]
	r.mu.RUnlock()
	if !ok || m.at.Before(r.started) {
		m = clockMark{at: r.started}
	}
	return r.upSince(r.h.Now(), m) >= r.t.typedSyncTimeout
}

// listedOrStuck is the publish gate of one resource: its first list is in the cache, or it will
// not come in time and the topic publishes without it.
func (r *runtime) listedOrStuck(gvr schema.GroupVersionResource) bool {
	return r.cl.Listed(gvr) || r.stuck(gvr)
}

// stuckSignature names the resources that are stuck now, so the publisher can rebuild the topics
// when one stops being (a list that completes with no object sends no event).
func (r *runtime) stuckSignature() string {
	var out []string
	for _, c := range r.crds() {
		if r.stuck(c.gvr) {
			out = append(out, c.name)
		}
	}
	return strings.Join(out, ",")
}

// silenced is a failure the settings say to ignore: plugins.kubernetes.optional.* marks the
// resource as one the cluster may lack.
func (c crd) silenced() bool { return c.optional && c.wantedWhenAbsent }

// notes are the source notes of the topic(s) in mask: one per CRD the topic carries data of that
// is not there ("not_configured") or whose data is missing (the category of the failure).
func (r *runtime) notes(mask uint32, now time.Time) []model.SourceStatus {
	var out []model.SourceStatus
	for _, c := range r.crds() {
		if c.topic&mask == 0 {
			continue
		}
		absent, fail := r.crdState(c)
		switch {
		case fail.any():
			out = append(out, model.SourceStatus{Name: c.name, Error: fail.category(), CheckedAt: now.UTC()})
		case absent && c.wantedWhenAbsent && !c.optional:
			out = append(out, model.SourceStatus{Name: c.name, Error: upstream.CategoryNotConfigured, CheckedAt: now.UTC()})
		}
	}
	return out
}

// degraded names each wanted group that is missing or failed, "" when none is. A group the
// settings mark optional never makes the row degraded, missing or failed.
func (r *runtime) degraded() string {
	var missing, failed, unlisted []string
	for _, c := range r.crds() {
		absent, fail := r.crdState(c)
		switch {
		case fail.discovery && !c.silenced():
			failed = append(failed, c.name)
		case fail.list != "" && !c.silenced():
			unlisted = append(unlisted, fmt.Sprintf("%s (%s)", c.name, fail.list))
		case absent && c.wantedWhenAbsent && !c.optional:
			missing = append(missing, c.name)
		}
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("not installed: %s (set plugins.kubernetes.optional.* to silence)", strings.Join(missing, ", ")))
	}
	if len(failed) > 0 {
		parts = append(parts, fmt.Sprintf("discovery failed for: %s", strings.Join(failed, ", ")))
	}
	if len(unlisted) > 0 {
		parts = append(parts, fmt.Sprintf("cannot list: %s", strings.Join(unlisted, ", ")))
	}
	return strings.Join(parts, "; ")
}
