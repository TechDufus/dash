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
	// needsList marks a resource that is served everywhere but may be refused to this client (the
	// Ingresses need RBAC the default role lacks): when its informer has not listed within the
	// sync timeout it counts as failed, and the topic publishes without it.
	needsList bool
}

// crds are the resources the plugin watches, from the settings.
func (r *runtime) crds() []crd {
	out := []crd{
		{kube.GVRCertificates, kube.KindCertificates, "cert-manager", r.cfg.Optional.Certificates, true, dirtyCluster, false},
		{kube.GVRExternalSecrets, kube.KindExtSecrets, "external-secrets", r.cfg.Optional.ExternalSecrets, true, dirtyCluster, false},
		{kube.GVRHTTPRoutes, kube.KindHTTPRoutes, "gateway-api", r.cfg.Optional.HTTPRoutes, true, dirtyApps, false},
		{kube.GVRCNPGClusters, kube.KindCNPG, "cnpg", true, false, dirtyStorage, false},
	}
	if r.cfg.Apps.Ingress {
		out = append(out, crd{kube.GVRIngresses, kube.KindIngresses, "ingress", false, true, dirtyApps, true})
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
}

// discoveryLoop rediscovers the served resources at Start, every t.discovery, and at once when a
// consumer asks for a resource through svc.Cluster.Watch.
func (r *runtime) discoveryLoop() {
	t := time.NewTicker(r.t.discovery)
	defer t.Stop()
	for {
		r.discover()
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
		case <-r.cl.DiscoveryRequests():
		}
	}
}

// discover runs one discovery and records its outcome. A failing group that is not one the plugin
// wants (a metrics-server APIService that is down, any aggregated API) is logged at INFO, once
// per change, and affects nothing.
func (r *runtime) discover() {
	err := r.cl.Discover(r.wanted())
	if r.ctx.Err() != nil {
		return
	}
	failed := gvStrings(r.cl.FailedGroups())
	r.mu.Lock()
	changed := !r.disc.ended || !slices.Equal(failed, r.disc.failed)
	r.disc = discState{ended: true, failed: failed}
	r.mu.Unlock()

	var gd *kube.ErrGroupDiscoveryFailed
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
}

func gvStrings(gvs []schema.GroupVersion) []string {
	out := make([]string, len(gvs))
	for i, gv := range gvs {
		out[i] = gv.String()
	}
	return out
}

// crdState is the state of one CRD at the last discovery: absent (served by nobody), or its
// group's discovery failed.
func (r *runtime) crdState(c crd) (absent, failed bool) {
	r.mu.RLock()
	d := r.disc
	r.mu.RUnlock()
	if !d.ended {
		return false, false
	}
	if slices.Contains(d.failed, c.gvr.GroupVersion().String()) {
		return false, true
	}
	if absent := !r.cl.Has(c.gvr); absent || !c.needsList {
		return absent, false
	}
	return false, r.stuck(c)
}

// stuck reports a served resource whose informer has not listed within the sync timeout.
func (r *runtime) stuck(c crd) bool {
	return !r.cl.Listed(c.gvr) && r.h.Now().Sub(r.started) >= r.t.typedSyncTimeout
}

// notes are the source notes of the topic(s) in mask: one per CRD the topic carries data of that
// is not there ("not_configured") or whose discovery failed (the category), unless it is optional.
func (r *runtime) notes(mask uint32, now time.Time) []model.SourceStatus {
	var out []model.SourceStatus
	for _, c := range r.crds() {
		if c.topic&mask == 0 {
			continue
		}
		absent, failed := r.crdState(c)
		switch {
		case failed:
			cat := upstream.CategoryUnreachable
			if err := r.cl.WatchError(c.gvr); err != nil && c.needsList {
				cat = kube.Category(err)
			}
			out = append(out, model.SourceStatus{Name: c.name, Error: cat, CheckedAt: now.UTC()})
		case absent && c.wantedWhenAbsent && !c.optional:
			out = append(out, model.SourceStatus{Name: c.name, Error: upstream.CategoryNotConfigured, CheckedAt: now.UTC()})
		}
	}
	return out
}

// degraded names each wanted group that is missing or failed, "" when none is.
func (r *runtime) degraded() string {
	var missing, failed []string
	for _, c := range r.crds() {
		absent, fail := r.crdState(c)
		switch {
		case fail:
			failed = append(failed, c.name)
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
	return strings.Join(parts, "; ")
}
