package flux

import (
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// fo is a payload object of kind, as build makes it: a Ready one unless a change says otherwise.
func fo(kind model.FluxKind, ns, name string, mods ...func(*model.FluxObj)) model.FluxObj {
	o := model.FluxObj{
		Ref: plugin.Ref(ID, string(kind), ns, name), Kind: kind, Namespace: ns, Name: name, Ready: true,
		Links: []model.Link{},
	}
	for _, m := range mods {
		m(&o)
	}
	return o
}

func notRdy(msg string) func(*model.FluxObj) {
	return func(o *model.FluxObj) { o.Ready, o.Message, o.LastTransition = false, msg, new(t0) }
}
func susp(reason string) func(*model.FluxObj) {
	return func(o *model.FluxObj) { o.Suspended, o.SuspendReason = true, reason }
}
func sgn() func(*model.FluxObj) { return func(o *model.FluxObj) { o.SpecVerify = true } }
func vrf(ok bool) func(*model.FluxObj) {
	return func(o *model.FluxObj) { o.Verified, o.LastTransition = &ok, new(t0) }
}

// payload puts the objects in the list their kind belongs to.
func payload(objs ...model.FluxObj) model.FluxData {
	d := model.FluxData{Sources: []model.FluxObj{}, Kustomizations: []model.FluxObj{}, HelmReleases: []model.FluxObj{}}
	for _, o := range objs {
		switch o.Kind {
		case model.FluxKindKustomization:
			d.Kustomizations = append(d.Kustomizations, o)
		case model.FluxKindHelmRelease:
			d.HelmReleases = append(d.HelmReleases, o)
		default:
			d.Sources = append(d.Sources, o)
		}
	}
	return d
}

func pin(kind model.FluxKind, ns, name string) pluginconf.VerifySource {
	return pluginconf.VerifySource{Kind: string(kind), Namespace: ns, Name: name}
}

// cfgOf is the default section with the given pins and requireWhenSpecVerify.
func cfgOf(require bool, pins ...pluginconf.VerifySource) pluginconf.Flux {
	c := pluginconf.DefaultFlux()
	c.Verify.RequireWhenSpecVerify = require
	c.Verify.Sources = pins
	return c
}

const (
	git = model.FluxKindGitRepository
	oci = model.FluxKindOCIRepository
	hlm = model.FluxKindHelmRepository
	kst = model.FluxKindKustomization
	hr  = model.FluxKindHelmRelease
)

// The rules of the attention source (O-3), one row each.
func TestFluxRules(t *testing.T) {
	root := fo(git, "gitops", "root", sgn(), vrf(true))
	for _, tc := range []struct {
		name    string
		cfg     pluginconf.Flux
		objs    []model.FluxObj
		unknown map[model.FluxKind]bool
		want    []string
	}{
		{"a healthy cluster", cfgOf(true), []model.FluxObj{root, fo(kst, "gitops", "apps"), fo(hr, "media", "app")}, nil, nil},
		{"spec.verify without the condition is critical", cfgOf(true),
			[]model.FluxObj{fo(git, "gitops", "root", sgn())}, nil, []string{"flux:source-unverified/GitRepository/gitops/root"}},
		{"spec.verify with the condition False is critical", cfgOf(true),
			[]model.FluxObj{fo(oci, "gitops", "charts", sgn(), vrf(false))}, nil, []string{"flux:source-unverified/OCIRepository/gitops/charts"}},
		{"spec.verify with the condition True is fine", cfgOf(true), []model.FluxObj{root}, nil, nil},
		{"requireWhenSpecVerify off and no pin ignores spec.verify", cfgOf(false),
			[]model.FluxObj{fo(git, "gitops", "root", sgn())}, nil, nil},
		{"no spec.verify and no pin is not checked", cfgOf(true), []model.FluxObj{fo(git, "gitops", "root")}, nil, nil},
		{"a pin with the condition and spec.verify is fine", cfgOf(false, pin(git, "gitops", "root")), []model.FluxObj{root}, nil, nil},
		{"a pin without spec.verify is critical", cfgOf(true, pin(git, "gitops", "root")),
			[]model.FluxObj{fo(git, "gitops", "root", vrf(true))}, nil, []string{"flux:source-unverified/GitRepository/gitops/root"}},
		{"a pin without the condition is critical even with requireWhenSpecVerify off", cfgOf(false, pin(git, "gitops", "root")),
			[]model.FluxObj{fo(git, "gitops", "root", sgn())}, nil, []string{"flux:source-unverified/GitRepository/gitops/root"}},
		{"a pin that is missing is critical", cfgOf(true, pin(git, "gitops", "root")),
			[]model.FluxObj{fo(kst, "gitops", "apps")}, nil, []string{"flux:source-unverified/GitRepository/gitops/root"}},
		{"a pin of the other kind does not match", cfgOf(true, pin(oci, "gitops", "root")),
			[]model.FluxObj{root}, nil, []string{"flux:source-unverified/OCIRepository/gitops/root"}},
		{"a pin whose list is refused is not missing", cfgOf(true, pin(git, "gitops", "root")),
			nil, map[model.FluxKind]bool{git: true}, nil},
		{"a refused list of the other kind does not hide the pin", cfgOf(true, pin(git, "gitops", "root")),
			nil, map[model.FluxKind]bool{oci: true}, []string{"flux:source-unverified/GitRepository/gitops/root"}},
		{"a not-ready object warns", cfgOf(true), []model.FluxObj{fo(kst, "gitops", "apps", notRdy("build failed"))}, nil,
			[]string{"flux:Kustomization/gitops/apps"}},
		{"a suspended object warns, and not also as not ready", cfgOf(true),
			[]model.FluxObj{fo(hr, "media", "app", notRdy("x"), susp("paused"))}, nil, []string{"flux:suspended/HelmRelease/media/app"}},
		{"a suspended source warns", cfgOf(true), []model.FluxObj{fo(hlm, "gitops", "charts", susp(""))}, nil,
			[]string{"flux:suspended/HelmRepository/gitops/charts"}},
		{"an unverified source that is also suspended is both", cfgOf(true),
			[]model.FluxObj{fo(git, "gitops", "root", sgn(), susp("freeze"))}, nil,
			[]string{"flux:suspended/GitRepository/gitops/root", "flux:source-unverified/GitRepository/gitops/root"}},
		{"payload order, then the missing pins", cfgOf(true, pin(git, "a", "gone")),
			[]model.FluxObj{fo(hr, "m", "x", notRdy("")), fo(kst, "g", "y", notRdy("")), fo(oci, "g", "z", sgn())}, nil,
			[]string{"flux:source-unverified/OCIRepository/g/z", "flux:Kustomization/g/y", "flux:HelmRelease/m/x", "flux:source-unverified/GitRepository/a/gone"}},
		{"a Kustomization with the same name as a pinned source is not the pin", cfgOf(true, pin(git, "gitops", "root")),
			[]model.FluxObj{fo(kst, "gitops", "root")}, nil, []string{"flux:source-unverified/GitRepository/gitops/root"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := attentionOf(built{data: payload(tc.objs...), unknown: tc.unknown}, tc.cfg)
			if items == nil {
				t.Fatal("attentionOf returned nil: a built payload with nothing to raise is a non-nil empty list")
			}
			if got := itemIDs(items); !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
				t.Fatalf("items = %v, want %v", got, tc.want)
			}
			seen := map[string]bool{}
			for _, it := range items {
				if seen[it.ID] {
					t.Errorf("duplicate item id %s", it.ID)
				}
				seen[it.ID] = true
				if it.Plugin != "flux" || it.Source != "flux" || it.Route != "/gitops" || it.Links == nil || it.Ref == "" {
					t.Errorf("item = %+v", it)
				}
				if _, _, _, ok := plugin.ParseRef(it.Ref); !ok {
					t.Errorf("item %s has the invalid ref %q", it.ID, it.Ref)
				}
			}
		})
	}
}

