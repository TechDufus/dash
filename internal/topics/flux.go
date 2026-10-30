package topics

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
)

const annotationSuspended = "homelab.io/suspended"

func (m *Manager) fluxObjects(gvr schema.GroupVersionResource, kind model.FluxKind) []model.FluxObj {
	out := []model.FluxObj{}
	for _, u := range m.d.Cluster.List(gvr) {
		out = append(out, m.fluxObj(u, kind))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) fluxObj(u *unstructured.Unstructured, kind model.FluxKind) model.FluxObj {
	suspended, _, _ := unstructured.NestedBool(u.Object, "spec", "suspend")
	_, msg := conditionOf(u, "Ready")
	o := model.FluxObj{
		Kind: kind, Namespace: u.GetNamespace(), Name: u.GetName(), Ready: conditionTrue(u, "Ready"),
		Suspended: suspended, SuspendReason: u.GetAnnotations()[annotationSuspended], Message: msg,
		LastTransition: conditionTime(u, "Ready"),
		Links:          m.d.Links.Headlamp(string(kind), u.GetNamespace(), u.GetName()),
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
		if s, _ := conditionOf(u, "SourceVerified"); s != "" {
			o.Verified = new(s == "True")
		}
	}
	return o
}

func (m *Manager) buildFlux() model.FluxData {
	d := model.FluxData{Sources: []model.FluxObj{}}
	d.Sources = append(d.Sources, m.fluxObjects(kube.GVRGitRepositories, model.FluxKindGitRepository)...)
	d.Sources = append(d.Sources, m.fluxObjects(kube.GVROCIRepositories, model.FluxKindOCIRepository)...)
	d.Sources = append(d.Sources, m.fluxObjects(kube.GVRHelmRepositories, model.FluxKindHelmRepository)...)
	d.Kustomizations = m.fluxObjects(kube.GVRKustomizations, model.FluxKindKustomization)
	d.HelmReleases = m.fluxObjects(kube.GVRHelmReleases, model.FluxKindHelmRelease)
	return d
}
