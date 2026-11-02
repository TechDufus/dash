// Package kube owns the Kubernetes clients and the shared informers dash builds its topics from.
//
// The typed informers (nodes, namespaces, pods, PVCs, the workload controllers and the warning
// events) are registered when the Cluster is built and gate HasSynced: the kubernetes plugin
// reports itself starting until they have synced. A custom resource is different: its CRD may
// not exist, and an informer for a resource the API server does not serve never syncs. So the
// dynamic informers are discovery-gated (discovery.go): they start when discovery says the
// resource is served, they never gate HasSynced, and Listed says when one has finished its
// first list (or will never have one).
package kube

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

// FieldManager is the field manager of every write dash makes to the cluster.
const FieldManager = "dash"

// GVRs of the custom resources dash watches.
var (
	GVRKustomizations   = schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	GVRHelmReleases     = schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}
	GVRGitRepositories  = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	GVROCIRepositories  = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}
	GVRHelmRepositories = schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}
	GVRCNPGClusters     = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	GVRCertificates     = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	GVRExternalSecrets  = schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1", Resource: "externalsecrets"}
	GVRHTTPRoutes       = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	GVRIngresses        = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
)

// Kind names what changed, so a consumer can mark the right dependents dirty.
type Kind string

const (
	KindNodes        Kind = "nodes"
	KindNamespaces   Kind = "namespaces"
	KindPods         Kind = "pods"
	KindWorkloads    Kind = "workloads" // deployments, statefulsets, daemonsets, replicasets, jobs, cronjobs
	KindPVCs         Kind = "pvcs"
	KindEvents       Kind = "events"
	KindFlux         Kind = "flux"
	KindCNPG         Kind = "cnpg"
	KindCertificates Kind = "certificates"
	KindExtSecrets   Kind = "externalsecrets"
	KindHTTPRoutes   Kind = "httproutes"
	KindIngresses    Kind = "ingresses"
	// KindCRDs is a custom resource that appeared in or vanished from the served set.
	KindCRDs Kind = "crds"
	// KindOther is a resource watched on behalf of a caller that named no kind.
	KindOther Kind = "other"
)

// defaultKinds is the kind of a GVR that Watch registers without one.
var defaultKinds = map[schema.GroupVersionResource]Kind{
	GVRKustomizations: KindFlux, GVRHelmReleases: KindFlux, GVRGitRepositories: KindFlux,
	GVROCIRepositories: KindFlux, GVRHelmRepositories: KindFlux,
	GVRCNPGClusters: KindCNPG, GVRCertificates: KindCertificates,
	GVRExternalSecrets: KindExtSecrets, GVRHTTPRoutes: KindHTTPRoutes, GVRIngresses: KindIngresses,
}

// Connection modes, as plugins.kubernetes.mode spells them.
const (
	ModeAuto       = "auto"
	ModeInCluster  = "inCluster"
	ModeKubeconfig = "kubeconfig"
)

// ErrNoCluster is what LoadConfig returns in auto mode when there is neither a kubeconfig nor an
// in-cluster environment: dash runs without a cluster.
var ErrNoCluster = errors.New("no cluster configuration")

// ErrBadContext is what LoadConfig wraps when the kubeconfig has no context of the name asked
// for (plugins.kubernetes.context).
var ErrBadContext = errors.New("the kubeconfig has no such context")