// The item's text and its severity, time and reference, for each rule.
func TestFluxItemsCarryTheirDetail(t *testing.T) {
	cfg := cfgOf(true, pin(git, "gitops", "gone"))
	d := payload(
		fo(git, "gitops", "root", sgn(), notRdy("first line\nsecond line")),
		fo(hr, "media", "app", susp("paused for the move")),
		fo(hr, "media", "other", susp("")),
	)
	items := attentionOf(built{data: d}, cfg)
	byID := func(id string) model.AttentionItem {
		it, ok := item(items, id)
		if !ok {
			t.Fatalf("no item %s in %v", id, itemIDs(items))
		}
		return it
	}

	nr := byID("flux:GitRepository/gitops/root")
	if nr.Severity != model.SeverityWarn || nr.Detail != "first line" || !nr.Since.Equal(t0) || nr.Title != "GitRepository gitops/root not ready" {
		t.Errorf("not ready = %+v", nr)
	}
	un := byID("flux:source-unverified/GitRepository/gitops/root")
	if un.Severity != model.SeverityCrit || un.Detail != "SourceVerified is not True" || !un.Since.Equal(t0) {
		t.Errorf("unverified = %+v", un)
	}
	if un.Ref != "flux:GitRepository/gitops/root" {
		t.Errorf("unverified ref = %q", un.Ref)
	}
	s := byID("flux:suspended/HelmRelease/media/app")
	if s.Severity != model.SeverityWarn || s.Detail != "paused for the move" || !s.Since.IsZero() || s.Title != "HelmRelease media/app suspended" {
		t.Errorf("suspended = %+v: no time of its own, so the supervisor stamps it", s)
	}
	if o := byID("flux:suspended/HelmRelease/media/other"); o.Detail != "suspended" {
		t.Errorf("a suspended object with no reason says %q, want \"suspended\"", o.Detail)
	}
	missing := byID("flux:source-unverified/GitRepository/gitops/gone")
	if missing.Severity != model.SeverityCrit || missing.Detail != "pinned source not found" || !missing.Since.IsZero() ||
		missing.Ref != "flux:GitRepository/gitops/gone" {
		t.Errorf("missing pin = %+v", missing)
	}
	for _, it := range []model.AttentionItem{un, missing} {
		if _, _, _, ok := plugin.ParseRef(it.Ref); !ok {
			t.Errorf("ref %q", it.Ref)
		}
	}
}

