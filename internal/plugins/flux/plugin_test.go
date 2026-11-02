package flux

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The shim's manifest facts survive the move: nav entry, version, description. A lost nav entry is
// not caught by the SPA module registry test.
func TestFluxManifestKeepsTheShimsNav(t *testing.T) {
	m := New().Manifest()
	if m.ID != "flux" || m.Name != "Flux" || m.Version != "1.0.0" || m.Description != "Flux sources, kustomizations and Helm releases." {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != (model.NavItem{Path: "/gitops", Label: "GitOps", Title: "GitOps (Flux)", Icon: "GitBranch"}) {
		t.Errorf("nav = %+v", m.UI.Nav)
	}
	if len(m.UI.Generic) != 0 {
		t.Errorf("generic views = %+v: the plugin has a module of its own", m.UI.Generic)
	}
}

func TestFluxManifestDeclaresItsTopicActionAndDependency(t *testing.T) {
	m := New().Manifest()
	if !reflect.DeepEqual(m.Requires, []plugin.ID{"kubernetes"}) || len(m.Uses) != 0 {
		t.Errorf("requires = %v, uses = %v: flux requires kubernetes and uses nothing", m.Requires, m.Uses)
	}
	if len(m.Topics) != 1 || m.Topics[0].Name != "objects" || reflect.TypeOf(m.Topics[0].Type) != reflect.TypeFor[model.FluxData]() {
		t.Fatalf("topics = %+v", m.Topics)
	}
	if got := plugin.TopicName(m.ID, m.Topics[0]); got != model.Topic("flux.objects") {
		t.Errorf("topic name = %s", got)
	}
	if len(m.Actions) != 1 || m.Actions[0].Name != "reconcile" {
		t.Errorf("actions = %+v", m.Actions)
	}
	if len(m.Series) != 0 {
		t.Errorf("series = %+v", m.Series)
	}
	if _, ok := m.Config().(*pluginconf.Flux); !ok {
		t.Errorf("config = %T", m.Config())
	}
}

// With no setting at all the plugin is configured (it needs none); the hint starts with the
// section's name, which is what the "not configured" row prints.
func TestEveryRealPluginSaysWhatToSet(t *testing.T) {
	p := New().(*Plugin)
	if !p.Configured(nil) || !p.Configured(&pluginconf.Flux{}) {
		t.Error("flux needs no setting: it is always configured")
	}
	if h := p.ConfigHint(); !strings.HasPrefix(h, "plugins.flux.") {
		t.Errorf("hint = %q, want it to start with plugins.flux.", h)
	}
}

func TestFluxValidateIsTheSectionsValidate(t *testing.T) {
	p := New().(*Plugin)
	if err := p.Validate(&pluginconf.Flux{}); err != nil {
		t.Errorf("an empty section = %v", err)
	}
	if err := p.Validate(pluginconf.DefaultFlux()); err != nil {
		t.Errorf("the default section by value = %v", err)
	}
	bad := pluginconf.DefaultFlux()
	bad.Verify.Sources = []pluginconf.VerifySource{{Kind: "Kustomization", Namespace: "a", Name: "b"}}
	if err := p.Validate(&bad); err == nil {
		t.Error("a pin of a kind that cannot be verified was accepted")
	}
	if err := p.Validate("not a section"); err == nil {
		t.Error("a configuration of the wrong type was accepted")
	}
	var nilCfg *pluginconf.Flux
	if err := p.Validate(nilCfg); err == nil {
		t.Error("a nil section was accepted")
	}
}

func TestStartRefusesAConfigurationOfAnotherType(t *testing.T) {
	p := &Plugin{}
	if err := p.Start(context.Background(), &stubHost{}, 42); err == nil {
		t.Error("Start accepted an int")
	}
	if st := p.Status(); st.State != plugin.StateStarting {
		t.Errorf("status before a successful Start = %+v", st)
	}
}

// The five resources are the catalog's, in the order the page lists them, and each kind has its
// own.
func TestKindsAndResourcesAgree(t *testing.T) {
	if len(kinds) != 5 || len(gvrs) != 5 {
		t.Fatalf("%d kinds, %d resources", len(kinds), len(gvrs))
	}
	seen := map[string]bool{}
	for i, k := range kinds {
		g, ok := gvrOf(k)
		if !ok || g != gvrs[i] {
			t.Errorf("gvrOf(%s) = %v, %v", k, g, ok)
		}
		if seen[g.Resource] {
			t.Errorf("resource %s twice", g.Resource)
		}
		seen[g.Resource] = true
	}
	if _, ok := gvrOf("Pod"); ok {
		t.Error("gvrOf(Pod) is ok")
	}
}

// Status and the rounds are safe to call concurrently with Stop and each other: run under -race.
func TestStatusAndContributionsAreSafeConcurrently(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitState(model.PluginHealthy)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			r.setKind(hr, obj(hr, "media", "app", ready("ok")))
			if i%2 == 0 {
				r.setKind(hr, obj(hr, "media", "app", notReady("x")))
			}
		}
	}()
	for range 200 {
		_ = r.row()
		_ = r.attention()
		_ = r.kpis()
	}
	<-done
}
