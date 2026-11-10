package proxmox

import (
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
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

// The plugin answers the three kinds through the resolver: a VM's console on the node it runs on, a
// named node's shell (the host node's without a name), and nothing for a kind that is not its own.
func TestLinkProviderKinds(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, plugin.StateHealthy)
	waitFor(t, "the cluster", func() bool { _, ok := e.current().Service().(svc.ClusterNodes).Nodes(); return ok })
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
	want = []string{
		"Console " + base + "/?console=kvm&novnc=1&vmid=102&vmname=db-1&node=pve-2&resize=off&cmd=",
		"Proxmox UI " + base + "/",
	}
	if got := href(plugin.LinkRequest{Kind: "VM", ID: "102", Name: "db-1"}); !slices.Equal(got, want) {
		t.Errorf("a VM on pve-2: %v, want %v", got, want)
	}
	if got := href(plugin.LinkRequest{Kind: "HostShell"}); !slices.Equal(got, []string{"Host shell " + base + "/?console=shell&novnc=1&node=pve-1&resize=off&cmd="}) {
		t.Errorf("HostShell: %v", got)
	}
	if got := href(plugin.LinkRequest{Kind: "HostShell", Name: "pve-2"}); !slices.Equal(got, []string{"Host shell " + base + "/?console=shell&novnc=1&node=pve-2&resize=off&cmd="}) {
		t.Errorf("HostShell of pve-2: %v", got)
	}
	if got := href(plugin.LinkRequest{Kind: "ProxmoxUI"}); !slices.Equal(got, []string{"Proxmox UI " + base + "/"}) {
		t.Errorf("ProxmoxUI: %v", got)
	}
	for _, req := range []plugin.LinkRequest{
		{Kind: "Pod", Namespace: "media", Name: "app-0"}, {Kind: "Host"}, {Kind: "VM", ID: "nope"}, {Kind: "VM"}, {Kind: "VM", ID: "-4"},
		{Kind: "HostShell", Name: "pve-9"},
	} {
		if got := href(req); len(got) != 0 {
			t.Errorf("%+v: %v, want nothing", req, got)
		}
	}
}

// Without plugins.proxmox.node a cluster of several nodes has no host node: a shell request without
// a name gets nothing, a named node its shell, and the config is never mutated.
func TestHostShellWithoutAHostNode(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Proxmox) { c.Node = "" }})
	waitFor(t, "the cluster", func() bool { _, ok := e.current().Service().(svc.ClusterNodes).Nodes(); return ok })
	shells := func(name string) int {
		return len(e.sup.Links().Resolve(plugin.LinkRequest{Kind: "HostShell", Name: name}))
	}
	if shells("") != 0 || shells("pve-2") != 1 {
		t.Errorf("shells: %d without a name, %d for pve-2; want 0 and 1", shells(""), shells("pve-2"))
	}
	if e.cfg.Node != "" {
		t.Errorf("the configuration was mutated: node = %q", e.cfg.Node)
	}
}
