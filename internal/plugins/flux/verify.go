package flux

import (
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The signature check has one scope and one failure test, shared by the attention rule, the
// verified KPI and the list of sources the GitOps page reports, so the three cannot disagree:
//
//   - a GitRepository or OCIRepository is in scope when it is pinned (verify.sources), or when
//     verify.requireWhenSpecVerify is on and it has spec.verify;
//   - a source in scope fails when it is pinned and has no spec.verify, or when it does not have
//     SourceVerified=True;
//   - a pin that no source matches is in scope and fails ("pinned source not found"), unless the
//     list of its kind could not be read: then nothing is known and it is out of scope.
//
// The KPI is present exactly when something is in scope and is 0 exactly when something fails,
// which is exactly when a flux:source-unverified/* item exists.

// verdict is one source in scope: the source (nil for a pin that was not found) and what is wrong
// with it, "" when nothing is.
type verdict struct {
	ref    model.FluxSourceRef
	obj    *model.FluxObj
	detail string
}

// failed reports that the source in scope fails the check.
func (v verdict) failed() bool { return v.detail != "" }

// verifiable reports that a source of kind can carry spec.verify and a SourceVerified condition.
func verifiable(kind model.FluxKind) bool {
	return kind == model.FluxKindGitRepository || kind == model.FluxKindOCIRepository
}

// pinsOf is the configured pins as a set.
func pinsOf(cfg pluginconf.Flux) map[model.FluxSourceRef]bool {
	out := map[model.FluxSourceRef]bool{}
	for _, p := range cfg.Verify.Sources {
		out[pinRef(p)] = true
	}
	return out
}

func pinRef(p pluginconf.VerifySource) model.FluxSourceRef {
	return model.FluxSourceRef{Kind: model.FluxKind(p.Kind), Namespace: p.Namespace, Name: p.Name}
}

// inScope is the scope test of a source that exists.
func inScope(cfg pluginconf.Flux, pinned bool, o model.FluxObj) bool {
	return pinned || (cfg.Verify.RequireWhenSpecVerify && o.SpecVerify)
}

// verdicts are the sources in scope, in payload order, and then the pins that were not found
// (and whose kind could be read), in the order they were configured.
func verdicts(b built, cfg pluginconf.Flux) []verdict {
	pinned := pinsOf(cfg)
	var out []verdict
	found := map[model.FluxSourceRef]bool{}
	for i := range b.data.Sources {
		o := &b.data.Sources[i]
		if !verifiable(o.Kind) {
			continue
		}
		ref := model.FluxSourceRef{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
		found[ref] = true
		if !inScope(cfg, pinned[ref], *o) {
			continue
		}
		v := verdict{ref: ref, obj: o}
		switch {
		case pinned[ref] && !o.SpecVerify:
			v.detail = "the pinned source has no spec.verify"
		case o.Verified == nil || !*o.Verified:
			v.detail = "SourceVerified is not True"
		}
		out = append(out, v)
	}
	for _, p := range cfg.Verify.Sources {
		ref := pinRef(p)
		if found[ref] || b.unknown[ref.Kind] {
			continue
		}
		out = append(out, verdict{ref: ref, detail: "pinned source not found"})
	}
	return out
}
