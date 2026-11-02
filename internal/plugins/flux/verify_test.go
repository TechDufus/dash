package flux

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The attention rule, the verified KPI and the list of sources the GitOps page reports share one
// scope and one failure test (verify.go). This runs every combination of the inputs through the
// three and compares them with a truth table written out independently:
//
//	in scope  = pinned, or (requireWhenSpecVerify and signed); a missing pin is in scope unless
//	            the list of its kind could not be read
//	fails     = (pinned and not signed) or (in scope and SourceVerified is not True), or missing
//
// The KPI must be present exactly when something is in scope, 0 exactly when something fails, and
// that is exactly when a flux:source-unverified/* item exists.
func TestVerificationScopeIsOneForTheKPIThePageAndTheItems(t *testing.T) {
	type tri int
	const (
		verifiedNil tri = iota
		verifiedFalse
		verifiedTrue
	)
	cases := 0
	for _, pinned := range []bool{false, true} {
		for _, signedSrc := range []bool{false, true} {
			for _, ver := range []tri{verifiedNil, verifiedFalse, verifiedTrue} {
				for _, require := range []bool{false, true} {
					for _, found := range []bool{false, true} {
						for _, unreadable := range []bool{false, true} {
							name := fmt.Sprintf("pinned=%v signed=%v verified=%d require=%v found=%v unreadable=%v",
								pinned, signedSrc, ver, require, found, unreadable)
							t.Run(name, func(t *testing.T) {
								cases++
								cfg := cfgOf(require)
								if pinned {
									cfg.Verify.Sources = []pluginconf.VerifySource{pin(git, "g", "r")}
								}
								var objs []model.FluxObj
								if found {
									var mods []func(*model.FluxObj)
									if signedSrc {
										mods = append(mods, sgn())
									}
									switch ver {
									case verifiedTrue:
										mods = append(mods, vrf(true))
									case verifiedFalse:
										mods = append(mods, vrf(false))
									}
									objs = append(objs, fo(git, "g", "r", mods...))
								}
								var unknown map[model.FluxKind]bool
								if unreadable {
									unknown = map[model.FluxKind]bool{git: true}
								}
								b := built{data: payload(objs...), unknown: unknown}
								b.data.VerifiedSources = verifiedSources(b.data, cfg)

								// The truth table.
								var inScope, fails bool
								switch {
								case found:
									inScope = pinned || (require && signedSrc)
									fails = inScope && ((pinned && !signedSrc) || ver != verifiedTrue)
								case pinned && !unreadable:
									inScope, fails = true, true
								}
								// A source that is not signed and not pinned never reaches the list.
								listed := found && (pinned || (require && signedSrc)) || (!found && pinned)

								k, present := kpi(kpisOf(b, cfg), "verified")
								if present != inScope {
									t.Fatalf("KPI present = %v, want %v", present, inScope)
								}
								if present {
									wantV, wantH := 1.0, model.HealthOK
									if fails {
										wantV, wantH = 0, model.HealthCrit
									}
									if k.Value != wantV || k.Health != wantH {
										t.Errorf("KPI = %v (%s), want %v (%s)", k.Value, k.Health, wantV, wantH)
									}
								}

								var unverified []string
								for _, it := range attentionOf(b, cfg) {
									if strings.HasPrefix(it.ID, "flux:source-unverified/") {
										unverified = append(unverified, it.ID)
									}
								}
								if (len(unverified) > 0) != fails {
									t.Errorf("source-unverified items = %v, want one: %v", unverified, fails)
								}
								if len(unverified) > 1 {
									t.Errorf("source-unverified items = %v: one source, one item", unverified)
								}
								// The tile is 0 exactly when an item exists.
								if present && (k.Value == 0) != (len(unverified) > 0) {
									t.Errorf("KPI value %v with items %v", k.Value, unverified)
								}

								ref := model.FluxSourceRef{Kind: git, Namespace: "g", Name: "r"}
								if got := slices.Contains(b.data.VerifiedSources, ref); got != listed {
									t.Errorf("verifiedSources = %+v, want the source listed: %v", b.data.VerifiedSources, listed)
								}
							})
						}
					}
				}
			}
		}
	}
	if cases != 2*2*3*2*2*2 {
		t.Fatalf("ran %d cases", cases)
	}
}

// With the setting off a signed tenant source raises no item, no critical tile and no banner on the
// page: the pin-only scope the setting restores, through the whole plugin.
func TestVerifyOffRestoresThePinOnlyScope(t *testing.T) {
	r := newRig(t, rigOpts{
		cfg: func(c *pluginconf.Flux) {
			c.Verify.RequireWhenSpecVerify = false
			c.Verify.Sources = []pluginconf.VerifySource{pin(git, "gitops", "root")}
		},
		prefill: func(r *rig) {
			r.setKind(git,
				obj(git, "gitops", "root", ready("ok"), signed(), verified("True")),
				obj(git, "tenant", "app", ready("ok"), signed()), // signed, unverified, not pinned
			)
		},
	})
	d := r.waitPublished("the sources", func(d model.FluxData) bool { return len(d.Sources) == 2 })
	if want := []model.FluxSourceRef{{Kind: git, Namespace: "gitops", Name: "root"}}; !slices.Equal(d.VerifiedSources, want) {
		t.Errorf("verifiedSources = %+v, want %+v", d.VerifiedSources, want)
	}
	eventually(t, "a built contributor", func() bool { return r.attention() != nil })
	if a := r.attention(); len(a) != 0 {
		t.Errorf("attention = %v", itemIDs(a))
	}
	if k, ok := kpi(r.kpis(), "verified"); !ok || k.Value != 1 || k.Health != model.HealthOK {
		t.Errorf("verified = %+v (present %v), want 1", k, ok)
	}
}
