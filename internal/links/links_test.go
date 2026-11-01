package links

import (
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

func testBuilder() *Builder {
	cfg, err := config.Load([]string{
		"DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006",
		"DASH_PLUGINS_PROXMOX_NODE=pve-1",
		"DASH_SERVER_PUBLIC_URL=https://dash.example.com",
	})
	if err != nil {
		panic(err)
	}
	return New(cfg)
}

func TestProxmoxLinks(t *testing.T) {
	b := testBuilder()
	tests := []struct {
		name string
		got  []model.Link
		want string
	}{
		{"console", b.VMConsole(2020, "talos-cp-1"),
			"https://pve.example.com:8006/?console=kvm&novnc=1&vmid=2020&vmname=talos-cp-1&node=pve-1&resize=off&cmd="},
		{"console name escaped", b.VMConsole(100, "a b"),
			"https://pve.example.com:8006/?console=kvm&novnc=1&vmid=100&vmname=a+b&node=pve-1&resize=off&cmd="},
		{"shell", b.HostShell(),
			"https://pve.example.com:8006/?console=shell&novnc=1&node=pve-1&resize=off&cmd="},
		{"ui", b.ProxmoxUI(), "https://pve.example.com:8006/"},
	}
	for _, tc := range tests {
		if len(tc.got) != 1 || tc.got[0].Href != tc.want {
			t.Errorf("%s: got %+v, want %q", tc.name, tc.got, tc.want)
		}
	}
	if b.VMConsole(1, "x")[0].Kind != model.LinkKindProxmox {
		t.Error("wrong link kind")
	}
}

func TestUnconfiguredToolsGiveNoLinks(t *testing.T) {
	b := New(config.Defaults())
	for name, got := range map[string][]model.Link{
		"console": b.VMConsole(1, "x"), "shell": b.HostShell(), "ui": b.ProxmoxUI(),
	} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s: want an empty non-nil slice, got %#v", name, got)
		}
	}
}