// LoadConfig returns the REST config of a mode: kubeconfig (a path, with an optional context) for
// ModeKubeconfig, the in-cluster service account for ModeInCluster, and for ModeAuto the
// kubeconfig when a path is given, else the in-cluster one, else ErrNoCluster. qps and burst are
// the client's rate limits. It reads no environment variable: the caller resolves KUBECONFIG.
func LoadConfig(mode, kubeconfig, kubeContext string, qps float32, burst int) (*rest.Config, error) {
	var rc *rest.Config
	var err error
	switch {
	case mode == ModeKubeconfig || (mode == ModeAuto && kubeconfig != ""):
		if kubeconfig == "" {
			return nil, errors.New("mode kubeconfig needs plugins.kubernetes.kubeconfig (or KUBECONFIG)")
		}
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
		if raw, lerr := rules.Load(); kubeContext != "" && lerr == nil {
			if _, ok := raw.Contexts[kubeContext]; !ok {
				return nil, fmt.Errorf("kubeconfig: %q: %w", kubeContext, ErrBadContext)
			}
		}
		rc, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubeconfig: %w", err)
		}
	case mode == ModeInCluster || mode == ModeAuto:
		rc, err = rest.InClusterConfig()
		if errors.Is(err, rest.ErrNotInCluster) {
			if mode == ModeAuto {
				return nil, ErrNoCluster
			}
			return nil, fmt.Errorf("mode inCluster outside a cluster: %w", err)
		}
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	rc.QPS, rc.Burst = qps, burst
	return rc, nil
}

// Cluster bundles the clients and the informer-backed listers.
type Cluster struct {
	Client  kubernetes.Interface
	Dynamic dynamic.Interface

	Nodes        corelisters.NodeLister
	Namespaces   corelisters.NamespaceLister
	Pods         corelisters.PodLister
	Deployments  appslisters.DeploymentLister
	StatefulSets appslisters.StatefulSetLister
	DaemonSets   appslisters.DaemonSetLister
	ReplicaSets  appslisters.ReplicaSetLister
	Jobs         batchlisters.JobLister
	CronJobs     batchlisters.CronJobLister
	PVCs         corelisters.PersistentVolumeClaimLister
	Events       corelisters.EventLister // Warning events only

	typed  informers.SharedInformerFactory
	events informers.SharedInformerFactory
	dyn    dynamicinformer.DynamicSharedInformerFactory
	disc   discovery.ServerResourcesInterface

	synced   []cache.InformerSynced // the typed informers: the readiness gate
	typedInf []cache.SharedIndexInformer
	syncErr  atomic.Pointer[error] // the last watch error of a typed informer
	wake     chan struct{}         // a request for an immediate discovery (coalesced)
	mu       sync.RWMutex          // guards what follows
	onChange []func(Kind)
	ctx      context.Context // set by Start
	dynamics map[schema.GroupVersionResource]*dynamicState
	// discoverFailed is set by a discovery that failed altogether, and cleared by one that
	// returned an answer.
	discoverFailed bool
}

// New builds clients and (unstarted) informers from a REST config.
func New(rc *rest.Config) (*Cluster, error) {
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("kubernetes client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return NewFromClients(client, dyn), nil
}

// NewFromClients builds informers over existing clients (fakes in tests). Only the typed
// informers exist yet: a custom resource is added with AddDynamic, Watch or Discover.
func NewFromClients(client kubernetes.Interface, dyn dynamic.Interface) *Cluster {
	c := &Cluster{Client: client, Dynamic: dyn, wake: make(chan struct{}, 1), dynamics: map[schema.GroupVersionResource]*dynamicState{}}
	c.disc = client.Discovery()
	c.typed = informers.NewSharedInformerFactory(client, 0)
	c.events = informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithTweakListOptions(func(o *metav1.ListOptions) {
		o.FieldSelector = "type=Warning"
	}))
	c.dyn = dynamicinformer.NewDynamicSharedInformerFactory(dyn, 0)

	core, apps, batch := c.typed.Core().V1(), c.typed.Apps().V1(), c.typed.Batch().V1()
	c.Nodes, c.Namespaces, c.Pods, c.PVCs = core.Nodes().Lister(), core.Namespaces().Lister(), core.Pods().Lister(), core.PersistentVolumeClaims().Lister()
	c.Deployments, c.StatefulSets, c.DaemonSets, c.ReplicaSets = apps.Deployments().Lister(), apps.StatefulSets().Lister(), apps.DaemonSets().Lister(), apps.ReplicaSets().Lister()
	c.Jobs, c.CronJobs = batch.Jobs().Lister(), batch.CronJobs().Lister()
	c.Events = c.events.Core().V1().Events().Lister()

	typed := []struct {
		inf  cache.SharedIndexInformer
		kind Kind
	}{
		{core.Nodes().Informer(), KindNodes},
		{core.Namespaces().Informer(), KindNamespaces},
		{core.Pods().Informer(), KindPods},
		{core.PersistentVolumeClaims().Informer(), KindPVCs},
		{apps.Deployments().Informer(), KindWorkloads},
		{apps.StatefulSets().Informer(), KindWorkloads},
		{apps.DaemonSets().Informer(), KindWorkloads},
		{apps.ReplicaSets().Informer(), KindWorkloads},
		{batch.Jobs().Informer(), KindWorkloads},
		{batch.CronJobs().Informer(), KindWorkloads},
		{c.events.Core().V1().Events().Informer(), KindEvents},
	}
	for _, t := range typed {
		c.watchTyped(t.inf, t.kind)
	}
	return c
}