func TestSpecVerifyWithoutTheConditionIsCritical(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.setKind(git, obj(git, "gitops", "root", ready("ok"), signed()))
	r.waitState("healthy")
	eventually(t, "the critical item", func() bool {
		it, ok := item(r.attention(), "flux:source-unverified/GitRepository/gitops/root")
		return ok && it.Severity == model.SeverityCrit
	})
	// With the condition True it goes.
	r.setKind(git, obj(git, "gitops", "root", ready("ok"), signed(), verified("True")))
	eventually(t, "the item to go", func() bool {
		a := r.attention()
		return a != nil && len(a) == 0
	})
}

func TestVerifyOffIgnoresSpecVerify(t *testing.T) {
	r := newRig(t, rigOpts{
		cfg:     func(c *pluginconf.Flux) { c.Verify.RequireWhenSpecVerify = false },
		prefill: func(r *rig) { r.setKind(git, obj(git, "gitops", "root", ready("ok"), signed())) },
	})
	r.waitPublished("the source", func(d model.FluxData) bool { return len(d.Sources) == 1 })
	eventually(t, "a built contributor", func() bool { return r.attention() != nil })
	if a := r.attention(); len(a) != 0 {
		t.Errorf("attention = %v, want an empty list: spec.verify is ignored without a pin", itemIDs(a))
	}
	// The same scope for the tile and the page: with the setting off and nothing pinned, nothing is
	// checked, so the tile is absent and the page lists no source.
	if k, ok := kpi(r.kpis(), "verified"); ok {
		t.Errorf("verified = %+v: the KPI follows the setting, so a signed source is out of scope", k)
	}
	if d, _, _ := r.published(); len(d.VerifiedSources) != 0 {
		t.Errorf("verifiedSources = %+v, want none while the setting is off and nothing is pinned", d.VerifiedSources)
	}
}

func TestPinWithoutSpecVerifyIsCritical(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Flux) {
		c.Verify.Sources = []pluginconf.VerifySource{pin(git, "gitops", "root")}
	}})
	r.setKind(git, obj(git, "gitops", "root", ready("ok"), verified("True")))
	eventually(t, "the pin to be critical", func() bool {
		it, ok := item(r.attention(), "flux:source-unverified/GitRepository/gitops/root")
		return ok && it.Detail == "the pinned source has no spec.verify"
	})
	k, ok := kpi(r.kpis(), "verified")
	if !ok || k.Value != 0 || k.Health != model.HealthCrit {
		t.Errorf("verified = %+v (present %v), want 0 and critical", k, ok)
	}
}

