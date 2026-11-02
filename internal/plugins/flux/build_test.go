package flux

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The topic's shape is the shim's: the sources (Git, OCI, Helm, each kind sorted on its own), the
// Kustomizations and the Helm releases, each sorted by namespace and name, with the revision where
// each kind keeps it, the Ready condition's message and time in UTC, and the source's signature.
func TestBuildPayloadShape(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Flux) { c.SuspendReasonAnnotation = "example.com/why" }})
	r.setKind(git,
		obj(git, "gitops", "zeta", ready("stored artifact"), statusField("main@sha1:aaaa", "artifact", "revision")),
		obj(git, "gitops", "alpha", ready("stored artifact"), signed(), verified("True"),
			statusField("main@sha1:bbbb", "artifact", "revision")),
		obj(git, "apps", "mid", notReady("auth failed\nsecond line")),
	)
	r.setKind(oci, obj(oci, "gitops", "charts", ready("ok"), signed(), verified("False"),
		statusField("1.2.3@sha256:cc", "artifact", "revision")))
	r.setKind(hlm, obj(hlm, "gitops", "stable", ready("ok"), statusField("sha256:dd", "artifact", "revision")))
	r.setKind(kst,
		obj(kst, "gitops", "platform", ready("Applied revision: main@sha1:bbbb"), statusField("main@sha1:bbbb", "lastAppliedRevision")),
		obj(kst, "apps", "web", ready("ok")),
	)
	r.setKind(hr,
		obj(hr, "media", "withhistory", ready("ok"),
			statusField([]any{map[string]any{"chartVersion": "2.0.1"}, map[string]any{"chartVersion": "1.9.9"}}, "history"),
			statusField("2.0.2", "lastAttemptedRevision")),
		obj(hr, "media", "attempted", notReady("upgrade failed"), statusField("3.1.0", "lastAttemptedRevision")),
		obj(hr, "media", "paused", ready("ok"), suspended(), annotated("example.com/why", "waiting for the volume")),
	)
	d := r.waitPublished("every list", func(d model.FluxData) bool {
		return len(d.Sources) == 5 && len(d.Kustomizations) == 2 && len(d.HelmReleases) == 3
	})

	var order []string
	for _, o := range d.Sources {
		order = append(order, o.Ref)
	}
	if want := []string{
		"flux:GitRepository/apps/mid", "flux:GitRepository/gitops/alpha", "flux:GitRepository/gitops/zeta",
		"flux:OCIRepository/gitops/charts", "flux:HelmRepository/gitops/stable",
	}; !slices.Equal(order, want) {
		t.Errorf("sources = %v, want %v", order, want)
	}
	if got := d.Kustomizations[0].Ref + " " + d.Kustomizations[1].Ref; got != "flux:Kustomization/apps/web flux:Kustomization/gitops/platform" {
		t.Errorf("kustomizations = %s", got)
	}

	for _, tc := range []struct {
		ref, revision, message string
	}{
		{"flux:GitRepository/gitops/alpha", "main@sha1:bbbb", "stored artifact"},
		{"flux:GitRepository/apps/mid", "", "auth failed\nsecond line"},
		{"flux:OCIRepository/gitops/charts", "1.2.3@sha256:cc", "ok"},
		{"flux:HelmRepository/gitops/stable", "sha256:dd", "ok"},
		{"flux:Kustomization/gitops/platform", "main@sha1:bbbb", "Applied revision: main@sha1:bbbb"},
		{"flux:HelmRelease/media/withhistory", "2.0.1", "ok"},
		{"flux:HelmRelease/media/attempted", "3.1.0", "upgrade failed"},
	} {
		o, ok := find(d, tc.ref)
		if !ok {
			t.Errorf("no %s", tc.ref)
			continue
		}
		if o.Revision != tc.revision || o.Message != tc.message {
			t.Errorf("%s: revision %q, message %q; want %q, %q", tc.ref, o.Revision, o.Message, tc.revision, tc.message)
		}
	}

	alpha, _ := find(d, "flux:GitRepository/gitops/alpha")
	if !alpha.Ready || alpha.Suspended || !alpha.SpecVerify || alpha.Verified == nil || !*alpha.Verified ||
		alpha.LastTransition == nil || !alpha.LastTransition.Equal(t0) || alpha.LastTransition.Location() != time.UTC {
		t.Errorf("alpha = %+v", alpha)
	}
	if o, _ := find(d, "flux:OCIRepository/gitops/charts"); o.Verified == nil || *o.Verified || !o.SpecVerify {
		t.Errorf("a source whose SourceVerified is False: %+v", o)
	}
	// verified is set only when the condition exists; specVerify only for spec.verify.
	if o, _ := find(d, "flux:GitRepository/gitops/zeta"); o.Verified != nil || o.SpecVerify {
		t.Errorf("a source with neither: %+v", o)
	}
	if o, _ := find(d, "flux:Kustomization/apps/web"); o.Verified != nil || o.SpecVerify || o.LastTransition == nil {
		t.Errorf("a kustomization: %+v", o)
	}
	if o, _ := find(d, "flux:GitRepository/apps/mid"); o.Ready || !o.LastTransition.Equal(t0) {
		t.Errorf("a source that is not ready: %+v", o)
	}
	if p, _ := find(d, "flux:HelmRelease/media/paused"); !p.Suspended || p.SuspendReason != "waiting for the volume" {
		t.Errorf("a suspended release: %+v", p)
	}
	for _, o := range append(append(slices.Clone(d.Sources), d.Kustomizations...), d.HelmReleases...) {
		if o.Links == nil {
			t.Errorf("%s has nil links", o.Ref)
		}
	}

	// Every object carries its ref, and they are unique: the kind is part of the id.
	plugintest.AssertRefs(t, r.sup, ID, d, model.FluxObj{})
}

