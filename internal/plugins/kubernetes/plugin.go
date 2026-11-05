// Package kubernetes is the Kubernetes plugin (design F.0, P3.8): it owns the connection to the
// cluster and the informers, builds the kubernetes.cluster, kubernetes.storage and
// kubernetes.apps topics, serves the pod detail and the node and pod charts, runs the restart
// action, exports the node index and the cluster caches to the plugins that use them, and adds
// the cluster's share to the overview (attention items, KPIs, topology).
//
// It reads the settings through internal/pluginconf; like every plugin it imports neither
// internal/config nor internal/api, nor another plugin.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// ID is the plugin's id and its instance name: plugins.kubernetes, topics kubernetes.cluster,
// kubernetes.storage and kubernetes.apps.
const ID plugin.ID = "kubernetes"

const (
	// The manifest names of the topics; they are published as "kubernetes.<name>".
	topicCluster = "cluster"
	topicStorage = "storage"
	topicApps    = "apps"

	// The sources of the topics and the row.
	sourceKubernetes = "kubernetes"
	sourcePrometheus = "prometheus"

	// pollTimeout caps one run of a poller.
	pollTimeout = 25 * time.Second
)

// timing holds the intervals of the plugin; tests shorten them.
type timing struct {
	probe     time.Duration // how often the API server is probed
	usage     time.Duration // the node and pod usage poller
	storage   time.Duration // the storage poller
	discovery time.Duration // how often the served API resources are rediscovered
	tick      time.Duration // the publisher's round
	// unavailableAfter is how long the probe must have failed, without a success between, for
	// the row to turn unavailable. Recovery has no debounce.
	unavailableAfter time.Duration
	// typedSyncTimeout is how long, in API-server up-time (the clock pauses while the probe
	// fails), the typed informers may take to sync before the row is an error and the service is
	// withdrawn, and a list that never completes is published without.
	typedSyncTimeout time.Duration
}

func defaultTiming() timing {
	return timing{
		probe: 5 * time.Second, usage: 30 * time.Second, storage: 60 * time.Second,
		discovery: 5 * time.Minute, tick: time.Second,
		unavailableAfter: 10 * time.Second, typedSyncTimeout: 60 * time.Second,
	}
}

// Plugin reads the cluster. The supervisor makes a fresh one for every start of the instance.
type Plugin struct {
	plugin.ObjectStore
	t       timing
	connect Connect
	refs    *secret.K8sRefs         // the k8s: references the secret watchers read; nil reads none
	rt      atomic.Pointer[runtime] // set by Start when there is a cluster

	secrets *secretWatchers // set by Start when there is a cluster and refs

	mu     sync.Mutex
	noConn *connStatus // set by Start when there is no cluster to run on
}

// connStatus is the row of an instance that could not connect.
type connStatus struct {
	state plugin.State
	msg   string
}

// New is the plugin without secret references: its manifest, routes and settings, for the
// tools and tests that never start it.
func New() plugin.Plugin { return Factory(nil)() }

// Factory is the plugin's factory. refs holds the k8s: references of the configuration: the
// plugin reads the Secrets they name once it is connected (nil reads none).
func Factory(refs *secret.K8sRefs) plugin.Factory { return FactoryWithConnect(refs, connectCluster) }

// FactoryWithConnect is Factory with the way of reaching the cluster given, so a test can hand
// over fake clients.
func FactoryWithConnect(refs *secret.K8sRefs, connect Connect) plugin.Factory {
	return func() plugin.Plugin { return &Plugin{t: defaultTiming(), connect: connect, refs: refs} }
}

func (p *Plugin) timing() timing {
	if p.t == (timing{}) {
		return defaultTiming()
	}
	return p.t
}

