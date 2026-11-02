package proxmox

import (
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

const (
	lab  = "https://pve.example.com:8006"
	node = "pve-1"
)

// The cases of the old links.Builder, which dies with this leaf: the same hrefs for the same
// node, id and name, with the name and the node escaped.
func TestProxmoxLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  []model.Link
		want string
	}{
		{"console", vmLinks(lab, node, 2020, "k8s-cp-1"),
			"https://pve.example.com:8006/?console=kvm&novnc=1&vmid=2020&vmname=k8s-cp-1&node=pve-1&resize=off&cmd="},
		{"console name escaped", vmLinks(lab, node, 100, "a b"),
			"https://pve.example.com:8006/?console=kvm&novnc=1&vmid=100&vmname=a+b&node=pve-1&resize=off&cmd="},
		{"console node escaped", vmLinks(lab, "pve 1&x", 100, "a"),
			"https://pve.example.com:8006/?console=kvm&novnc=1&vmid=100&vmname=a&node=pve+1%26x&resize=off&cmd="},
		{"shell", hostShell(lab, node),
			"https://pve.example.com:8006/?console=shell&novnc=1&node=pve-1&resize=off&cmd="},
		{"ui", proxmoxUI(lab), "https://pve.example.com:8006/"},
		{"a base with a trailing slash", proxmoxUI(lab + "/"), "https://pve.example.com:8006/"},
	} {
		if len(tc.got) != 1 || tc.got[0].Href != tc.want {
			t.Errorf("%s: got %+v, want %q", tc.name, tc.got, tc.want)
		}
	}
	if vmLinks(lab, node, 1, "x")[0].Kind != model.LinkKindProxmox {
		t.Error("wrong link kind")
	}
}

// Without a URL there is no link; without a node there is no console or shell, only the UI.
func TestUnconfiguredToolsGiveNoLinks(t *testing.T) {
	for name, got := range map[string][]model.Link{
		"console":  vmLinks("", node, 1, "x"),
		"no node":  vmLinks(lab, "", 1, "x"),
		"shell":    hostShell("", node),
		"no shell": hostShell(lab, ""),
		"ui":       proxmoxUI(""),
	} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s: want an empty non-nil slice, got %#v", name, got)
		}
	}
}

// The plugin answers the three kinds through the resolver, with the node it reads, and nothing
// for a kind that is not its own.
func TestLinkProviderKinds(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, plugin.StateHealthy)
	r := e.sup.Links()
	href := func(req plugin.LinkRequest) []string {
		var out []string
		for _, l := range r.Resolve(req) {
			if l.Kind != model.LinkKindProxmox {
				t.Errorf("link kind = %q", l.Kind)
			}
			out = append(out, l.Label+" "+l.Href)
		}
		return out
	}
	base := e.pve.URL
	want := []string{
		"Console " + base + "/?console=kvm&novnc=1&vmid=101&vmname=k8s-cp-1&node=pve-1&resize=off&cmd=",
		"Proxmox UI " + base + "/",
	}
	if got := href(plugin.LinkRequest{Kind: "VM", ID: "101", Name: "k8s-cp-1"}); !slices.Equal(got, want) {
		t.Errorf("VM: %v, want %v", got, want)
	}
	if got := href(plugin.LinkRequest{Kind: "HostShell"}); !slices.Equal(got, []string{"Host shell " + base + "/?console=shell&novnc=1&node=pve-1&resize=off&cmd="}) {
		t.Errorf("HostShell: %v", got)
	}
	if got := href(plugin.LinkRequest{Kind: "ProxmoxUI"}); !slices.Equal(got, []string{"Proxmox UI " + base + "/"}) {
		t.Errorf("ProxmoxUI: %v", got)
	}
	for _, req := range []plugin.LinkRequest{
		{Kind: "Pod", Namespace: "media", Name: "app-0"}, {Kind: "Host"}, {Kind: "VM", ID: "nope"}, {Kind: "VM"}, {Kind: "VM", ID: "-4"},
	} {
		if got := href(req); len(got) != 0 {
			t.Errorf("%+v: %v, want nothing", req, got)
		}
	}
}

// The links follow the node the plugin found: with the node unset they appear after discovery,
// not before, and the config is never mutated (the links read the plugin's own node).
func TestLinksReadTheDiscoveredNode(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/nodes", 500)
	cfg := func(c *pluginconf.Proxmox) { c.Node = "" }
	e := newEnv(t, envOpts{pve: pve, cfg: cfg})
	waitFor(t, "discovery to retry", func() bool { return pve.count("/nodes") >= 2 })
	console := func() int {
		n := 0
		for _, l := range e.sup.Links().Resolve(plugin.LinkRequest{Kind: "HostShell"}) {
			if l.Label == "Host shell" {
				n++
			}
		}
		return n
	}
	if console() != 0 {
		t.Error("a shell link before the node is known")
	}
	pve.heal("/nodes")
	waitFor(t, "the shell link", func() bool { return console() == 1 })
	if e.cfg.Node != "" {
		t.Errorf("the configuration was mutated: node = %q", e.cfg.Node)
	}
}
