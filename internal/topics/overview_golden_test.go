package topics

import (
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

var update = flag.Bool("update", false, "rewrite testdata/overview.golden.json")

const overviewGolden = "testdata/overview.golden.json"

// goldenSnapshots is a lab with something to say in every source: it exercises each attention
// rule, every KPI and each part of the topology.
func goldenSnapshots() built {
	ago := func(d time.Duration) time.Time { return testNow.Add(-d) }
	agoP := func(d time.Duration) *time.Time { return new(ago(d)) }
	return built{
		flux: &model.FluxData{
			Sources: []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: new(false), LastTransition: agoP(time.Hour)},
				{Kind: model.FluxKindOCIRepository, Namespace: "flux-system", Name: "charts", Ready: true, Verified: new(true)}},
			Kustomizations: []model.FluxObj{
				{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "apps", Ready: false, Message: "build failed\nsecond line", LastTransition: agoP(30 * time.Minute)},
				{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "paused", Ready: false, Suspended: true},
				{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "infra", Ready: true},
			},
			HelmReleases: []model.FluxObj{{Kind: model.FluxKindHelmRelease, Namespace: "monitoring", Name: "prom", Ready: true}},
		},
	}
}

// goldenProxmoxLinks stands in for the running proxmox plugin's LinkProvider: the Proxmox UI and
// the host shell of the lab's node, as the plugin builds them (its own tests pin the hrefs).
type goldenProxmoxLinks struct{}

func (goldenProxmoxLinks) Links(req plugin.LinkRequest) []model.Link {
	switch req.Kind {
	case "ProxmoxUI":
		return []model.Link{{Label: "Proxmox UI", Href: "https://pve.example.com:8006/", Kind: model.LinkKindProxmox}}
	case "HostShell":
		return []model.Link{{Label: "Host shell", Href: "https://pve.example.com:8006/?console=shell&novnc=1&node=pve-demo&resize=off&cmd=", Kind: model.LinkKindProxmox}}
	}
	return nil
}

// goldenManager is a Manager that has only what the overview needs.
func goldenManager(t *testing.T) *Manager {
	cfg := config.Defaults()
	lab := append(labLinks(t).(providers), goldenProxmoxLinks{})
	m := New(Deps{Cfg: cfg, LinkProviders: func() plugin.LinkResolver { return lab }})
	m.now = func() time.Time { return testNow }
	return m
}

// goldenAlerts stands in for the running alertmanager plugin: the two firing alerts of the lab
// as its AttentionSource raises them (the rules themselves are the plugin's, tested there), with
// the Grafana link the lab's providers resolve for Kind "Alerts".
type goldenAlerts struct{ lab plugin.LinkResolver }

func (g goldenAlerts) Attention(time.Time) []model.AttentionItem {
	ls := g.lab.Resolve(plugin.LinkRequest{Kind: "Alerts"})
	return []model.AttentionItem{
		{ID: "alert:f1", Severity: model.SeverityCrit, Source: "alertmanager", Plugin: "alertmanager", Title: "NodeDown",
			Detail: "cp-2 is down", Since: testNow.Add(-50 * time.Minute), Route: "/alerts", Links: ls, Ref: "alertmanager:Alert/f1"},
		{ID: "alert:f2", Severity: model.SeverityWarn, Source: "alertmanager", Plugin: "alertmanager", Title: "DiskFilling",
			Detail: "media/data", Since: testNow.Add(-3 * time.Hour), Route: "/alerts", Links: ls, Ref: "alertmanager:Alert/f2"},
	}
}

// goldenGatus stands in for the running gatus plugin: the two failing checks of the lab as its
// AttentionSource raises them (the severity rule and the since time are the plugin's, tested
// there), each with the ref the plugin gives its endpoints.
type goldenGatus struct{}

func (goldenGatus) Attention(time.Time) []model.AttentionItem {
	return []model.AttentionItem{
		{ID: "gatus:infrastructure_unifi", Severity: model.SeverityCrit, Source: "gatus", Plugin: "gatus", Title: "UniFi check failing",
			Detail: "infrastructure", Since: testNow.Add(-4 * time.Minute), Route: "/status", Links: []model.Link{}, Ref: "gatus:Endpoint/infrastructure_unifi"},
		{ID: "gatus:apps_grafana", Severity: model.SeverityWarn, Source: "gatus", Plugin: "gatus", Title: "Grafana check failing",
			Detail: "apps", Since: testNow.Add(-2 * time.Minute), Route: "/status", Links: []model.Link{}, Ref: "gatus:Endpoint/apps_grafana"},
	}
}

