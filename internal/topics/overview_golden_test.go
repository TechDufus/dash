package topics

import (
	"encoding/json"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
)

var update = flag.Bool("update", false, "rewrite testdata/overview.golden.json")

const overviewGolden = "testdata/overview.golden.json"

// goldenSnapshots is a lab with something to say in every source: it exercises each attention
// rule, every KPI and each part of the topology.
func goldenSnapshots() built {
	ago := func(d time.Duration) time.Time { return testNow.Add(-d) }
	agoP := func(d time.Duration) *time.Time { return new(ago(d)) }
	return built{
		host: &model.HostData{
			Name: "pve-demo", PowerWatts: 412, MemUsedBytes: 96 << 30, MemTotalBytes: 256 << 30, PackageTempsC: []float64{71, 91.5},
			Pools: []model.Pool{{Name: "bulk", State: "degraded"}, {Name: "fast", State: "online", ScrubErrors: 3, LastScrubEnd: agoP(48 * time.Hour)}},
			Disks: []model.Disk{{Bay: "4", Serial: "S1", Device: "sdd", Healthy: false}, {Bay: "5", Healthy: true}},
		},
		vms: &model.VMsData{VMs: []model.VM{
			{VMID: 100, Name: "talos-cp-1", Status: "running", Health: model.HealthOK, CPURatio: 0.4, MemBytes: 6 << 30, MaxMemBytes: 8 << 30, K8sNode: &model.K8sNodeRef{Name: "cp-1", Ready: true}},
			{VMID: 101, Name: "talos-cp-2", Status: "running", Health: model.HealthWarn, CPURatio: 0.9, MemBytes: 7 << 30, MaxMemBytes: 8 << 30, K8sNode: &model.K8sNodeRef{Name: "cp-2", Ready: false}},
			{VMID: 3000, Name: "scratch", Status: "stopped", Health: model.HealthWarn},
			{VMID: 9000, Name: "template", Status: "stopped", Template: true},
		}},
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
		alerts: &model.AlertsData{Alerts: []model.Alert{
			{Fingerprint: "f1", Name: "NodeDown", Severity: "critical", State: "active", Summary: "cp-2 is down", StartsAt: ago(50 * time.Minute)},
			{Fingerprint: "f2", Name: "DiskFilling", Severity: "warning", State: "active", Description: "media/data", StartsAt: ago(3 * time.Hour)},
			{Fingerprint: "f3", Name: "Silenced", Severity: "critical", State: "suppressed", SilencedBy: []string{"s1"}},
			{Fingerprint: "f4", Name: "Watchdog", Severity: "critical", State: "active"},
		}},
		status: &model.StatusData{Endpoints: []model.Endpoint{
			{Group: "infrastructure", Name: "UniFi", Key: "infrastructure_unifi", Healthy: false, Results: []model.CheckResult{
				{Success: true, Timestamp: ago(5 * time.Minute)}, {Success: false, Timestamp: ago(4 * time.Minute)}, {Success: false, Timestamp: ago(time.Minute)}}},
			{Group: "apps", Name: "Grafana", Key: "apps_grafana", Healthy: false, Results: []model.CheckResult{{Success: false, Timestamp: ago(2 * time.Minute)}}},
			{Group: "apps", Name: "Wiki", Key: "apps_wiki", Healthy: true, Results: []model.CheckResult{{Success: true, Timestamp: ago(time.Minute)}}},
		}},
		apps: &model.AppsData{},
	}
}

// goldenManager is a Manager that has only what the overview needs.
func goldenManager() *Manager {
	cfg := config.Defaults()
	cfg.HeadlampURL, cfg.HeadlampCluster = "https://headlamp.example.com", "demo"
	cfg.GrafanaURL, cfg.ProxmoxURL, cfg.ProxmoxNode = "https://grafana.example.com", "https://pve.example.com:8006", "pve-demo"
	m := New(Deps{Cfg: cfg, Links: links.New(cfg)})
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

// The overview a lab gets is unchanged by composing it from plugin contributions: the golden file
// was written by the code that built it from the nine topics directly.
func TestOverviewOutputIsUnchanged(t *testing.T) {
	got, err := json.MarshalIndent(goldenManager().buildOverview(goldenSnapshots(), nil, testNow), "", "  ")
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
