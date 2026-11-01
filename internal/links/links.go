// Package links builds deep links into Proxmox. The links into Headlamp and Grafana come from
// their plugins (plugin.LinkProvider), through plugin.LinkResolver.
package links

import (
	"net/url"
	"strconv"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// Builder holds the base URL and node of the Proxmox host dash links to.
type Builder struct {
	proxmox, node string
}

// New builds a Builder from the configuration.
func New(cfg config.Config) *Builder {
	return &Builder{proxmox: cfg.ProxmoxURL, node: cfg.ProxmoxNode}
}

// one returns a single link, or none when its tool's base URL is not configured.
func one(base, label, href string, kind model.LinkKind) []model.Link {
	if base == "" {
		return []model.Link{}
	}
	return []model.Link{{Label: label, Href: href, Kind: kind}}
}

// VMConsole is the noVNC console of a VM.
func (b *Builder) VMConsole(vmid int, name string) []model.Link {
	q := "?console=kvm&novnc=1&vmid=" + strconv.Itoa(vmid) +
		"&vmname=" + url.QueryEscape(name) +
		"&node=" + url.QueryEscape(b.node) + "&resize=off&cmd="
	return one(b.proxmox, "Console", b.proxmox+"/"+q, model.LinkKindProxmox)
}

// HostShell is the Proxmox host's shell console.
func (b *Builder) HostShell() []model.Link {
	q := "?console=shell&novnc=1&node=" + url.QueryEscape(b.node) + "&resize=off&cmd="
	return one(b.proxmox, "Host shell", b.proxmox+"/"+q, model.LinkKindProxmox)
}

// ProxmoxUI is the Proxmox web UI.
func (b *Builder) ProxmoxUI() []model.Link {
	return one(b.proxmox, "Proxmox UI", b.proxmox+"/", model.LinkKindProxmox)
}
