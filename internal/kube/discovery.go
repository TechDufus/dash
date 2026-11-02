package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/cache"
)

// ErrGroupDiscoveryFailed is client-go's error for an API group whose resources could not be
// listed (an aggregated API that is down, say). Discover returns it when discovery failed for
// some groups only: the healthy groups are applied, and Groups names the failed ones, wanted or
// not.
type ErrGroupDiscoveryFailed = discovery.ErrGroupDiscoveryFailed

// dynamicState is one custom resource dash was asked to watch.
type dynamicState struct {
	kind        Kind
	rev         atomic.Uint64
	present     bool // the last successful discovery of its group lists it
	groupFailed bool // the last discovery failed for its group (present keeps the earlier answer)
	// evaluated says a Discover has applied its answer to this resource. Until then nothing is
	// known about it: it is not present, and it is not listed either (its informer may not
	// exist yet).
	evaluated bool
	inf         cache.SharedIndexInformer
	watchErr    atomic.Pointer[error] // the last error the informer's watch reported
	lister      cache.GenericLister
}

// SetDiscovery replaces the discovery client, for tests that need a discovery that fails in a
// way the fake clientset cannot (a failed group).
func (c *Cluster) SetDiscovery(d discovery.ServerResourcesInterface) {
	c.mu.Lock()
	c.disc = d
	c.mu.Unlock()
}

// AddDynamic asks for an informer of gvr, notified as kind. It is idempotent. The informer
// starts when discovery says the API server serves gvr (immediately, when it already does):
// one for a CRD that is not served starts when the CRD appears. A new gvr requests an immediate
// discovery (see DiscoveryRequests). Only typed informers gate HasSynced.
func (c *Cluster) AddDynamic(gvr schema.GroupVersionResource, kind Kind) {
	if c.register(gvr, kind) {
		select {
		case c.wake <- struct{}{}:
		default: // one request is already waiting
		}
	}
}

// Watch is AddDynamic for callers that do not name a kind: the kind of a GVR dash knows, else
// KindOther.
func (c *Cluster) Watch(gvrs ...schema.GroupVersionResource) {
	for _, gvr := range gvrs {
		kind, ok := defaultKinds[gvr]
		if !ok {
			kind = KindOther
		}
		c.AddDynamic(gvr, kind)
	}
}

// DiscoveryRequests is signalled (once, however many requests there were) when a new resource
// was added and discovery should run now rather than at the next tick.
func (c *Cluster) DiscoveryRequests() <-chan struct{} { return c.wake }

// register records gvr as wanted and reports whether it is new.
func (c *Cluster) register(gvr schema.GroupVersionResource, kind Kind) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.dynamics[gvr]; ok {
		return false
	}
	c.dynamics[gvr] = &dynamicState{kind: kind}
	return true
}

// Discover registers every resource in wanted (as AddDynamic does, without requesting another
// discovery) and then asks the API server once which resources it serves. A resource that is
// served and has no informer gets one, started at once when the Cluster has started; one that
// stopped being served is marked absent. Both fire KindCRDs.
//
// A failure of some groups returns an *ErrGroupDiscoveryFailed after applying the healthy
// groups: a wanted resource of a failed group is neither present nor absent (it keeps its last
// answer), and Listed counts it as listed so a builder can publish without it. A failure of the
// whole call keeps the previous state and returns the error; a resource that no discovery has
// evaluated yet then counts as listed (nothing is known, nothing will be) until one succeeds.
func (c *Cluster) Discover(wanted map[schema.GroupVersionResource]Kind) error {
	for gvr, kind := range wanted {
		c.register(gvr, kind)
	}
	c.mu.RLock()
	disc := c.disc
	c.mu.RUnlock()
	_, lists, err := disc.ServerGroupsAndResources()
	var failed map[schema.GroupVersion]error
	switch {
	case err == nil:
	case discovery.IsGroupDiscoveryFailedError(err):
		var gd *discovery.ErrGroupDiscoveryFailed
		if errors.As(err, &gd) {
			failed = gd.Groups
		}
	default:
		c.mu.Lock()
		c.discoverFailed = true
		c.mu.Unlock()
		return fmt.Errorf("discovery: %w", err)
	}

	served := servedSet(lists)
	var changed []schema.GroupVersionResource
	c.mu.Lock()
	c.discoverFailed = false
	for gvr, st := range c.dynamics {
		st.evaluated = true
		_, groupFailed := failed[gvr.GroupVersion()]
		st.groupFailed = groupFailed
		if groupFailed {
			continue
		}
		if now := served[gvr]; now != st.present {
			st.present = now
			st.rev.Add(1)
			changed = append(changed, gvr)
		}
		if st.present && st.inf == nil {
			c.startInformerLocked(gvr, st)
		}
	}
	started := c.ctx
	c.mu.Unlock()
	if started != nil {
		c.dyn.Start(started.Done())
	}
	if len(changed) > 0 {
		c.notify(KindCRDs)
	}
	if len(failed) > 0 {
		return err
	}
	return nil
}

