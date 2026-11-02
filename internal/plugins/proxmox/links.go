package proxmox

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// The link kinds the plugin answers (plugin.LinkRequest.Kind).
const (
	kindVM        = "VM"
	kindHostShell = "HostShell"
	kindProxmoxUI = "ProxmoxUI"
)

func one(label, href string) []model.Link {
	return []model.Link{{Label: label, Href: href, Kind: model.LinkKindProxmox}}
}

// vmLinks is the noVNC console of a VM: the configured URL with the VM's name and the node
// escaped. It is empty without a URL or a node: a console link with no node opens nothing.
func vmLinks(base, node string, vmid int, name string) []model.Link {
	base = strings.TrimRight(base, "/")
	if base == "" || node == "" {
		return []model.Link{}
	}
	q := "?console=kvm&novnc=1&vmid=" + strconv.Itoa(vmid) +
		"&vmname=" + url.QueryEscape(name) +
		"&node=" + url.QueryEscape(node) + "&resize=off&cmd="
	return one("Console", base+"/"+q)
}

// hostShell is the Proxmox host's shell console; like the VM console it needs the node.
func hostShell(base, node string) []model.Link {
	base = strings.TrimRight(base, "/")
	if base == "" || node == "" {
		return []model.Link{}
	}
	q := "?console=shell&novnc=1&node=" + url.QueryEscape(node) + "&resize=off&cmd="
	return one("Host shell", base+"/"+q)
}

// proxmoxUI is the Proxmox web UI.
func proxmoxUI(base string) []model.Link {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return []model.Link{}
	}
	return one("Proxmox UI", base+"/")
}

// Links implements plugin.LinkProvider: a VM (its console and the UI), the host's shell and the
// Proxmox UI. The node is the one the plugin reads, so it is known after discovery too.
func (p *Plugin) Links(req plugin.LinkRequest) []model.Link {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	node, _ := r.nodeName()
	switch req.Kind {
	case kindVM:
		id, err := strconv.Atoi(req.ID)
		if err != nil || id <= 0 {
			return nil
		}
		return append(vmLinks(r.cfg.URL, node, id, req.Name), proxmoxUI(r.cfg.URL)...)
	case kindHostShell:
		return hostShell(r.cfg.URL, node)
	case kindProxmoxUI:
		return proxmoxUI(r.cfg.URL)
	}
	return nil
}