// goldenVMs is the lab's guests as the proxmox plugin publishes them.
func goldenVMs() []model.VM {
	return []model.VM{
		{Ref: "proxmox:VM/100", VMID: 100, Name: "k8s-node-1", Status: "running", Health: model.HealthOK, CPURatio: 0.4, MemBytes: 6 << 30, MaxMemBytes: 8 << 30, K8sNode: &model.K8sNodeRef{Name: "cp-1", Ready: true}},
		{Ref: "proxmox:VM/101", VMID: 101, Name: "k8s-node-2", Status: "running", Health: model.HealthWarn, CPURatio: 0.9, MemBytes: 7 << 30, MaxMemBytes: 8 << 30, K8sNode: &model.K8sNodeRef{Name: "cp-2", Ready: false}},
		{Ref: "proxmox:VM/3000", VMID: 3000, Name: "scratch", Status: "stopped", Health: model.HealthWarn, Links: []model.Link{}},
		{Ref: "proxmox:VM/9000", VMID: 9000, Name: "template", Status: "stopped", Template: true},
	}
}

// goldenProxmox stands in for the running proxmox plugin: the attention items, KPIs and topology
// VMs it derives from the lab's guests (the rules are the plugin's, tested there).
type goldenProxmox struct{ vms []model.VM }

func (g goldenProxmox) Attention(time.Time) []model.AttentionItem {
	return []model.AttentionItem{
		{ID: "vm:3000", Severity: model.SeverityWarn, Source: "proxmox", Plugin: "proxmox", Title: "VM scratch is stopped",
			Detail: "VMID 3000", Since: testNow, Route: "/vms/3000", Links: []model.Link{}, Ref: "proxmox:VM/3000"},
	}
}

func (g goldenProxmox) KPIs() []model.KPI {
	return []model.KPI{
		{Key: "vms.running", Plugin: "proxmox", Label: "VMs running", Value: 2, Health: model.HealthWarn},
		{Key: "vms.total", Plugin: "proxmox", Label: "VMs", Value: 3, Health: model.HealthOK},
	}
}

func (g goldenProxmox) Topology() model.Topology {
	t := model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	for _, vm := range g.vms {
		if vm.Template {
			continue
		}
		t.VMs = append(t.VMs, model.TopologyVM{
			VMID: vm.VMID, Name: vm.Name, Health: vm.Health, CPURatio: vm.CPURatio, MemRatio: float64(vm.MemBytes) / max(float64(vm.MaxMemBytes), 1),
			IsK8sNode: vm.K8sNode != nil, Running: vm.Status == "running",
		})
	}
	return t
}

// goldenHardware stands in for the running hardware plugin: the attention items, KPIs and topology
// host it derives from the lab's host (the rules and the thresholds are the plugin's, tested
// there), each item linked to the Proxmox UI and the host shell as the plugin links them.
type goldenHardware struct{ links []model.Link }

func (g goldenHardware) item(id string, sev model.Severity, source, title, detail string, since time.Time, ref string) model.AttentionItem {
	return model.AttentionItem{ID: id, Severity: sev, Source: source, Plugin: "hardware", Title: title, Detail: detail,
		Since: since, Route: "/hardware", Links: g.links, Ref: ref}
}

func (g goldenHardware) Attention(time.Time) []model.AttentionItem {
	return []model.AttentionItem{
		g.item("zfs:pool:bulk", model.SeverityCrit, "zfs", "ZFS pool bulk is degraded", "", testNow, "hardware:Pool/pve-demo/bulk"),
		g.item("zfs:scrub:fast", model.SeverityWarn, "zfs", "ZFS pool fast last scrub found errors", "3 errors", testNow.Add(-48*time.Hour), "hardware:Pool/pve-demo/fast"),
		g.item("smart:4", model.SeverityCrit, "smart", "Disk in bay 4 reports SMART failure", "sdd S1", testNow, "hardware:Disk/pve-demo/S1"),
		g.item("host:cpu-temp", model.SeverityCrit, "hardware", "CPU package at 92 °C", "", testNow, "hardware:Host/pve-demo"),
	}
}

func (goldenHardware) KPIs() []model.KPI {
	return []model.KPI{
		{Key: "power", Plugin: "hardware", Label: "Power", Unit: "W", Value: 412, Health: model.HealthOK},
		{Key: "cpuTemp", Plugin: "hardware", Label: "CPU temp", Unit: "°C", Value: 91.5, Health: model.HealthCrit},
	}
}

func (goldenHardware) Topology() model.Topology {
	return model.Topology{
		Host: model.TopologyHost{Name: "pve-demo", Health: model.HealthCrit, PowerWatts: 412, CPUTempC: 91.5,
			CPUTempHealth: model.HealthCrit, MemUsedRatio: 96.0 / 256.0},
		VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{},
	}
}

// goldenKubernetes stands in for the running kubernetes plugin: the attention items, KPIs and
// topology groups it derives from the lab's cluster and storage (the rules are the plugin's,
// tested there; these are the items the lab had before the plugin took them over, each with the
// ref the plugin now gives it). It makes the golden file pin what the overview carries for a
// cluster, where each item sorts among the other plugins' and the order of the KPIs.
type goldenKubernetes struct{}