// Manifest is the plugin's declaration. Its series close over p, so the series the API answers
// are those of the instance that runs.
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Kubernetes",
		Description: "How dash reaches and reads the cluster.",
		Version:     "1.0.0",
		Uses:        []plugin.ID{"prometheus", "gatus"},
		Config:      func() any { return &pluginconf.Kubernetes{} },
		Topics: []plugin.TopicSpec{
			{Name: topicCluster, Type: model.ClusterData{}, Doc: "Nodes, namespaces, pods, workloads and certificates."},
			{Name: topicStorage, Type: model.StorageData{}, Doc: "Volumes and backups."},
			{Name: topicApps, Type: model.AppsData{}, Doc: "The apps behind HTTP routes."},
		},
		Actions: actionSpecs(),
		Series:  p.seriesSpecs(),
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/cluster", Label: "Cluster", Title: "Kubernetes cluster", Icon: "Boxes"},
		}},
	}
}

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.Kubernetes, error) {
	switch c := cfg.(type) {
	case *pluginconf.Kubernetes:
		if c != nil {
			return *c, nil
		}
	case pluginconf.Kubernetes:
		return c, nil
	}
	return pluginconf.Kubernetes{}, fmt.Errorf("kubernetes: unexpected configuration type %T", cfg)
}

// Configured is always true: without a cluster the plugin reports itself unavailable instead of
// disabled, so the pages that need it say so.
func (p *Plugin) Configured(any) bool { return true }

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.kubernetes.kubeconfig or run in a cluster" }

// Validate applies the section's own rules.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// noClusterMsg is the row of an instance that has no cluster configuration.
const noClusterMsg = "no cluster configuration: set plugins.kubernetes.kubeconfig or run in a cluster"

// Start connects, builds the informers and launches the pollers; it does not wait for a sync.
// A cluster that cannot be reached is not an error of Start: the row says why (no configuration:
// unavailable; an explicit mode that cannot run: error), and dash serves everything else.
func (p *Plugin) Start(ctx context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.noConn = nil
	p.mu.Unlock()
	conn, err := p.connectFn()(c)
	switch {
	case errors.Is(err, kube.ErrNoCluster):
		p.setNoConn(plugin.StateUnavailable, noClusterMsg)
		return nil
	case err != nil:
		mode := c.ConnectionMode()
		h.Logger().Error("cannot connect to the cluster", "mode", mode, "error", h.ScrubLog(err.Error()))
		setting := "plugins.kubernetes.mode " + mode
		if errors.Is(err, kube.ErrBadContext) {
			setting = "plugins.kubernetes.context"
		}
		p.setNoConn(plugin.StateError, fmt.Sprintf("cannot connect to the cluster (%s): see the log", setting))
		return nil
	}
	p.secrets = bindSecrets(ctx, conn.Cluster.Client, p.refs, h.Logger())
	rctx, cancel := context.WithCancel(ctx)
	r := newRuntime(rctx, cancel, h, c, p.timing(), conn)
	p.rt.Store(r)
	r.run()
	return nil
}

func (p *Plugin) connectFn() Connect {
	if p.connect == nil {
		return connectCluster
	}
	return p.connect
}

func (p *Plugin) setNoConn(state plugin.State, msg string) {
	p.mu.Lock()
	p.noConn = &connStatus{state, msg}
	p.mu.Unlock()
}