// watchTyped adds a typed informer to the readiness gate and the change notifications.
func (c *Cluster) watchTyped(inf cache.SharedIndexInformer, kind Kind) {
	c.synced = append(c.synced, inf.HasSynced)
	c.typedInf = append(c.typedInf, inf)
	notify := func() { c.notify(kind) }
	// Registering a handler or an error handler on a not-yet-started informer cannot fail.
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { notify() },
		UpdateFunc: func(_, _ any) { notify() },
		DeleteFunc: func(any) { notify() },
	})
	_ = inf.SetWatchErrorHandlerWithContext(func(ctx context.Context, r *cache.Reflector, err error) {
		c.syncErr.Store(&err)
		cache.DefaultWatchErrorHandler(ctx, r, err)
	})
}

// OnChange registers fn to be called (on the informer goroutines; keep it cheap) for every
// add, update or delete of a watched resource, and with KindCRDs when the served set changes.
// Register before Start.
func (c *Cluster) OnChange(fn func(Kind)) {
	c.mu.Lock()
	c.onChange = append(c.onChange, fn)
	c.mu.Unlock()
}

func (c *Cluster) notify(kind Kind) {
	c.mu.RLock()
	fns := c.onChange
	c.mu.RUnlock()
	for _, fn := range fns {
		fn(kind)
	}
}

// Start launches every informer registered so far; it does not block. A dynamic informer added
// later starts when it is added.
func (c *Cluster) Start(ctx context.Context) {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	c.typed.Start(ctx.Done())
	c.events.Start(ctx.Done())
	c.dyn.Start(ctx.Done())
}

// Shutdown waits until every informer goroutine has ended. The context given to Start must be
// cancelled first, or it never returns. The Cluster starts no informer afterwards: a discovery
// that still runs creates its informers without starting them.
func (c *Cluster) Shutdown() {
	c.typed.Shutdown()
	c.events.Shutdown()
	c.dyn.Shutdown()
}

// Stopped reports that every informer the Cluster has has ended (after Shutdown, or when the
// context of Start is done and they have exited).
func (c *Cluster) Stopped() bool {
	for _, inf := range c.typedInf {
		if !inf.IsStopped() {
			return false
		}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, st := range c.dynamics {
		if st.inf != nil && !st.inf.IsStopped() {
			return false
		}
	}
	return true
}

// HasSynced is true once every typed informer has completed its first list. A dynamic informer
// never gates it (see Listed).
func (c *Cluster) HasSynced() bool {
	for _, s := range c.synced {
		if !s() {
			return false
		}
	}
	return true
}

// WaitForSync blocks until HasSynced or ctx ends.
func (c *Cluster) WaitForSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), c.synced...)
}

// SyncError is the last error a typed informer's watch reported, nil when none has. It is what
// explains a sync that does not complete: a forbidden list, an unreachable API server.
func (c *Cluster) SyncError() error {
	if p := c.syncErr.Load(); p != nil {
		return *p
	}
	return nil
}