func (goldenKubernetes) Attention(time.Time) []model.AttentionItem {
	item := func(id string, sev model.Severity, source, title, detail string, since time.Time, route, ref string) model.AttentionItem {
		return model.AttentionItem{ID: id, Severity: sev, Source: source, Plugin: "kubernetes", Title: title, Detail: detail,
			Since: since, Route: route, Links: []model.Link{}, Ref: ref}
	}
	return []model.AttentionItem{
		item("node:cp-2", model.SeverityCrit, "kubernetes", "Node cp-2 not ready", "192.0.2.12", testNow, "/cluster", "kubernetes:Node/cp-2"),
		item("pod:media/app-0", model.SeverityCrit, "kubernetes", "Pod media/app-0 is failing", "CrashLoopBackOff", testNow.Add(-47*time.Minute), "/cluster/pods/media/app-0", "kubernetes:Pod/media/app-0"),
		item("pod:media/app-1", model.SeverityWarn, "kubernetes", "Pod media/app-1 is stuck pending", "Pending for over 5 minutes", testNow.Add(-20*time.Minute), "/cluster/pods/media/app-1", "kubernetes:Pod/media/app-1"),
		item("workload:Deployment/media/app", model.SeverityCrit, "kubernetes", "Deployment media/app not ready", "0 of 2 ready", testNow, "/cluster", "kubernetes:Workload/Deployment/media/app"),
		item("cert:media/tls", model.SeverityCrit, "cert-manager", "Certificate media/tls expires soon", "expires 2026-11-04 (5 days)", testNow, "/cluster", "kubernetes:Certificate/media/tls"),
		item("pvc:media/data", model.SeverityCrit, "storage", "PVC media/data is 93% full", "", testNow, "/storage", "kubernetes:PVC/media/data"),
		item("pvc:monitoring/tsdb", model.SeverityWarn, "storage", "PVC monitoring/tsdb is 82% full", "", testNow, "/storage", "kubernetes:PVC/monitoring/tsdb"),
		item("bucket:backups", model.SeverityCrit, "storage", "Bucket backups is 95% of its quota", "", testNow, "/storage", ""),
	}
}

func (goldenKubernetes) KPIs() []model.KPI {
	return []model.KPI{
		{Key: "nodes.ready", Plugin: "kubernetes", Label: "Nodes ready", Value: 1, Health: model.HealthCrit},
		{Key: "nodes.total", Plugin: "kubernetes", Label: "Nodes", Value: 2, Health: model.HealthOK},
		{Key: "pvc.worst", Plugin: "kubernetes", Label: "media/data", Value: 0.93, Health: model.HealthCrit},
	}
}

func (goldenKubernetes) Topology() model.Topology {
	group := func(name model.Group, health model.Health, byNode map[string]int, ns ...model.TopologyNamespace) model.TopologyGroup {
		return model.TopologyGroup{Name: name, Health: health, Namespaces: ns, PodsByNode: byNode}
	}
	return model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{
		group(model.GroupPlatform, model.HealthWarn, map[string]int{"cp-1": 2},
			model.TopologyNamespace{Name: "flux-system", Health: model.HealthOK, Pods: 4},
			model.TopologyNamespace{Name: "monitoring", Health: model.HealthWarn, Pods: 6}),
		group(model.GroupApps, model.HealthCrit, map[string]int{"cp-1": 1, "cp-2": 1},
			model.TopologyNamespace{Name: "media", Health: model.HealthCrit, Pods: 3}),
		group(model.GroupTenants, model.HealthOK, map[string]int{"cp-2": 1},
			model.TopologyNamespace{Name: "acme", Health: model.HealthOK, Pods: 2}),
	}}
}

// The overview a lab gets is unchanged by composing it from plugin contributions: the golden file
// was written by the code that built it from the topics directly, and regenerated as each
// integration moved into a plugin of its own. The host's items and KPIs come from the hardware
// contribution (P3.7), and the cluster's items, its three KPIs (nodes.ready, nodes.total,
// pvc.worst, now in kpiList and no longer in kpis) and its topology groups from the kubernetes
// one (P3.8): each item gained its ref, and nothing else changed against v0.8.1. The alerts
// arrive from the alertmanager contribution, and Kpis.alertsFiring is gone (its tile is the
// plugin's KPI). The whole document is compared, kpiList and the order of the items included.
func TestOverviewOutputIsUnchanged(t *testing.T) {
	gp := goldenProxmox{vms: goldenVMs()}
	gh := goldenHardware{links: goldenProxmoxLinks{}.Links(plugin.LinkRequest{Kind: "ProxmoxUI"})}
	gh.links = append(gh.links, goldenProxmoxLinks{}.Links(plugin.LinkRequest{Kind: "HostShell"})...)
	cs := []plugin.Contributor{
		{ID: "alertmanager", Attention: goldenAlerts{lab: labLinks(t)}},
		{ID: "gatus", Attention: goldenGatus{}},
		{ID: "proxmox", Attention: gp, KPIs: gp, Topology: gp},
		{ID: "hardware", Attention: gh, KPIs: gh, Topology: gh},
		{ID: "kubernetes", Attention: goldenKubernetes{}, KPIs: goldenKubernetes{}, Topology: goldenKubernetes{}},
	}
	got, err := json.MarshalIndent(goldenManager(t).buildOverview(goldenSnapshots(), cs, testNow), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(overviewGolden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(overviewGolden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("the overview changed (run with -update to rewrite the golden file if intended):\n got %s\nwant %s", got, want)
	}
}