// Stop ends every goroutine Start launched, as long as ctx allows: the Secret watchers, the
// loops, and then the informers (the factories are shut down once the loops, which may start
// informers, have ended, so nothing of this run outlives it).
func (p *Plugin) Stop(ctx context.Context) error {
	p.secrets.stop()
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		if r.cl != nil { // the demo twin has no cluster
			r.cl.Shutdown()
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status follows design 6.3: starting until the typed informers have synced and the first
// discovery and polls have ended, then healthy or degraded; unavailable while the API server
// does not answer (after the debounce) or there is no cluster; error when the informers do not
// sync within the timeout or an explicit mode cannot run.
func (p *Plugin) Status() plugin.Status {
	if r := p.rt.Load(); r != nil {
		return r.status()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.noConn != nil {
		return plugin.Status{State: p.noConn.state, Message: p.noConn.msg}
	}
	return plugin.Status{State: plugin.StateStarting}
}

// Service exports svc.NodeIndex and svc.Cluster in one object. It is nil (no service) while the
// row is unavailable or an error, and before Start: a consumer that waits for an unsynced index
// would otherwise wait for ever (Proxmox publishes its VMs without the Kubernetes flags
// instead).
func (p *Plugin) Service() any {
	r := p.rt.Load()
	if r == nil || r.svc == nil {
		return nil
	}
	switch r.status().State {
	case plugin.StateUnavailable, plugin.StateError:
		return nil
	}
	return r.svc
}

// Demo implements plugin.Demo.
func (p *Plugin) Demo() plugin.Plugin { return newTwin() }

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.Provider        = (*Plugin)(nil)
	_ plugin.Actor           = (*Plugin)(nil)
	_ plugin.Router          = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.KPISource       = (*Plugin)(nil)
	_ plugin.TopologySource  = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
	_ svc.NodeIndex          = (*service)(nil)
	_ svc.Cluster            = (*service)(nil)
)

// outcome is how the last poll of one source ended.
type outcome struct {
	polled bool // a poll has ended, whether or not it succeeded
	err    error
	at     time.Time
}

// runtime is one run of the plugin: what Start built and the loops fill. A reload makes a new
// one, so nothing here outlives its configuration.
type runtime struct {
	h      plugin.Host
	cfg    pluginconf.Kubernetes
	t      timing
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// live counts the goroutines spawn has started and not yet seen end: what Stop waits for.
	live atomic.Int32
	// rediscover asks the discovery loop to run now: the probe saw the API server answer again
	// after a discovery that left a wanted resource unknown.
	rediscover chan struct{}

	conn    *Connection
	cl      *kube.Cluster
	svc     *service
	started time.Time

	// dirty is the set of topics to rebuild (dirtyCluster|dirtyStorage|dirtyApps).
	dirty atomic.Uint32
	// nodeRev is svc.NodeIndex.Revision: bumped (from a process-wide counter, so a restarted
	// plugin never repeats a value) when a node is added, removed or changes.
	nodeRev atomic.Uint64

	// pubMu serialises the build and the publish of each topic, so a slow build cannot send an
	// older snapshot after a newer one.
	pubMu sync.Mutex

	mu         sync.RWMutex
	usage      usageSnap
	usageOut   outcome
	storage    storageSnap
	storageOut outcome
	probe      probeState
	disc       discState
	cluster    *model.ClusterData // the last published payloads: the overview derives from them
	storageD   *model.StorageData
	logged     map[string]string // key -> the value last logged for it (once per change)
	// had and appeared are, per wanted resource, whether the last discovery found it served
	// and when one after the first discovery found it first: a resource that appears later gets
	// its whole sync timeout from then, counted in API-server up-time (see clockMark).
	had      map[schema.GroupVersionResource]bool
	appeared map[schema.GroupVersionResource]clockMark

	// seenEndpoints and seenRevision are the gatus service and its Revision as the last round saw
	// them, seenStuck the resources that were stuck (only the publisher round reads or writes
	// them).
	seenEndpoints any
	seenRevision  uint64
	seenStuck     string

	wrap func(http.RoundTripper) http.RoundTripper // upstream.DevWrap of the REST config, built on first use
	// twin, when set, replaces the live builders (the demo twin).
	twin *twinSource
}

const (
	dirtyCluster uint32 = 1 << iota
	dirtyStorage
	dirtyApps
	dirtyAll = dirtyCluster | dirtyStorage | dirtyApps
)

// nodeSeq is the process-wide source of node revisions.
var nodeSeq atomic.Uint64

func newRuntime(ctx context.Context, cancel context.CancelFunc, h plugin.Host, cfg pluginconf.Kubernetes, t timing, conn *Connection) *runtime {
	r := &runtime{
		h: h, cfg: cfg, t: t, ctx: ctx, cancel: cancel, conn: conn, cl: conn.Cluster,
		started: h.Now(), logged: map[string]string{}, rediscover: make(chan struct{}, 1),
		had: map[schema.GroupVersionResource]bool{}, appeared: map[schema.GroupVersionResource]clockMark{},
	}
	r.svc = &service{r: r}
	r.nodeRev.Store(nodeSeq.Add(1))
	r.dirty.Store(dirtyAll)
	return r
}

// spawn runs fn on Host.Go and counts it, so Stop can wait for it.
func (r *runtime) spawn(name string, fn func()) {
	r.wg.Add(1)
	r.live.Add(1)
	r.h.Go(name, func() {
		defer r.wg.Done()
		defer r.live.Add(-1)
		fn()
	})
}

// every runs fn now and then every d until the plugin stops.
func (r *runtime) every(d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		fn()
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pollEvery is every for a poll that needs the Prometheus plugin: it also runs fn at once when
// the querier appears after a poll that found none (the plugin starts later, or its credential
// is filled late), looked for every t.tick, so the rows do not read "prometheus not_configured"
// until the next interval.
func (r *runtime) pollEvery(d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	watch := time.NewTicker(r.t.tick)
	defer watch.Stop()
	for {
		had := r.querier() != nil
		fn()
		for again := false; !again; {
			select {
			case <-r.ctx.Done():
				return
			case <-t.C:
				again = true
			case <-watch.C:
				again = !had && r.querier() != nil
			}
		}
	}
}

// run starts the informers and the loops.
func (r *runtime) run() {
	r.cl.OnChange(r.onChange)
	r.cl.Start(r.ctx)
	r.spawn("probe", r.probeLoop)
	r.spawn("discovery", r.discoveryLoop)
	r.spawn("poll usage", func() { r.pollEvery(r.t.usage, r.pollUsage) })
	r.spawn("poll storage", func() { r.pollEvery(r.t.storage, r.pollStorage) })
	r.spawn("publish", r.publishLoop)
}

// onChange marks the topics a change of kind touches.
func (r *runtime) onChange(k kube.Kind) {
	switch k {
	case kube.KindNodes:
		r.nodeRev.Store(nodeSeq.Add(1))
		r.mark(dirtyCluster)
	case kube.KindNamespaces:
		r.mark(dirtyCluster | dirtyApps)
	case kube.KindPods, kube.KindWorkloads, kube.KindEvents, kube.KindCertificates, kube.KindExtSecrets:
		r.mark(dirtyCluster)
	case kube.KindPVCs:
		r.mark(dirtyStorage)
	case kube.KindHTTPRoutes, kube.KindIngresses:
		r.mark(dirtyApps)
	case kube.KindCRDs:
		r.mark(dirtyAll)
	}
}

func (r *runtime) mark(d uint32) {
	for {
		old := r.dirty.Load()
		if r.dirty.CompareAndSwap(old, old|d) {
			return
		}
	}
}

// querier is the prometheus plugin's service, or nil while it does not run.
func (r *runtime) querier() svc.Querier {
	if v, ok := r.h.Service("prometheus"); ok {
		if q, ok := v.(svc.Querier); ok {
			return q
		}
	}
	return nil
}

// changed records value under key and reports whether it differs from the one recorded before:
// a failure is logged once per change, not once per poll.
func (r *runtime) changed(key, value string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	prev := r.logged[key]
	r.logged[key] = value
	return prev != value
}

// errNotRunning answers a route that is called while the plugin has no cluster.
var errNotRunning = &plugin.ActionError{Status: http.StatusServiceUnavailable, Code: model.ErrCodeUpstreamUnavailable, Message: "kubernetes is not running"}