// Patch sends a JSON merge patch to one object through the dynamic client, under the field
// manager "dash". namespace is empty for a cluster-scoped resource. The error is the API
// server's, unwrapped.
func (c *Cluster) Patch(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string, merge []byte) error {
	r := c.Dynamic.Resource(gvr)
	var err error
	if namespace == "" {
		_, err = r.Patch(ctx, name, types.MergePatchType, merge, metav1.PatchOptions{FieldManager: FieldManager})
	} else {
		_, err = r.Namespace(namespace).Patch(ctx, name, types.MergePatchType, merge, metav1.PatchOptions{FieldManager: FieldManager})
	}
	return err
}

// ListNodes, ListPods etc. are shorthands that hide the lister error (an informer cache read
// cannot fail with a selector of Everything).

func (c *Cluster) ListNodes() []*corev1.Node {
	l, _ := c.Nodes.List(labels.Everything())
	return l
}
func (c *Cluster) ListNamespaces() []*corev1.Namespace {
	l, _ := c.Namespaces.List(labels.Everything())
	return l
}
func (c *Cluster) ListPods() []*corev1.Pod {
	l, _ := c.Pods.List(labels.Everything())
	return l
}
func (c *Cluster) ListPVCs() []*corev1.PersistentVolumeClaim {
	l, _ := c.PVCs.List(labels.Everything())
	return l
}
func (c *Cluster) ListEvents() []*corev1.Event {
	l, _ := c.Events.List(labels.Everything())
	return l
}
func (c *Cluster) ListDeployments() []*appsv1.Deployment {
	l, _ := c.Deployments.List(labels.Everything())
	return l
}
func (c *Cluster) ListStatefulSets() []*appsv1.StatefulSet {
	l, _ := c.StatefulSets.List(labels.Everything())
	return l
}
func (c *Cluster) ListDaemonSets() []*appsv1.DaemonSet {
	l, _ := c.DaemonSets.List(labels.Everything())
	return l
}
func (c *Cluster) ListJobs() []*batchv1.Job {
	l, _ := c.Jobs.List(labels.Everything())
	return l
}
func (c *Cluster) ListCronJobs() []*batchv1.CronJob {
	l, _ := c.CronJobs.List(labels.Everything())
	return l
}

// Resolver walks pod owner chains through the informer caches.
type Resolver struct{ c *Cluster }

// Resolver returns an owner-chain resolver.
func (c *Cluster) Resolver() Resolver { return Resolver{c} }

// Owner is one element of an owner chain.
type Owner struct{ Kind, Name string }

// Chain returns the owner chain of a pod from the direct owner up: Pod -> ReplicaSet ->
// Deployment, or a direct StatefulSet, DaemonSet or Job (a Job's CronJob owner is followed).
func (r Resolver) Chain(p *corev1.Pod) []Owner {
	var chain []Owner
	kind, name := controller(p.OwnerReferences)
	for depth := 0; kind != "" && depth < 4; depth++ {
		chain = append(chain, Owner{kind, name})
		var refs []metav1.OwnerReference
		switch kind {
		case "ReplicaSet":
			if rs, err := r.c.ReplicaSets.ReplicaSets(p.Namespace).Get(name); err == nil {
				refs = rs.OwnerReferences
			}
		case "Job":
			if j, err := r.c.Jobs.Jobs(p.Namespace).Get(name); err == nil {
				refs = j.OwnerReferences
			}
		}
		kind, name = controller(refs)
	}
	return chain
}

// Top returns the last element of the chain (the pod's workload), or false for a bare pod.
func (r Resolver) Top(p *corev1.Pod) (Owner, bool) {
	chain := r.Chain(p)
	if len(chain) == 0 {
		return Owner{}, false
	}
	return chain[len(chain)-1], true
}

func controller(refs []metav1.OwnerReference) (kind, name string) {
	for _, o := range refs {
		if o.Controller != nil && *o.Controller {
			return o.Kind, o.Name
		}
	}
	return "", ""
}
