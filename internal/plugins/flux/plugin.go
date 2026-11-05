// Package flux is the Flux plugin (design F.0, P3.11): the Flux sources, Kustomizations and Helm
// releases of the cluster as the flux.objects topic, the signature checks of the sources, the
// reconcile action, and the plugin's share of the overview (attention items and KPIs).
//
// It reads the cluster through the kubernetes plugin's svc.Cluster, so it requires that plugin
// and has no connection of its own. Like every plugin it imports neither internal/config nor
// internal/api, nor another plugin.
package flux

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
)

// ID is the plugin's id and its instance name: plugins.flux, topic flux.objects.
const ID plugin.ID = "flux"

const (
	// topicObjects is the manifest name of the topic; it is published as "flux.objects".
	topicObjects = "objects"

	// sourceKubernetes names the cluster in the sources of the topic and the row; sourceFlux
	// names the Flux API groups, when the discovery of one of them fails.
	sourceKubernetes = "kubernetes"
	sourceFlux       = "flux"
)

// gvrs are the five resources the plugin reads, in the order the topic lists them: the sources
// (Git, OCI, Helm), then the Kustomizations and the Helm releases.
var gvrs = []schema.GroupVersionResource{
	kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
	kube.GVRKustomizations, kube.GVRHelmReleases,
}

// kinds are the Flux kinds of gvrs, index for index.
var kinds = []model.FluxKind{
	model.FluxKindGitRepository, model.FluxKindOCIRepository, model.FluxKindHelmRepository,
	model.FluxKindKustomization, model.FluxKindHelmRelease,
}

// gvrOf is the resource of a Flux kind.
func gvrOf(kind model.FluxKind) (schema.GroupVersionResource, bool) {
	for i, k := range kinds {
		if k == kind {
			return gvrs[i], true
		}
	}
	return schema.GroupVersionResource{}, false
}

// timing holds the interval of the plugin's round; tests shorten it.
type timing struct {
	tick time.Duration // how often the cluster's state is compared with the last round's
}

func defaultTiming() timing { return timing{tick: time.Second} }

// Plugin reads the Flux objects. The supervisor makes a fresh one for every start of the instance.
type Plugin struct {
	plugin.ObjectStore
	t  timing
	rt atomic.Pointer[runtime] // set by Start
}

// New is the plugin's factory.
func New() plugin.Plugin { return &Plugin{t: defaultTiming()} }

func (p *Plugin) timing() timing {
	if p.t == (timing{}) {
		return defaultTiming()
	}
	return p.t
}

// manifest is the plugin's declaration, shared with its demo twin.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Flux",
		Description: "Flux sources, kustomizations and Helm releases.",
		Version:     "1.0.0",
		Requires:    []plugin.ID{"kubernetes"},
		Config:      func() any { return &pluginconf.Flux{} },
		Topics: []plugin.TopicSpec{
			{Name: topicObjects, Type: model.FluxData{}, Doc: "Flux sources, kustomizations and Helm releases."},
		},
		Actions: actionSpecs(),
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/gitops", Label: "GitOps", Title: "GitOps (Flux)", Icon: "GitBranch"},
		}},
	}
}

// Manifest is the plugin's declaration.
func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.Flux, error) {
	switch c := cfg.(type) {
	case *pluginconf.Flux:
		if c != nil {
			return *c, nil
		}
	case pluginconf.Flux:
		return c, nil
	}
	return pluginconf.Flux{}, fmt.Errorf("flux: unexpected configuration type %T", cfg)
}

// Configured is always true: Flux needs no setting of its own, and a cluster without Flux makes
// the row unavailable instead of disabled.
func (p *Plugin) Configured(any) bool { return true }

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.flux.enabled" }

// Validate applies the section's own rules.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// Start finds the kubernetes service, asks it to watch the five resources and launches the round
// that publishes the topic; it does not wait for a sync. It does not fail without a service
// (kubernetes may be in error, and that is not an error of flux): the round looks for it again.
func (p *Plugin) Start(ctx context.Context, h plugin.Host, cfg any) error {
	h = p.Wrap(h)
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithCancel(ctx)
	r := &runtime{h: h, cfg: c, t: p.timing(), ctx: rctx, cancel: cancel}
	p.rt.Store(r)
	r.watch(r.lookup())
	r.spawn("publish", r.loop)
	return nil
}

// Stop ends every goroutine Start launched, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Status follows design 6.3; see runtime.status.
func (p *Plugin) Status() plugin.Status {
	r := p.rt.Load()
	if r == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return r.status()
}

// Demo implements plugin.Demo.
func (p *Plugin) Demo() plugin.Plugin { return newTwin() }

// class says what the last published payload was built from.
type class int

const (
	classNone  class = iota + 1 // the cluster serves none of the five resources: an empty payload
	classReady                  // the lists were complete: the real payload
)

// published is what this run has put in the hub: the service it read and the kind of payload.
type published struct {
	ok  bool
	svc svc.Cluster
	cls class
}

// built is the last real payload and what the rules need besides it.
type built struct {
	data model.FluxData
	// unknown are the source kinds whose list the plugin could not read (refused, or its API
	// group failed discovery): a pinned source of such a kind is not "missing", just unseen.
	unknown map[model.FluxKind]bool
}

// runtime is one run of the plugin: what Start built and the round fills. A reload makes a new
// one, so nothing here outlives its configuration.
type runtime struct {
	h      plugin.Host
	cfg    pluginconf.Flux
	t      timing
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// watched is the service the five resources were last requested from; watchMu guards it
	// (Start and the round both call watch).
	watchMu sync.Mutex
	watched svc.Cluster

	// seen is the state the last round looked at (only the round reads or writes it).
	seen     signature
	haveSeen bool

	// pubMu serialises build and publish, so a slow build cannot send an older snapshot after a
	// newer one.
	pubMu sync.Mutex

	mu  sync.RWMutex
	pub published
	// last is the last real payload, nil while there is none for the service the plugin reads.
	last *built
}

// spawn runs fn on Host.Go and counts it, so Stop can wait for it.
func (r *runtime) spawn(name string, fn func()) {
	r.wg.Add(1)
	r.h.Go(name, func() {
		defer r.wg.Done()
		fn()
	})
}

// lookup is the kubernetes plugin's svc.Cluster while the plugin runs and exports one, else nil.
// It is looked up per call: kubernetes restarts on a reload and its service changes with it, and
// it exports none while its own row is unavailable or in error.
func (r *runtime) lookup() svc.Cluster {
	v, ok := r.h.Service("kubernetes")
	if !ok {
		return nil
	}
	c, _ := v.(svc.Cluster)
	return c
}

// watch asks c to watch the five resources, once per service: a new kube.Cluster has no
// registrations, and Watch is idempotent for one that has them.
func (r *runtime) watch(c svc.Cluster) {
	if c == nil {
		return
	}
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if c == r.watched {
		return
	}
	r.watched = c
	c.Watch(gvrs...)
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.ConfigHinter    = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.Actor           = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.KPISource       = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
