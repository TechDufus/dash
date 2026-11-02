package topics

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/svc"
)

// fluxGVRs are the five resources the flux topic reads: the Manager asks the kubernetes plugin to
// watch them (watchCluster) and builds the topic once each is listed or absent.
var fluxGVRs = []schema.GroupVersionResource{
	kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
	kube.GVRKustomizations, kube.GVRHelmReleases,
}

// cluster is the kubernetes plugin's svc.Cluster while the plugin runs, else false. It is looked
// up per call: the plugin restarts on a reload and its service changes with it.
func (m *Manager) cluster() (svc.Cluster, bool) {
	if m.d.Cluster == nil {
		return nil, false
	}
	c, ok := m.d.Cluster()
	if !ok || c == nil {
		return nil, false
	}
	return c, true
}

// fluxListed reports that every flux resource has completed its first list, or is absent from the
// cluster: the flux topic never computes from a partial list.
func fluxListed(c svc.Cluster) bool {
	for _, gvr := range fluxGVRs {
		if !c.Listed(gvr) {
			return false
		}
	}
	return true
}

func (m *Manager) fluxObjects(c svc.Cluster, gvr schema.GroupVersionResource, kind model.FluxKind) []model.FluxObj {
	out := []model.FluxObj{}
	for _, u := range c.List(gvr) {
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
		Suspended: suspended, SuspendReason: u.GetAnnotations()[m.d.Cfg.Conventions.SuspendReasonAnnotation], Message: msg,
		LastTransition: conditionTime(u, "Ready"),
		Links:          m.link(string(kind), u.GetNamespace(), u.GetName()),
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

func (m *Manager) buildFlux(c svc.Cluster) model.FluxData {
	d := model.FluxData{Sources: []model.FluxObj{}}
	d.Sources = append(d.Sources, m.fluxObjects(c, kube.GVRGitRepositories, model.FluxKindGitRepository)...)
	d.Sources = append(d.Sources, m.fluxObjects(c, kube.GVROCIRepositories, model.FluxKindOCIRepository)...)
	d.Sources = append(d.Sources, m.fluxObjects(c, kube.GVRHelmRepositories, model.FluxKindHelmRepository)...)
	d.Kustomizations = m.fluxObjects(c, kube.GVRKustomizations, model.FluxKindKustomization)
	d.HelmReleases = m.fluxObjects(c, kube.GVRHelmReleases, model.FluxKindHelmRelease)
	return d
}

// conditionOf returns the status and message of a named status.condition of an unstructured object.
func conditionOf(u *unstructured.Unstructured, typ string) (status, message string) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		status, _ = cm["status"].(string)
		message, _ = cm["message"].(string)
		return status, message
	}
	return "", ""
}

func conditionTrue(u *unstructured.Unstructured, typ string) bool {
	s, _ := conditionOf(u, typ)
	return s == "True"
}

// conditionTime returns the lastTransitionTime of a status condition.
func conditionTime(u *unstructured.Unstructured, typ string) *time.Time {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		cm, ok := c.(map[string]any)
		if !ok || cm["type"] != typ {
			continue
		}
		if s, _ := cm["lastTransitionTime"].(string); s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return new(t.UTC())
			}
		}
	}
	return nil
}