// An object without a lastTransitionTime has no time, and a Ready condition that is absent is not
// ready; the lists of an empty cluster are empty, not null.
func TestBuildEmptyAndSparseObjects(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.setKind(kst, obj(kst, "g", "bare"))
	r.waitPublished("the kustomization", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 })
	d, _, _ := r.published()
	if o := d.Kustomizations[0]; o.Ready || o.LastTransition != nil || o.Message != "" || o.Revision != "" {
		t.Errorf("a bare object = %+v", o)
	}

	e := newRig(t, rigOpts{})
	e.waitState("healthy")
	ev, ok := e.hub.Latest("", topic)
	if !ok {
		t.Fatal("nothing published")
	}
	for _, key := range []string{`"sources":[]`, `"kustomizations":[]`, `"helmReleases":[]`, `"verifiedSources":[]`} {
		if !strings.Contains(string(ev.Data), key) {
			t.Errorf("an empty cluster's payload lacks %s: %s", key, ev.Data)
		}
	}
}

// A Git repository and a Kustomization with one name share a ref prefix but not a ref.
func TestRefsAreUniqueUnderCollidingNames(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.setKind(git, obj(git, "gitops", "root", ready("ok")))
	r.setKind(oci, obj(oci, "gitops", "root", ready("ok")))
	r.setKind(kst, obj(kst, "gitops", "root", ready("ok")))
	d := r.waitPublished("the objects", func(d model.FluxData) bool { return len(d.Sources) == 2 && len(d.Kustomizations) == 1 })
	refs := map[string]bool{}
	for _, o := range append(slices.Clone(d.Sources), d.Kustomizations...) {
		if refs[o.Ref] {
			t.Errorf("ref %s twice", o.Ref)
		}
		refs[o.Ref] = true
	}
	plugintest.AssertRefs(t, r.sup, ID, d, model.FluxObj{})
	for ref := range refs {
		if p, _, _, ok := plugin.ParseRef(ref); !ok || p != ID {
			t.Errorf("ref %q does not resolve to flux", ref)
		}
	}
}

func TestVerifiedSourcesListsPinsAndSignedSources(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Flux) {
		c.Verify.Sources = []pluginconf.VerifySource{
			pin(oci, "gitops", "charts"), pin(git, "gitops", "gone"), pin(git, "gitops", "root"),
		}
	}})
	r.setKind(git,
		obj(git, "gitops", "root", ready("ok"), signed(), verified("True")),
		obj(git, "gitops", "extra", ready("ok"), signed()), // signed, not pinned
		obj(git, "gitops", "plain", ready("ok")),           // neither
	)
	r.setKind(oci, obj(oci, "gitops", "charts", ready("ok"), signed(), verified("True")))
	r.setKind(hlm, obj(hlm, "gitops", "stable", ready("ok")))
	d := r.waitPublished("the sources", func(d model.FluxData) bool { return len(d.Sources) == 5 })
	want := []model.FluxSourceRef{
		{Kind: git, Namespace: "gitops", Name: "extra"},
		{Kind: git, Namespace: "gitops", Name: "gone"},
		{Kind: git, Namespace: "gitops", Name: "root"},
		{Kind: oci, Namespace: "gitops", Name: "charts"},
	}
	if !slices.Equal(d.VerifiedSources, want) {
		t.Errorf("verifiedSources = %+v, want %+v (pins and signed sources, each once, sorted)", d.VerifiedSources, want)
	}
}

// The unit view of the same list: each source once, sorted, never nil.
func TestVerifiedSourcesOfAPayload(t *testing.T) {
	d := payload(fo(git, "b", "x", sgn()), fo(git, "a", "x", sgn()), fo(hlm, "a", "h"))
	got := verifiedSources(d, cfgOf(true, pin(git, "b", "x"), pin(oci, "a", "y")))
	want := []model.FluxSourceRef{{Kind: git, Namespace: "a", Name: "x"}, {Kind: git, Namespace: "b", Name: "x"}, {Kind: oci, Namespace: "a", Name: "y"}}
	if !slices.Equal(got, want) {
		t.Errorf("verifiedSources = %+v, want %+v", got, want)
	}
	if got := verifiedSources(payload(), cfgOf(true)); got == nil || len(got) != 0 {
		t.Errorf("verifiedSources of nothing = %#v, want an empty non-nil list", got)
	}
}

// The links come from the running link providers, per kind, namespace and name.
func TestObjectsCarryTheLinksOfTheProviders(t *testing.T) {
	r := newRig(t, rigOpts{links: func(req plugin.LinkRequest) []model.Link {
		if req.Kind != "Kustomization" {
			return nil
		}
		return []model.Link{{Label: "Headlamp", Href: "https://headlamp.example.com/" + req.Namespace + "/" + req.Name}}
	}})
	r.setKind(kst, obj(kst, "gitops", "platform", ready("ok")))
	r.setKind(hr, obj(hr, "media", "app", ready("ok")))
	d := r.waitPublished("the objects", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 && len(d.HelmReleases) == 1 })
	if got := d.Kustomizations[0].Links; len(got) != 1 || got[0].Href != "https://headlamp.example.com/gitops/platform" {
		t.Errorf("kustomization links = %+v", got)
	}
	if got := d.HelmReleases[0].Links; got == nil || len(got) != 0 {
		t.Errorf("a release the provider has no link for: %#v, want an empty list", got)
	}
}
