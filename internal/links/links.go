// Package links builds deep links into Headlamp, Proxmox and Grafana.
package links

import (
	"net/url"
	"strconv"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// Builder holds the base URLs of the tools dash links to.
type Builder struct {
	headlamp, cluster, proxmox, node, grafana string
}

// New builds a Builder from the configuration.
func New(cfg config.Config) *Builder {
	return &Builder{
		headlamp: cfg.HeadlampURL,
		cluster:  cfg.HeadlampCluster,
		proxmox:  cfg.ProxmoxURL,
		node:     cfg.ProxmoxNode,
		grafana:  cfg.GrafanaURL,
	}
}

// headlampPaths maps a Kubernetes kind to its Headlamp path prefix; the object follows as
// "<ns>/<name>" (namespaced) or "<name>" (cluster-scoped).
var headlampPaths = map[string]struct {
	prefix     string
	namespaced bool
}{
	"Pod":                   {"pods", true},
	"Deployment":            {"deployments", true},
	"StatefulSet":           {"statefulsets", true},
	"DaemonSet":             {"daemonsets", true},
	"Job":                   {"jobs", true},
	"CronJob":               {"cronjobs", true},
	"Node":                  {"nodes", false},
	"Namespace":             {"namespaces", false},
	"PersistentVolumeClaim": {"storage/persistentvolumeclaims", true},
	"Kustomization":         {"customresources/kustomizations.kustomize.toolkit.fluxcd.io", true},
	"HelmRelease":           {"customresources/helmreleases.helm.toolkit.fluxcd.io", true},
	"GitRepository":         {"customresources/gitrepositories.source.toolkit.fluxcd.io", true},
	"OCIRepository":         {"customresources/ocirepositories.source.toolkit.fluxcd.io", true},
	"HelmRepository":        {"customresources/helmrepositories.source.toolkit.fluxcd.io", true},
	"Cluster":               {"customresources/clusters.postgresql.cnpg.io", true},
	"Certificate":           {"customresources/certificates.cert-manager.io", true},
}

// HeadlampURL returns the Headlamp URL of an object; ok is false for an unknown kind.
func (b *Builder) HeadlampURL(kind, namespace, name string) (string, bool) {
	p, ok := headlampPaths[kind]
	if !ok {
		return "", false
	}
	u := b.headlamp + "/c/" + url.PathEscape(b.cluster) + "/" + p.prefix + "/"
	if p.namespaced {
		u += url.PathEscape(namespace) + "/"
	}
	return u + url.PathEscape(name), true
}

// Headlamp returns the "Open in Headlamp" link for an object, or an empty slice for an unknown kind.
func (b *Builder) Headlamp(kind, namespace, name string) []model.Link {
	href, ok := b.HeadlampURL(kind, namespace, name)
	if !ok {
		return []model.Link{}
	}
	return []model.Link{{Label: "Open in Headlamp", Href: href, Kind: model.LinkKindHeadlamp}}
}

// VMConsole is the noVNC console of a VM.
func (b *Builder) VMConsole(vmid int, name string) model.Link {
	q := "?console=kvm&novnc=1&vmid=" + strconv.Itoa(vmid) +
		"&vmname=" + url.QueryEscape(name) +
		"&node=" + url.QueryEscape(b.node) + "&resize=off&cmd="
	return model.Link{Label: "Console", Href: b.proxmox + "/" + q, Kind: model.LinkKindProxmox}
}

// HostShell is the Proxmox host's shell console.
func (b *Builder) HostShell() model.Link {
	q := "?console=shell&novnc=1&node=" + url.QueryEscape(b.node) + "&resize=off&cmd="
	return model.Link{Label: "Host shell", Href: b.proxmox + "/" + q, Kind: model.LinkKindProxmox}
}

// ProxmoxUI is the Proxmox web UI.
func (b *Builder) ProxmoxUI() model.Link {
	return model.Link{Label: "Proxmox UI", Href: b.proxmox + "/", Kind: model.LinkKindProxmox}
}

// GrafanaAlerts is Grafana's alert list.
func (b *Builder) GrafanaAlerts() model.Link {
	return model.Link{Label: "Grafana alerts", Href: b.grafana + "/alerting/list", Kind: model.LinkKindGrafana}
}

// Grafana is the Grafana home page.
func (b *Builder) Grafana() model.Link {
	return model.Link{Label: "Grafana", Href: b.grafana + "/", Kind: model.LinkKindGrafana}
}