func TestSuspendedObjectWarnsWithTheAnnotation(t *testing.T) {
	const key = "example.com/suspend-reason"
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.Flux) { c.SuspendReasonAnnotation = key }})
	r.setKind(hr,
		obj(hr, "media", "app", ready("ok"), suspended(), annotated(key, "migrating the volume")),
		obj(hr, "media", "bare", ready("ok"), suspended()),
	)
	d := r.waitPublished("the releases", func(d model.FluxData) bool { return len(d.HelmReleases) == 2 })
	if o, _ := find(d, "flux:HelmRelease/media/app"); !o.Suspended || o.SuspendReason != "migrating the volume" {
		t.Errorf("object = %+v", o)
	}
	var items []model.AttentionItem
	eventually(t, "the suspended items", func() bool { items = r.attention(); return len(items) == 2 })
	if it, _ := item(items, "flux:suspended/HelmRelease/media/app"); it.Severity != model.SeverityWarn || it.Detail != "migrating the volume" {
		t.Errorf("with the annotation: %+v", it)
	}
	if it, _ := item(items, "flux:suspended/HelmRelease/media/bare"); it.Detail != "suspended" {
		t.Errorf("without the annotation: %+v", it)
	}
	// The supervisor stamped them with the time they were first seen, and keeps it.
	first := items[0].Since
	if !first.Equal(t0) {
		t.Fatalf("since = %v, want the first sighting %v", first, t0)
	}
	r.clk.advance(time.Hour)
	if again := r.attention(); !again[0].Since.Equal(t0) {
		t.Errorf("since moved to %v", again[0].Since)
	}
}

// An annotation that is not configured shows no reason, whatever the object carries.
func TestSuspendReasonNeedsTheSetting(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.setKind(hr, obj(hr, "media", "app", ready("ok"), suspended(), annotated("example.com/suspend-reason", "x"), annotated("", "empty key")))
	d := r.waitPublished("the release", func(d model.FluxData) bool { return len(d.HelmReleases) == 1 })
	if o := d.HelmReleases[0]; o.SuspendReason != "" {
		t.Errorf("suspendReason = %q with no annotation configured", o.SuspendReason)
	}
}

// A contributor that has built and has nothing to raise answers a non-nil empty list; nil is for
// a plugin that is hidden or has not built, so a resumed object prunes its sighting and a second
// suspension gets a new time.
func TestAttentionIsNonNilWhenNothingToRaise(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.setKind(hr, obj(hr, "media", "app", ready("ok")))
	r.waitState("healthy")
	eventually(t, "a built contributor", func() bool { return r.attention() != nil })
	c, ok := r.contributor()
	if !ok {
		t.Fatal("flux does not contribute")
	}
	plugintest.AssertAttentionNonNil(t, c)

	r.setKind(hr, obj(hr, "media", "app", ready("ok"), suspended()))
	eventually(t, "the suspension", func() bool { return len(r.attention()) == 1 })
	suspendedAt := r.attention()[0].Since

	r.setKind(hr, obj(hr, "media", "app", ready("ok")))
	eventually(t, "the item to go", func() bool { a := r.attention(); return a != nil && len(a) == 0 })

	r.clk.advance(2 * time.Hour)
	r.setKind(hr, obj(hr, "media", "app", ready("ok"), suspended()))
	eventually(t, "the second suspension", func() bool { return len(r.attention()) == 1 })
	if again := r.attention()[0].Since; !again.After(suspendedAt) {
		t.Errorf("a second suspension has since %v, want later than %v: the first sighting was pruned", again, suspendedAt)
	}
}

// Before the plugin has built there is no opinion: nil, not an empty list.
func TestAttentionIsNilBeforeAnythingIsBuilt(t *testing.T) {
	p := &Plugin{}
	if got := p.Attention(t0); got != nil {
		t.Errorf("Attention before Start = %v", got)
	}
	if got := p.KPIs(); got != nil {
		t.Errorf("KPIs before Start = %v", got)
	}
}