// servedSet lists the resources of a discovery answer; subresources ("pods/status") are not
// resources of their own.
func servedSet(lists []*metav1.APIResourceList) map[schema.GroupVersionResource]bool {
	out := map[schema.GroupVersionResource]bool{}
	for _, l := range lists {
		if l == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range l.APIResources {
			if strings.Contains(r.Name, "/") {
				continue
			}
			out[gv.WithResource(r.Name)] = true
		}
	}
	return out
}

// startInformerLocked creates the informer of gvr and wires its notifications; the caller holds
// c.mu and starts the factory afterwards.
func (c *Cluster) startInformerLocked(gvr schema.GroupVersionResource, st *dynamicState) {
	gi := c.dyn.ForResource(gvr)
	st.inf, st.lister = gi.Informer(), gi.Lister()
	bump := func() {
		st.rev.Add(1)
		c.notify(st.kind)
	}
	// Registering handlers on a not-yet-started informer cannot fail.
	_ = st.inf.SetWatchErrorHandlerWithContext(func(ctx context.Context, r *cache.Reflector, err error) {
		st.watchErr.Store(&err)
		cache.DefaultWatchErrorHandler(ctx, r, err)
	})
	_, _ = st.inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { bump() },
		UpdateFunc: func(_, _ any) { bump() },
		DeleteFunc: func(any) { bump() },
	})
}

// Has reports that the API server serves gvr: the last successful discovery of its group lists
// it. A resource nobody asked for is not served as far as Has knows.
func (c *Cluster) Has(gvr schema.GroupVersionResource) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.dynamics[gvr]
	return st != nil && st.present
}

// Listed reports that the first list of gvr has completed, or will never happen: discovery says
// gvr is not served, or failed for its group (or the discovery that would have evaluated gvr
// failed altogether). A builder publishes once its own resources are listed, so it never
// computes from a partial list. It is false for a resource nobody asked for, and for one that
// no discovery has evaluated yet: a resource registered after the last Discover has no informer
// until the next one, so Has and Listed are both false then.
func (c *Cluster) Listed(gvr schema.GroupVersionResource) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.dynamics[gvr]
	switch {
	case st == nil:
		return false
	case !st.evaluated:
		return c.discoverFailed
	case st.groupFailed || !st.present:
		return true
	}
	return st.inf != nil && st.inf.HasSynced()
}

// FailedGroups lists the group versions of the wanted resources whose discovery failed at the
// last attempt: the group's own failure, or, for a resource no discovery has evaluated, the
// failure of the whole call.
// The result is sorted and has no duplicates.
func (c *Cluster) FailedGroups() []schema.GroupVersion {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []schema.GroupVersion
	for gvr, st := range c.dynamics {
		if ((c.discoverFailed && !st.evaluated) || st.groupFailed) && !slices.Contains(out, gvr.GroupVersion()) {
			out = append(out, gvr.GroupVersion())
		}
	}
	slices.SortFunc(out, func(a, b schema.GroupVersion) int { return strings.Compare(a.String(), b.String()) })
	return out
}

// List returns the cached objects of a watched custom resource (all namespaces); nil while gvr is
// not served or not watched.
func (c *Cluster) List(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	c.mu.RLock()
	st := c.dynamics[gvr]
	var l cache.GenericLister
	if st != nil && st.present {
		l = st.lister
	}
	c.mu.RUnlock()
	if l == nil {
		return nil
	}
	objs, err := l.List(labels.Everything())
	if err != nil {
		return nil
	}
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok {
			out = append(out, u)
		}
	}
	return out
}

// Revision is the sum of the change counters of the watched resources in gvrs (all of them when
// none are given): it grows whenever one has an object added, changed or deleted, or appears in
// or leaves the served set, and never falls.
func (c *Cluster) Revision(gvrs ...schema.GroupVersionResource) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var sum uint64
	if len(gvrs) == 0 {
		for _, st := range c.dynamics {
			sum += st.rev.Load()
		}
		return sum
	}
	for _, gvr := range gvrs {
		if st := c.dynamics[gvr]; st != nil {
			sum += st.rev.Load()
		}
	}
	return sum
}

// WatchError is the last error the watch of gvr reported, nil when none has: what explains an
// informer that never lists (a forbidden list, say).
func (c *Cluster) WatchError(gvr schema.GroupVersionResource) error {
	c.mu.RLock()
	st := c.dynamics[gvr]
	c.mu.RUnlock()
	if st == nil {
		return nil
	}
	if p := st.watchErr.Load(); p != nil {
		return *p
	}
	return nil
}
