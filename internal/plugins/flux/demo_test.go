package flux

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// In demo mode the twin runs, against no cluster: the objects of the fake world, the real plugin's
// decoration, rules and KPIs, and a reconcile that does nothing.
func TestDemoRunsTheTwinAndContactsNoCluster(t *testing.T) {
	r := newRig(t, rigOpts{demo: true})
	r.waitState(model.PluginHealthy)
	d := r.waitPublished("the world's objects", func(d model.FluxData) bool { return len(d.HelmReleases) > 0 })
	if len(d.Sources) == 0 || len(d.Kustomizations) == 0 {
		t.Fatalf("payload = %+v", d)
	}
	plugintest.AssertRefs(t, r.sup, ID, d, model.FluxObj{})

	// Nothing neutral to name: no lab, and the demo's one GitOps namespace.
	ev, _ := r.hub.Latest("", topic)
	if strings.Contains(string(ev.Data), "flux-system") {
		t.Errorf("the demo payload names flux-system: %s", ev.Data)
	}
	if d.Sources[0].Namespace != world.FluxNamespace {
		t.Errorf("first source = %+v", d.Sources[0])
	}

	// The pinned GitRepository is the one source with spec.verify; the page iterates it.
	if len(d.VerifiedSources) != 1 || d.VerifiedSources[0] != world.FluxPinned {
		t.Errorf("verifiedSources = %+v, want %+v", d.VerifiedSources, world.FluxPinned)
	}

	// One suspended release warns, with its reason; nothing is critical.
	items := r.attention()
	if len(items) != 1 || items[0].ID != "flux:suspended/HelmRelease/acme/acme-preview" || items[0].Severity != model.SeverityWarn ||
		items[0].Detail != "paused while the preview env is rebuilt" {
		t.Errorf("attention = %+v", items)
	}
	c, ok := r.contributor()
	if !ok {
		t.Fatal("the twin does not contribute")
	}
	plugintest.AssertAttentionNonNil(t, c)
	ks := kpiMap(t, r.kpis())
	total := float64(len(d.Kustomizations) + len(d.HelmReleases))
	if ks["total"].Value != total || ks["ready"].Value != total || ks["suspended"].Value != 1 ||
		ks["verified"].Value != 1 || ks["verified"].Health != model.HealthOK {
		t.Errorf("KPIs = %+v", ks)
	}

	// The twin has no cluster to touch: the fake's was never asked anything.
	if len(r.kc.Watched()) != 0 || len(r.kc.Patches()) != 0 {
		t.Errorf("the twin used the cluster: watched %v, patched %v", r.kc.Watched(), r.kc.Patches())
	}
}

// The twin mounts the real action (demo forces actions on) and fakes it: an object of the world
// is accepted and nothing is patched, one that is not is a 404.
func TestDemoReconcileIsFaked(t *testing.T) {
	r := newRig(t, rigOpts{demo: true})
	r.waitState(model.PluginHealthy)
	r.waitPublished("the objects", func(d model.FluxData) bool { return len(d.Kustomizations) > 0 })
	if err := r.reconcile(reconcileOf(model.FluxKindGitRepository, world.FluxNamespace, world.FluxNamespace)); err != nil {
		t.Fatal(err)
	}
	if err := r.reconcile(reconcileOf(model.FluxKindKustomization, world.FluxNamespace, "platform")); err != nil {
		t.Fatal(err)
	}
	wantActionError(t, r.reconcile(reconcileOf(model.FluxKindKustomization, "nowhere", "nothing")), http.StatusNotFound, model.ErrCodeNotFound)
	if n := len(r.kc.Patches()); n != 0 {
		t.Errorf("%d patches from the twin", n)
	}
}

func TestDemoTwinStopsWithItsContext(t *testing.T) {
	tw := newTwin()
	h := &stubHost{}
	if err := tw.Start(context.Background(), h, nil); err != nil {
		t.Fatal(err)
	}
	if h.published.Load() != 1 {
		t.Errorf("published %d times at Start, want 1", h.published.Load())
	}
	if err := tw.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.wantIdle(t, "after Stop")
	// A twin that never started contributes nothing and stops.
	idle := newTwin()
	if idle.Attention(t0) != nil || idle.KPIs() != nil {
		t.Error("an unstarted twin has an opinion")
	}
	if err := idle.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start = %v", err)
	}
	wantActionError(t, errOf(idle.Act(context.Background(), "reconcile", admin, reconcileOf(kst, "a", "b"))),
		http.StatusServiceUnavailable, model.ErrCodeUpstreamUnavailable)
	wantActionError(t, errOf(idle.Act(context.Background(), "restart", admin, reconcileOf(kst, "a", "b"))),
		http.StatusNotFound, model.ErrCodeNotFound)
}

func errOf(_ any, err error) error { return err }

// decorate adds what the plugin adds to the world's objects and nothing else; running it twice
// changes nothing.
func TestDecorateGivesTheWorldItsRefsAndVerifiedSources(t *testing.T) {
	cfg := pluginconf.DefaultFlux()
	cfg.Verify.Sources = []pluginconf.VerifySource{{Kind: "GitRepository", Namespace: world.FluxNamespace, Name: world.FluxNamespace}}
	raw := model.FluxData{
		Sources:        []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "a", Name: "b", SpecVerify: true}},
		Kustomizations: []model.FluxObj{{Kind: model.FluxKindKustomization, Namespace: "a", Name: "b"}},
	}
	d := decorate(raw, cfg)
	if d.Sources[0].Ref != "flux:GitRepository/a/b" || d.Kustomizations[0].Ref != "flux:Kustomization/a/b" {
		t.Errorf("refs = %q, %q", d.Sources[0].Ref, d.Kustomizations[0].Ref)
	}
	if d.Sources[0].Links == nil || d.Kustomizations[0].Links == nil {
		t.Errorf("links = %+v", d)
	}
	if raw.Sources[0].Ref != "" {
		t.Error("decorate changed its input")
	}
	if len(d.VerifiedSources) != 2 {
		t.Errorf("verifiedSources = %+v, want the pin and the signed source", d.VerifiedSources)
	}
	if again := decorate(d, cfg); !reflect.DeepEqual(again, d) {
		t.Errorf("decorate is not idempotent:\n%+v\n%+v", again, d)
	}
}

// The twin is registered under the manifest of the real plugin, so the SPA cannot tell them apart.
func TestTwinHasTheManifestOfThePlugin(t *testing.T) {
	var tw plugin.Plugin = newTwin()
	if got, want := tw.Manifest().ID, New().Manifest().ID; got != want {
		t.Errorf("twin id = %s, want %s", got, want)
	}
	if !reflect.DeepEqual(tw.Manifest().Requires, New().Manifest().Requires) || len(tw.Manifest().Actions) != 1 {
		t.Errorf("twin manifest = %+v", tw.Manifest())
	}
}
