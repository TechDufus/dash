// Package kube owns the Kubernetes clients and the shared informers dash builds its topics from.
package kube

import (
	"context"
	"fmt"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

	"github.com/TechDufus/dash/internal/config"
)

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
)

// Kind names what changed, so topics can mark the right dependents dirty.
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
)

// LoadConfig returns the in-cluster config, or the kubeconfig at cfg.Kubeconfig in dev.
func LoadConfig(cfg config.Config) (*rest.Config, error) {
	var rc *rest.Config
	var err error
	if cfg.Kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kubernetes config: %w", err)
	}
	rc.QPS, rc.Burst = 20, 40
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

	typed   informers.SharedInformerFactory
	events  informers.SharedInformerFactory
	dyn     dynamicinformer.DynamicSharedInformerFactory
	generic map[schema.GroupVersionResource]cache.GenericLister
	kinds   map[schema.GroupVersionResource]Kind

	synced []cache.InformerSynced

	mu       sync.RWMutex
	onChange []func(Kind)
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

// NewFromClients builds informers over existing clients (fakes in tests).
func NewFromClients(client kubernetes.Interface, dyn dynamic.Interface) *Cluster {
	c := &Cluster{Client: client, Dynamic: dyn}
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
		c.watch(t.inf, t.kind)
	}

	c.generic = map[schema.GroupVersionResource]cache.GenericLister{}
	c.kinds = map[schema.GroupVersionResource]Kind{
		GVRKustomizations: KindFlux, GVRHelmReleases: KindFlux, GVRGitRepositories: KindFlux,
		GVROCIRepositories: KindFlux, GVRHelmRepositories: KindFlux,
		GVRCNPGClusters: KindCNPG, GVRCertificates: KindCertificates,
		GVRExternalSecrets: KindExtSecrets, GVRHTTPRoutes: KindHTTPRoutes,
	}
	for gvr, kind := range c.kinds {
		inf := c.dyn.ForResource(gvr)
		c.generic[gvr] = inf.Lister()
		c.watch(inf.Informer(), kind)
	}
	return c
}

func (c *Cluster) watch(inf cache.SharedIndexInformer, kind Kind) {
	c.synced = append(c.synced, inf.HasSynced)
	notify := func() { c.notify(kind) }
	// The handler registration itself cannot fail for a not-yet-started informer.
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { notify() },
		UpdateFunc: func(_, _ any) { notify() },
		DeleteFunc: func(any) { notify() },
	})
}

// OnChange registers fn to be called (on the informer goroutines; keep it cheap) for every
// add, update or delete. Register before Start.
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

// Start launches every informer; it does not block.
func (c *Cluster) Start(ctx context.Context) {
	c.typed.Start(ctx.Done())
	c.events.Start(ctx.Done())
	c.dyn.Start(ctx.Done())
}

// HasSynced is true once every informer has completed its first list.
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

// List returns the cached objects of a custom resource (all namespaces).
func (c *Cluster) List(gvr schema.GroupVersionResource) []*unstructured.Unstructured {
	l, ok := c.generic[gvr]
	if !ok {
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
