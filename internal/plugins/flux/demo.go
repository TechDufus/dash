package flux

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// demoEvery is how often the twin republishes, so the links of a provider that started after it
// appear and the ages move on, like the rest of demo mode.
const demoEvery = 5 * time.Second

// twin is the plugin's demo twin (design 1.9): the same manifest, the Flux objects of the fake
// world and no cluster. The payload's decoration, the attention rules and the KPIs are the real
// plugin's, run over the world's objects with the settings the fake lab would have (its
// GitOps repository pinned), so the overview and the GitOps page agree with what a real Flux
// shows. Reconcile is faked: it checks the object exists and does nothing.
type twin struct {
	mu   sync.Mutex
	h    plugin.Host
	cfg  pluginconf.Flux
	data *model.FluxData
	// reconciled counts the reconcile requests per object ref, so the twin's behaviour is visible.
	reconciled map[string]int

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newTwin() *twin { return &twin{reconciled: map[string]int{}} }

// twinLinks is the world's link source: whatever the running link providers offer.
type twinLinks struct{ h plugin.Host }

func (l twinLinks) Headlamp(kind, namespace, name string) []model.Link {
	return l.h.Links().Resolve(plugin.LinkRequest{Kind: kind, Namespace: namespace, Name: name})
}

func (t *twin) Manifest() plugin.Manifest { return manifest() }

// Start publishes the world's objects now and then every demoEvery.
func (t *twin) Start(ctx context.Context, h plugin.Host, _ any) error {
	cfg := pluginconf.DefaultFlux()
	cfg.Verify.Sources = []pluginconf.VerifySource{{
		Kind: string(world.FluxPinned.Kind), Namespace: world.FluxPinned.Namespace, Name: world.FluxPinned.Name,
	}}
	w := world.New(h.Now, h.Now(), twinLinks{h})
	rctx, cancel := context.WithCancel(ctx)
	t.mu.Lock()
	t.h, t.cfg, t.cancel = h, cfg, cancel
	t.mu.Unlock()
	refresh := func() {
		d := decorate(w.Flux(), cfg)
		t.mu.Lock()
		t.data = &d
		t.mu.Unlock()
		h.Publish(topicObjects, d, []model.SourceStatus{{Name: sourceKubernetes, OK: true, CheckedAt: h.Now().UTC()}})
	}
	refresh()
	t.wg.Add(1)
	h.Go("demo", func() {
		defer t.wg.Done()
		tick := time.NewTicker(demoEvery)
		defer tick.Stop()
		for {
			select {
			case <-rctx.Done():
				return
			case <-tick.C:
				refresh()
			}
		}
	})
	return nil
}

func (t *twin) Stop(ctx context.Context) error {
	t.mu.Lock()
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { t.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *twin) Status() plugin.Status {
	return plugin.Status{State: plugin.StateHealthy, Sources: []model.SourceStatus{{Name: sourceKubernetes, OK: true}}}
}

// decorate gives the world's objects what the plugin adds to the objects it reads: the
// reference, links that are never nil, and the list of the sources whose signature is reported.
func decorate(d model.FluxData, cfg pluginconf.Flux) model.FluxData {
	each := func(l []model.FluxObj) []model.FluxObj {
		out := make([]model.FluxObj, len(l))
		for i, o := range l {
			o.Ref = plugin.Ref(ID, string(o.Kind), o.Namespace, o.Name)
			if o.Links == nil {
				o.Links = []model.Link{}
			}
			out[i] = o
		}
		return out
	}
	d.Sources, d.Kustomizations, d.HelmReleases = each(d.Sources), each(d.Kustomizations), each(d.HelmReleases)
	d.VerifiedSources = verifiedSources(d, cfg)
	return d
}

// Act fakes the reconcile of an object of the world: nothing leaves the process.
func (t *twin) Act(_ context.Context, action string, _ auth.Principal, input any) (any, error) {
	in, ok := input.(*reconcileInput)
	if action != actionReconcile || !ok {
		return nil, refusal(http.StatusNotFound, model.ErrCodeNotFound, "no such action: "+action)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.data == nil {
		return nil, errNoCluster
	}
	for _, l := range [][]model.FluxObj{t.data.Sources, t.data.Kustomizations, t.data.HelmReleases} {
		for _, o := range l {
			if o.Kind == in.Kind && o.Namespace == in.Namespace && o.Name == in.Name {
				t.reconciled[o.Ref]++
				return nil, nil
			}
		}
	}
	return nil, refusal(http.StatusNotFound, model.ErrCodeNotFound, "no such object")
}

// Attention and KPIs of the twin, over its payload, so the overview agrees with the page.
func (t *twin) Attention(time.Time) []model.AttentionItem {
	t.mu.Lock()
	d, cfg := t.data, t.cfg
	t.mu.Unlock()
	if d == nil {
		return nil
	}
	return attentionOf(built{data: *d}, cfg)
}

func (t *twin) KPIs() []model.KPI {
	t.mu.Lock()
	d, cfg := t.data, t.cfg
	t.mu.Unlock()
	if d == nil {
		return nil
	}
	return kpisOf(built{data: *d}, cfg)
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.Actor           = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
	_ plugin.KPISource       = (*twin)(nil)
)
