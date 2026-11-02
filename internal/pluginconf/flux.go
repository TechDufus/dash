package pluginconf

import (
	"github.com/TechDufus/dash/internal/model"
)

// Flux is plugins.flux: how dash reads Flux objects, which sources must be signature-verified,
// and the reconcile action.
type Flux struct {
	PluginBase
	// SuspendReasonAnnotation on a Flux object is shown as the reason it is suspended.
	SuspendReasonAnnotation string     `json:"suspendReasonAnnotation" doc:"Flux object annotation whose value is shown as the reason the object is suspended. Empty hides the reason."`
	Verify                  FluxVerify `json:"verify" doc:"Which Flux sources must be signature-verified."`
	Actions                 Actions    `json:"actions" doc:"The reconcile action."`
}

// FluxVerify is how the signature checks of the Flux sources are judged.
type FluxVerify struct {
	RequireWhenSpecVerify bool           `json:"requireWhenSpecVerify" doc:"A GitRepository or OCIRepository that has spec.verify must report SourceVerified=True: without it the source is critical on the overview, in the verified tile and on the GitOps page. false puts only the sources listed in sources in scope, for the attention items, the tile and the page alike. The default is true."`
	Sources               []VerifySource `json:"sources" doc:"Sources that must be signed and verified, as [{kind, namespace, name}] with kind GitRepository or OCIRepository. Each is critical on the overview when it lacks spec.verify or SourceVerified=True, or when it does not exist, and each is listed on the GitOps page. A JSON list in the environment."`
}

// VerifySource names one source that must be signature-verified.
type VerifySource struct {
	Kind      string `json:"kind" doc:"GitRepository or OCIRepository."`
	Namespace string `json:"namespace" doc:"Namespace of the source."`
	Name      string `json:"name" doc:"Name of the source."`
}

// DefaultFlux is the section with nothing set: spec.verify is enforced and no source is pinned.
func DefaultFlux() Flux {
	return Flux{Verify: FluxVerify{RequireWhenSpecVerify: true}}
}

// Validate holds the rules of the section the schema cannot express: every pin names a source
// of a kind that can be verified, completely, once.
func (f Flux) Validate() error {
	var p problems
	seen := map[VerifySource]bool{}
	for i, s := range f.Verify.Sources {
		switch model.FluxKind(s.Kind) {
		case model.FluxKindGitRepository, model.FluxKindOCIRepository:
		default:
			p.add("verify.sources", "entry %d has kind %q, which is not GitRepository or OCIRepository", i+1, s.Kind)
			continue
		}
		if s.Namespace == "" || s.Name == "" {
			p.add("verify.sources", "entry %d needs both namespace and name", i+1)
			continue
		}
		if seen[s] {
			p.add("verify.sources", "entry %d repeats %s %s/%s", i+1, s.Kind, s.Namespace, s.Name)
		}
		seen[s] = true
	}
	return p.err()
}

// ActionsConfig implements the action policy hook: reconcile's target has a namespace, so the
// deny list applies.
func (f Flux) ActionsConfig() Actions { return f.Actions }
