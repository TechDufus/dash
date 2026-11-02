package world

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// The fake lab's GitOps objects name no real lab: the namespace is the neutral one the rest of
// the fixtures share, and the objects' own names carry no lab marker.
func TestFluxFixturesUseTheNeutralNamespace(t *testing.T) {
	d := testWorld().Flux()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(raw); strings.Contains(s, "flux-system") {
		t.Errorf("the flux fixtures name flux-system: %s", s)
	}
	if FluxNamespace != "gitops-system" {
		t.Errorf("FluxNamespace = %q", FluxNamespace)
	}
	for _, o := range d.Kustomizations {
		if o.Namespace != FluxNamespace {
			t.Errorf("kustomization %s is in namespace %q, want %q", o.Name, o.Namespace, FluxNamespace)
		}
	}
}

// The payload is what the flux twin decorates: one signed and verified source (the pinned one),
// no other source with spec.verify, one suspended release with its reason, nothing else failing.
func TestFluxFixtureHasOneVerifiedSourceAndOneSuspendedRelease(t *testing.T) {
	d := testWorld().Flux()
	var signed []model.FluxObj
	for _, s := range d.Sources {
		if s.SpecVerify {
			signed = append(signed, s)
		}
	}
	if len(signed) != 1 {
		t.Fatalf("%d sources have spec.verify, want 1", len(signed))
	}
	got := signed[0]
	if got.Kind != FluxPinned.Kind || got.Namespace != FluxPinned.Namespace || got.Name != FluxPinned.Name {
		t.Errorf("the signed source is %s %s/%s, want the pinned %+v", got.Kind, got.Namespace, got.Name, FluxPinned)
	}
	if got.Verified == nil || !*got.Verified || !got.Ready {
		t.Errorf("the signed source = %+v, want ready and verified", got)
	}

	var suspended []model.FluxObj
	for _, l := range [][]model.FluxObj{d.Sources, d.Kustomizations, d.HelmReleases} {
		for _, o := range l {
			if o.Suspended {
				suspended = append(suspended, o)
			}
			if !o.Ready {
				t.Errorf("%s %s/%s is not ready", o.Kind, o.Namespace, o.Name)
			}
		}
	}
	if len(suspended) != 1 || suspended[0].Kind != model.FluxKindHelmRelease || suspended[0].SuspendReason == "" {
		t.Errorf("suspended = %+v, want one HelmRelease with a reason", suspended)
	}
}
