package flux

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// loop runs a round now and then every t.tick until the plugin stops.
func (r *runtime) loop() {
	t := time.NewTicker(r.t.tick)
	defer t.Stop()
	for {
		r.round()
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// round compares the kubernetes service and what it says about the five resources with the last
// round's and publishes the topic when they differ. A plugin cannot be called back when the
// service changes (a callback registered once would not survive a reload, which builds a new
// service), and a list that completes with no object sends no informer event, so the plugin
// compares on its own tick.
//
//   - Without a service nothing is published: what the plugin last built describes a cluster
//     nobody can reach, so it stops contributing to the overview and its row says it waits.
//   - Until the informers have synced and every resource is listed, absent or refused, nothing
//     is published: the topic is never built from a partial list.
func (r *runtime) round() {
	c := r.lookup()
	if c == nil {
		if r.seen.svc != nil || !r.haveSeen {
			r.seen, r.haveSeen = signature{}, true
			r.withdraw()
		}
		return
	}
	r.watch(c)
	v := look(c)
	s := signatureOf(c, v)
	if r.haveSeen && s == r.seen {
		return
	}
	r.seen, r.haveSeen = s, true
	if !v.ready() {
		return
	}
	r.publish(c, v)
}

// withdraw forgets what the plugin published: its contributions stop and its row is no longer
// "ready" for a payload built from a service that is gone. The topic itself is left alone.
func (r *runtime) withdraw() {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	r.mu.Lock()
	r.pub, r.last = published{}, nil
	r.mu.Unlock()
}

// publish builds the topic from the service and publishes it. A cluster with no Flux gets an
// empty payload on the way into the unavailable row, so the supervisor's resend of the last
// event, when the row turns visible again, can never be the stale list; the row turns visible
// only after the rebuilt payload is published (status).
func (r *runtime) publish(c svc.Cluster, v view) {
	r.pubMu.Lock()
	defer r.pubMu.Unlock()
	srcs := sources(v, r.h.Now())
	if v.none() {
		r.h.Publish(topicObjects, r.build(nil), srcs)
		r.mu.Lock()
		r.pub, r.last = published{ok: true, svc: c, cls: classNone}, nil
		r.mu.Unlock()
		return
	}
	data := r.build(c)
	unknown := map[model.FluxKind]bool{}
	for _, k := range kinds {
		if v.failedKind(k) {
			unknown[k] = true
		}
	}
	r.h.Publish(topicObjects, data, srcs)
	r.mu.Lock()
	r.pub, r.last = published{ok: true, svc: c, cls: classReady}, &built{data: data, unknown: unknown}
	r.mu.Unlock()
}
