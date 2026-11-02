package flux

import (
	"cmp"
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/internal/cond"
	"github.com/TechDufus/dash/internal/svc"
)

// build is the topic's payload from the service's caches: the sources (Git, OCI and Helm
// repositories, each kind sorted on its own), the Kustomizations and the Helm releases, each
// sorted by namespace and name, and the sources whose signature the page reports. A nil c, or a
// resource that is not served, refused or not yet listed, gives an empty list; the lists are
// never nil.
func (r *runtime) build(c svc.Cluster) model.FluxData {
	links := r.h.Links()
	list := func(kind model.FluxKind) []model.FluxObj {
		out := []model.FluxObj{}
		if c == nil {
			return out
		}
		gvr, _ := gvrOf(kind)
		for _, u := range c.List(gvr) {
			out = append(out, objectOf(u, kind, r.cfg.SuspendReasonAnnotation, links))
		}
		slices.SortFunc(out, func(a, b model.FluxObj) int {
			return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
		})
		return out
	}
	d := model.FluxData{Sources: []model.FluxObj{}}
	for _, k := range []model.FluxKind{model.FluxKindGitRepository, model.FluxKindOCIRepository, model.FluxKindHelmRepository} {
		d.Sources = append(d.Sources, list(k)...)
	}
	d.Kustomizations = list(model.FluxKindKustomization)
	d.HelmReleases = list(model.FluxKindHelmRelease)
	d.VerifiedSources = verifiedSources(d, r.cfg)
	return d
}

// objectOf is one Flux object as the page shows it. Ready is the Ready condition being True,
// Message its message and LastTransition its lastTransitionTime (UTC). The revision is where
// each kind keeps it: the last applied one of a Kustomization, the chart version of a Helm
// release's latest history entry (else the last attempted revision), the artifact's for a
// source. A source also says whether it has spec.verify and what its SourceVerified condition
// says (nil when it has none).
func objectOf(u *unstructured.Unstructured, kind model.FluxKind, reasonAnnotation string, links plugin.LinkResolver) model.FluxObj {
	suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	_, msg := cond.Of(u, "Ready")
	o := model.FluxObj{
		Ref:  plugin.Ref(ID, string(kind), u.GetNamespace(), u.GetName()),
		Kind: kind, Namespace: u.GetNamespace(), Name: u.GetName(), Ready: cond.True(u, "Ready"),
		Suspended: suspended, Message: msg, LastTransition: cond.Time(u, "Ready"),
		Links: links.Resolve(plugin.LinkRequest{Kind: string(kind), Namespace: u.GetNamespace(), Name: u.GetName()}),
	}
	if reasonAnnotation != "" {
		o.SuspendReason = u.GetAnnotations()[reasonAnnotation]
	}
	switch kind {
	case model.FluxKindKustomization:
		o.Revision, _, _ = unstructured.NestedString(u.Object, "status", "lastAppliedRevision")
	case model.FluxKindHelmRelease:
		hist, _, _ := unstructured.NestedSlice(u.Object, "status", "history")
		if len(hist) > 0 {
			if h, ok := hist[0].(map[string]any); ok {
				o.Revision, _ = h["chartVersion"].(string)
			}
		}
		if o.Revision == "" {
			o.Revision, _, _ = unstructured.NestedString(u.Object, "status", "lastAttemptedRevision")
		}
	default:
		o.Revision, _, _ = unstructured.NestedString(u.Object, "status", "artifact", "revision")
		if v, found, _ := unstructured.NestedFieldNoCopy(u.Object, "spec", "verify"); found && v != nil {
			o.SpecVerify = true
		}
		if s, _ := cond.Of(u, "SourceVerified"); s != "" {
			o.Verified = new(s == "True")
		}
	}
	return o
}

// verifiedSources are the sources the GitOps page reports the signature of: the pinned ones
// (whether or not they exist) and, while requireWhenSpecVerify is on, every source with
// spec.verify: the scope of the signature check (verify.go), each once, sorted by kind,
// namespace and name. It is never nil.
func verifiedSources(d model.FluxData, cfg pluginconf.Flux) []model.FluxSourceRef {
	out := []model.FluxSourceRef{}
	add := func(ref model.FluxSourceRef) {
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	for _, p := range cfg.Verify.Sources {
		add(pinRef(p))
	}
	for _, s := range d.Sources {
		if verifiable(s.Kind) && inScope(cfg, false, s) {
			add(model.FluxSourceRef{Kind: s.Kind, Namespace: s.Namespace, Name: s.Name})
		}
	}
	slices.SortFunc(out, func(a, b model.FluxSourceRef) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out
}
