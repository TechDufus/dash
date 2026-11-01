package links

import (
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

func testBuilder() *Builder {
	cfg, err := config.Load([]string{
		"DASH_PROXMOX_URL=https://pve.example.com:8006",
		"DASH_PROXMOX_NODE=pve-1",
		"DASH_HEADLAMP_URL=https://headlamp.example.com",
		"DASH_GRAFANA_URL=https://grafana.example.com",
		"DASH_PUBLIC_URL=https://dash.example.com",
	})
	if err != nil {
		panic(err)
	}
	return New(cfg)
}

func TestHeadlampKinds(t *testing.T) {
	const base = "https://headlamp.example.com/c/main/"
	tests := []struct {
		kind, ns, name, want string
	}{
		{"Pod", "homepage", "homepage-abc", base + "pods/homepage/homepage-abc"},
		{"Deployment", "homepage", "homepage", base + "deployments/homepage/homepage"},
		{"StatefulSet", "monitoring", "vmsingle", base + "statefulsets/monitoring/vmsingle"},
		{"DaemonSet", "kube-system", "cilium", base + "daemonsets/kube-system/cilium"},
		{"Job", "rustfs", "j", base + "jobs/rustfs/j"},
		{"CronJob", "rustfs", "nas-backup", base + "cronjobs/rustfs/nas-backup"},
		{"Node", "", "talos-cp-1", base + "nodes/talos-cp-1"},
		{"Namespace", "", "monitoring", base + "namespaces/monitoring"},
		{"PersistentVolumeClaim", "monitoring", "data", base + "storage/persistentvolumeclaims/monitoring/data"},
		{"Kustomization", "flux-system", "app-homepage", base + "customresources/kustomizations.kustomize.toolkit.fluxcd.io/flux-system/app-homepage"},
		{"HelmRelease", "monitoring", "vm", base + "customresources/helmreleases.helm.toolkit.fluxcd.io/monitoring/vm"},
		{"GitRepository", "flux-system", "flux-system", base + "customresources/gitrepositories.source.toolkit.fluxcd.io/flux-system/flux-system"},
		{"OCIRepository", "flux-system", "x", base + "customresources/ocirepositories.source.toolkit.fluxcd.io/flux-system/x"},
		{"HelmRepository", "flux-system", "x", base + "customresources/helmrepositories.source.toolkit.fluxcd.io/flux-system/x"},
		{"Cluster", "db", "pg", base + "customresources/clusters.postgresql.cnpg.io/db/pg"},
		{"Certificate", "gateway", "wild", base + "customresources/certificates.cert-manager.io/gateway/wild"},
	}
	b := testBuilder()
	for _, tc := range tests {
		got, ok := b.HeadlampURL(tc.kind, tc.ns, tc.name)
		if !ok || got != tc.want {
			t.Errorf("%s: got %q ok=%v, want %q", tc.kind, got, ok, tc.want)
		}
		l := b.Headlamp(tc.kind, tc.ns, tc.name)
		if len(l) != 1 || l[0].Kind != model.LinkKindHeadlamp || l[0].Href != tc.want {
			t.Errorf("%s: link %+v", tc.kind, l)
		}
	}
	if l := b.Headlamp("Widget", "a", "b"); l == nil || len(l) != 0 {
		t.Errorf("unknown kind must give an empty non-nil slice, got %#v", l)
	}
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
		{"grafana alerts", b.GrafanaAlerts(), "https://grafana.example.com/alerting/list"},
		{"grafana", b.Grafana(), "https://grafana.example.com/"},
	}
	for _, tc := range tests {
		if len(tc.got) != 1 || tc.got[0].Href != tc.want {
			t.Errorf("%s: got %+v, want %q", tc.name, tc.got, tc.want)
		}
	}
	if b.VMConsole(1, "x")[0].Kind != model.LinkKindProxmox || b.Grafana()[0].Kind != model.LinkKindGrafana {
		t.Error("wrong link kinds")
	}
}

func TestUnconfiguredToolsGiveNoLinks(t *testing.T) {
	b := New(config.Defaults())
	for name, got := range map[string][]model.Link{
		"console": b.VMConsole(1, "x"), "shell": b.HostShell(), "ui": b.ProxmoxUI(),
		"grafana alerts": b.GrafanaAlerts(), "grafana": b.Grafana(), "headlamp": b.Headlamp("Pod", "a", "b"),
	} {
		if got == nil || len(got) != 0 {
			t.Errorf("%s: want an empty non-nil slice, got %#v", name, got)
		}
	}
}
