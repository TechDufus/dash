package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// FluxNamespace is the namespace of the fake lab's GitOps controllers and of the Flux objects
// that belong to them. cluster.go lists the same namespace.
const FluxNamespace = "gitops-system"

// FluxPinned is the GitRepository the demo's flux twin is configured to require a verified
// signature of: the one source of the fake lab with spec.verify.
var FluxPinned = model.FluxSourceRef{Kind: model.FluxKindGitRepository, Namespace: FluxNamespace, Name: FluxNamespace}

// Flux is the GitOps payload: sources, kustomizations and helm releases, one of them suspended.
// It carries what the Flux objects themselves say; the plugin adds the object references and the
// list of verified sources.
func (w *World) Flux() model.FluxData { return w.flux(w.clock()) }

func (w *World) flux(now time.Time) model.FluxData {
	var d model.FluxData
	verified := true
	rev := "main@sha1:4f2a9c1e"
	d.Sources = []model.FluxObj{
		{Kind: model.FluxKindGitRepository, Namespace: FluxNamespace, Name: FluxNamespace, Ready: true, Revision: rev, Message: "stored artifact for revision '" + rev + "'", LastTransition: agoP(now, 11*time.Minute), Verified: &verified, SpecVerify: true},
		{Kind: model.FluxKindOCIRepository, Namespace: FluxNamespace, Name: "cilium", Ready: true, Revision: "1.18.2@sha256:9b1d", Message: "stored artifact", LastTransition: agoP(now, 4*24*time.Hour)},
		{Kind: model.FluxKindHelmRepository, Namespace: FluxNamespace, Name: "grafana", Ready: true, Revision: "sha256:1c7e", Message: "stored artifact", LastTransition: agoP(now, 2*time.Hour)},
		{Kind: model.FluxKindHelmRepository, Namespace: FluxNamespace, Name: "jetstack", Ready: true, Revision: "sha256:aa30", Message: "stored artifact", LastTransition: agoP(now, 2*time.Hour)},
	}
	for i, k := range []string{"platform-crds", "platform", "monitoring", "apps", "tenants", "guardrails"} {
		ks := model.FluxObj{Kind: model.FluxKindKustomization, Namespace: FluxNamespace, Name: k, Ready: true, Revision: rev,
			Message: "Applied revision: " + rev, LastTransition: agoP(now, time.Duration(11+i)*time.Minute),
			Links: w.links.Headlamp("Kustomization", FluxNamespace, k)}
		d.Kustomizations = append(d.Kustomizations, ks)
	}
	for _, hr := range []struct{ ns, name, ver string }{
		{"kube-system", "cilium", "1.18.2"}, {"cert-manager", "cert-manager", "v1.19.1"},
		{"monitoring", "victoria-metrics", "0.62.0"}, {"monitoring", "grafana", "10.1.2"},
		{"cnpg-system", "cloudnative-pg", "0.26.0"}, {"headlamp", "headlamp", "0.36.0"},
		{"external-secrets", "external-secrets", "0.20.3"},
	} {
		d.HelmReleases = append(d.HelmReleases, model.FluxObj{
			Kind: model.FluxKindHelmRelease, Namespace: hr.ns, Name: hr.name, Ready: true, Revision: hr.ver,
			Message:        "Helm upgrade succeeded for release " + hr.ns + "/" + hr.name + " with chart " + hr.name + "@" + hr.ver,
			LastTransition: agoP(now, time.Duration(len(hr.name))*3*time.Hour), Links: w.links.Headlamp("HelmRelease", hr.ns, hr.name),
		})
	}
	suspended := model.FluxObj{Kind: model.FluxKindHelmRelease, Namespace: "acme", Name: "acme-preview", Suspended: true,
		SuspendReason: "paused while the preview env is rebuilt", Ready: true, Revision: "1.4.0", Message: "suspended",
		LastTransition: agoP(now, 2*24*time.Hour), Links: w.links.Headlamp("HelmRelease", "acme", "acme-preview")}
	d.HelmReleases = append(d.HelmReleases, suspended)
	return d
}
