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
		cluster: &model.ClusterData{
			Nodes: []model.Node{{Name: "cp-1", IP: "192.0.2.11", Ready: true}, {Name: "cp-2", IP: "192.0.2.12", Ready: false}},
			Namespaces: []model.Namespace{
				{Name: "flux-system", Group: model.GroupPlatform, PodsTotal: 4, Health: model.HealthOK},
				{Name: "monitoring", Group: model.GroupPlatform, PodsTotal: 6, Health: model.HealthWarn},
				{Name: "media", Group: model.GroupApps, PodsTotal: 3, Health: model.HealthCrit},
				{Name: "acme", Group: model.GroupTenants, PodsTotal: 2, Health: model.HealthOK},
				{Name: "stray", Group: "", PodsTotal: 1, Health: model.HealthCrit},
			},
			Pods: []model.Pod{
				{Namespace: "media", Name: "app-0", Phase: "Running", Node: "cp-1", Health: model.HealthCrit, Reason: "CrashLoopBackOff", CreatedAt: ago(47 * time.Minute)},
				{Namespace: "media", Name: "app-1", Phase: "Pending", Node: "cp-2", Health: model.HealthWarn, CreatedAt: ago(20 * time.Minute)},
				{Namespace: "monitoring", Name: "prom-0", Phase: "Running", Node: "cp-1", Health: model.HealthOK},
				{Namespace: "monitoring", Name: "prom-1", Phase: "Running", Node: "cp-1", Health: model.HealthOK},
				{Namespace: "acme", Name: "web-0", Phase: "Running", Node: "cp-2", Health: model.HealthOK},
				{Namespace: "acme", Name: "job-x", Phase: "Succeeded", Node: "cp-2", Health: model.HealthOK},
				{Namespace: "flux-system", Name: "source-0", Phase: "Running", Node: "", Health: model.HealthOK},
			},
			Workloads: []model.Workload{
				{Kind: "Deployment", Namespace: "media", Name: "app", Ready: 0, Desired: 2, Health: model.HealthCrit},
				{Kind: "StatefulSet", Namespace: "monitoring", Name: "prom", Ready: 2, Desired: 2, Health: model.HealthOK},
			},
			Certificates: []model.Cert{
				{Namespace: "media", Name: "tls", NotAfter: agoP(-5 * 24 * time.Hour)},
				{Namespace: "acme", Name: "tls", NotAfter: agoP(-90 * 24 * time.Hour)},
				{Namespace: "acme", Name: "pending"},
			},
		},
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
		storage: &model.StorageData{
			PVCs: []model.PVC{
				{Namespace: "media", Name: "data", UsedRatio: new(0.93)},
				{Namespace: "monitoring", Name: "tsdb", UsedRatio: new(0.82)},
				{Namespace: "acme", Name: "db", UsedRatio: new(0.2)},
				{Namespace: "acme", Name: "new"},
			},
			Buckets: []model.Bucket{{Name: "backups", UsedBytes: 95, QuotaBytes: 100}, {Name: "scratch", UsedBytes: 5}},
		},
		apps: &model.AppsData{},
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

// withoutNewFields drops what the overview gained when it became a composition of plugin
// contributions (the plugin of each item, the KPI list), so the rest can be compared with what
// the overview was before.
func withoutNewFields(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "kpiList")
	items, _ := doc["attention"].([]any)
	for _, it := range items {
		delete(it.(map[string]any), "plugin")
	}
	return doc
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

// The overview a lab gets is unchanged by composing it from plugin contributions: the golden file
// was written by the code that built it from the topics directly, and regenerated when the host
// moved into the hardware plugin (P3.7): the host's items, its two KPIs (now in kpiList, no longer
// in kpis) and the host in the topology come from its contribution; nothing else changed. The
// alerts arrive from the alertmanager contribution, and Kpis.alertsFiring is gone (its tile is
// the plugin's KPI).
func TestOverviewOutputIsUnchanged(t *testing.T) {
	gp := goldenProxmox{vms: goldenVMs()}
	gh := goldenHardware{links: goldenProxmoxLinks{}.Links(plugin.LinkRequest{Kind: "ProxmoxUI"})}
	gh.links = append(gh.links, goldenProxmoxLinks{}.Links(plugin.LinkRequest{Kind: "HostShell"})...)
	cs := []plugin.Contributor{
		{ID: "alertmanager", Attention: goldenAlerts{lab: labLinks(t)}},
		{ID: "gatus", Attention: goldenGatus{}},
		{ID: "proxmox", Attention: gp, KPIs: gp, Topology: gp},
		{ID: "hardware", Attention: gh, KPIs: gh, Topology: gh},
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
	gotDoc, wantDoc := withoutNewFields(t, got), withoutNewFields(t, want)
	g, _ := json.MarshalIndent(gotDoc, "", "  ")
	w, _ := json.MarshalIndent(wantDoc, "", "  ")
	if string(g) != string(w) {
		t.Errorf("the overview changed:\n got %s\nwant %s", g, w)
	}
}
