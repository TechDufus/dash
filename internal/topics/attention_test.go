package topics

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
)

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func input() AttentionInput {
	return AttentionInput{Links: links.New(config.Load(func(string) string { return "" })), Now: testNow}
}

// severityOf returns the severity of the item with id, or "" when there is none.
func severityOf(items []model.AttentionItem, id string) model.Severity {
	for _, it := range items {
		if it.ID == id {
			return it.Severity
		}
	}
	return ""
}

func TestRatioBoundaries(t *testing.T) {
	tests := []struct {
		ratio float64
		want  model.Health
	}{
		{0.0, model.HealthOK}, {0.79, model.HealthOK}, {0.80, model.HealthWarn},
		{0.89, model.HealthWarn}, {0.90, model.HealthCrit}, {1.0, model.HealthCrit},
	}
	for _, tc := range tests {
		if got := RatioHealth(tc.ratio); got != tc.want {
			t.Errorf("RatioHealth(%v) = %s, want %s", tc.ratio, got, tc.want)
		}
		// The same rule applies through the attention table for PVCs and buckets.
		in := input()
		in.Storage.PVCs = []model.PVC{{Namespace: "n", Name: "p", UsedRatio: new(tc.ratio)}}
		in.Storage.Buckets = []model.Bucket{{Name: "b", UsedBytes: int64(tc.ratio * 1000), QuotaBytes: 1000}}
		items := Attention(in, nil)
		for _, id := range []string{"pvc:n/p", "bucket:b"} {
			want := model.Severity(tc.want)
			if tc.want == model.HealthOK {
				want = ""
			}
			if got := severityOf(items, id); got != want {
				t.Errorf("ratio %v: %s severity = %q, want %q", tc.ratio, id, got, want)
			}
		}
	}
}

func TestBucketWithoutQuotaIsIgnored(t *testing.T) {
	in := input()
	in.Storage.Buckets = []model.Bucket{{Name: "b", UsedBytes: 999, QuotaBytes: 0}}
	if items := Attention(in, nil); len(items) != 0 {
		t.Fatalf("no quota must not raise attention, got %+v", items)
	}
}

func TestCertBoundaries(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		name string
		left time.Duration
		want model.Health
	}{
		{"30 days", 30 * day, model.HealthOK},
		{"just over 21 days", 21*day + time.Second, model.HealthOK},
		{"exactly 21 days", 21 * day, model.HealthOK},
		{"just under 21 days", 21*day - time.Second, model.HealthWarn},
		{"exactly 7 days", 7 * day, model.HealthWarn},
		{"just under 7 days", 7*day - time.Second, model.HealthCrit},
		{"expired", -day, model.HealthCrit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CertHealth(testNow.Add(tc.left), testNow); got != tc.want {
				t.Errorf("CertHealth = %s, want %s", got, tc.want)
			}
			in := input()
			in.Cluster.Certificates = []model.Cert{{Namespace: "gateway", Name: "wild", NotAfter: new(testNow.Add(tc.left))}}
			want := model.Severity(tc.want)
			if tc.want == model.HealthOK {
				want = ""
			}
			if got := severityOf(Attention(in, nil), "cert:gateway/wild"); got != want {
				t.Errorf("attention severity = %q, want %q", got, want)
			}
		})
	}
}

func TestVMHealth(t *testing.T) {
	tests := []struct {
		name     string
		vmid     int
		status   string
		template bool
		want     model.Health
	}{
		{"talos running", 2020, "running", false, model.HealthOK},
		{"talos stopped", 2020, "stopped", false, model.HealthCrit},
		{"talos 2022 stopped", 2022, "stopped", false, model.HealthCrit},
		{"2023 is not talos", 2023, "stopped", false, model.HealthWarn},
		{"omp-cloud stopped", 3000, "stopped", false, model.HealthWarn},
		{"template stopped", 9000, "stopped", true, model.HealthOK},
	}
	for _, tc := range tests {
		if got := VMHealth(tc.vmid, tc.status, tc.template); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}

	in := input()
	in.VMs.VMs = []model.VM{
		{VMID: 2020, Name: "talos-cp-1", Status: "stopped"},
		{VMID: 3000, Name: "omp-cloud", Status: "stopped"},
		{VMID: 9000, Name: "tmpl", Status: "stopped", Template: true},
		{VMID: 2021, Name: "talos-cp-2", Status: "running"},
	}
	items := Attention(in, nil)
	if got := severityOf(items, "vm:2020"); got != model.SeverityCrit {
		t.Errorf("talos stopped = %q, want crit", got)
	}
	if got := severityOf(items, "vm:3000"); got != model.SeverityWarn {
		t.Errorf("omp-cloud stopped = %q, want warn", got)
	}
	if severityOf(items, "vm:9000") != "" || severityOf(items, "vm:2021") != "" {
		t.Errorf("template and running VMs must not raise attention: %+v", items)
	}
	if got := items[0].Route; got != "/vms/2020" {
		t.Errorf("crit item must sort first with route /vms/2020, got %s", got)
	}
}

func TestFluxRules(t *testing.T) {
	in := input()
	in.Flux = model.FluxData{
		Kustomizations: []model.FluxObj{
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "bad", Ready: false, Message: "boom\nsecond line"},
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "paused", Ready: false, Suspended: true, SuspendReason: "x"},
			{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: "good", Ready: true},
		},
		Sources: []model.FluxObj{
			{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: new(true)},
		},
	}
	items := Attention(in, nil)
	if got := severityOf(items, "flux:Kustomization/flux-system/bad"); got != model.SeverityWarn {
		t.Errorf("not-ready kustomization = %q, want warn", got)
	}
	if len(items) != 1 {
		t.Fatalf("suspended, ready and verified objects must not be flagged, got %+v", items)
	}
	if items[0].Detail != "boom" {
		t.Errorf("detail must be the first message line, got %q", items[0].Detail)
	}

	tests := []struct {
		name     string
		verified *bool
		want     model.Severity
	}{
		{"verified", new(true), ""},
		{"not verified", new(false), model.SeverityCrit},
		{"condition absent", nil, model.SeverityCrit},
	}
	for _, tc := range tests {
		in := input()
		in.Flux.Sources = []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Verified: tc.verified}}
		if got := severityOf(Attention(in, nil), "flux:source-unverified"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// Only the flux-system source is fenced by the crit rule.
	in = input()
	in.Flux.Sources = []model.FluxObj{{Kind: model.FluxKindGitRepository, Namespace: "x", Name: "other", Ready: true, Verified: new(false)}}
	if items := Attention(in, nil); len(items) != 0 {
		t.Errorf("other sources are not fenced: %+v", items)
	}
}

func TestAlertRules(t *testing.T) {
	tests := []struct {
		name  string
		alert model.Alert
		want  model.Severity
	}{
		{"warning", model.Alert{Fingerprint: "a", Name: "X", Severity: "warning", State: "active"}, model.SeverityWarn},
		{"critical", model.Alert{Fingerprint: "a", Name: "X", Severity: "critical", State: "active"}, model.SeverityCrit},
		{"watchdog", model.Alert{Fingerprint: "a", Name: "Watchdog", Severity: "critical", State: "active"}, ""},
		{"info inhibitor", model.Alert{Fingerprint: "a", Name: "InfoInhibitor", Severity: "warning", State: "active"}, ""},
		{"silenced", model.Alert{Fingerprint: "a", Name: "X", Severity: "critical", State: "suppressed", SilencedBy: []string{"s1"}}, ""},
		{"inhibited", model.Alert{Fingerprint: "a", Name: "X", Severity: "critical", State: "suppressed", InhibitedBy: []string{"i1"}}, ""},
		{"silenced but listed active", model.Alert{Fingerprint: "a", Name: "X", Severity: "critical", State: "active", SilencedBy: []string{"s1"}}, ""},
		{"info severity", model.Alert{Fingerprint: "a", Name: "X", Severity: "info", State: "active"}, ""},
		{"none severity", model.Alert{Fingerprint: "a", Name: "X", Severity: "none", State: "active"}, ""},
	}
	for _, tc := range tests {
		in := input()
		in.Alerts = []model.Alert{tc.alert}
		if got := severityOf(Attention(in, nil), "alert:a"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPodAndWorkloadHealth(t *testing.T) {
	created := testNow.Add(-10 * time.Minute)
	podTests := []struct {
		name    string
		phase   string
		waiting []string
		created time.Time
		want    model.Health
	}{
		{"running", "Running", nil, created, model.HealthOK},
		{"crashloop", "Running", []string{"CrashLoopBackOff"}, created, model.HealthCrit},
		{"imagepull", "Pending", []string{"ImagePullBackOff"}, testNow, model.HealthCrit},
		{"errimagepull", "Pending", []string{"ErrImagePull"}, testNow, model.HealthCrit},
		{"config error", "Pending", []string{"CreateContainerConfigError"}, testNow, model.HealthCrit},
		{"create error", "Pending", []string{"CreateContainerError"}, testNow, model.HealthCrit},
		{"container creating is fine", "Pending", []string{"ContainerCreating"}, testNow.Add(-time.Minute), model.HealthOK},
		{"pending 5m exactly", "Pending", nil, testNow.Add(-5 * time.Minute), model.HealthOK},
		{"pending over 5m", "Pending", nil, testNow.Add(-5*time.Minute - time.Second), model.HealthWarn},
		{"completed job", "Succeeded", nil, created, model.HealthOK},
	}
	for _, tc := range podTests {
		if got := PodHealth(tc.phase, tc.waiting, tc.created, testNow); got != tc.want {
			t.Errorf("pod %s: %s, want %s", tc.name, got, tc.want)
		}
	}
	wlTests := []struct {
		ready, desired int
		want           model.Health
	}{
		{1, 1, model.HealthOK}, {0, 0, model.HealthOK}, {3, 3, model.HealthOK},
		{1, 3, model.HealthWarn}, {0, 1, model.HealthCrit}, {0, 3, model.HealthCrit},
	}
	for _, tc := range wlTests {
		if got := WorkloadHealth(tc.ready, tc.desired); got != tc.want {
			t.Errorf("workload %d/%d: %s, want %s", tc.ready, tc.desired, got, tc.want)
		}
	}

	in := input()
	in.Cluster.Pods = []model.Pod{{Namespace: "a", Name: "p", Health: model.HealthCrit, Reason: "CrashLoopBackOff", CreatedAt: created}}
	in.Cluster.Workloads = []model.Workload{{Kind: "Deployment", Namespace: "a", Name: "w", Ready: 0, Desired: 1, Health: model.HealthCrit}}
	in.Cluster.Nodes = []model.Node{{Name: "talos-cp-1", Ready: false}, {Name: "talos-cp-2", Ready: true}}
	items := Attention(in, nil)
	if severityOf(items, "pod:a/p") != model.SeverityCrit || severityOf(items, "workload:Deployment/a/w") != model.SeverityCrit ||
		severityOf(items, "node:talos-cp-1") != model.SeverityCrit || severityOf(items, "node:talos-cp-2") != "" {
		t.Errorf("unexpected cluster items: %+v", items)
	}
	for _, it := range items {
		if it.ID == "pod:a/p" && it.Route != "/cluster/pods/a/p" {
			t.Errorf("pod route = %s", it.Route)
		}
	}
}

func TestEndpointHealth(t *testing.T) {
	tests := []struct {
		group   string
		healthy bool
		want    model.Health
	}{
		{"infrastructure", false, model.HealthCrit}, {"Infrastructure", false, model.HealthCrit},
		{"apps", false, model.HealthWarn}, {"infrastructure", true, model.HealthOK}, {"apps", true, model.HealthOK},
	}
	for _, tc := range tests {
		if got := EndpointHealth(tc.group, tc.healthy); got != tc.want {
			t.Errorf("%s healthy=%v: %s, want %s", tc.group, tc.healthy, got, tc.want)
		}
	}
	in := input()
	in.Status.Endpoints = []model.Endpoint{
		{Group: "infrastructure", Name: "UniFi", Key: "infrastructure_unifi", Healthy: false, Results: []model.CheckResult{
			{Success: true, Timestamp: testNow.Add(-3 * time.Minute)},
			{Success: false, Timestamp: testNow.Add(-2 * time.Minute)},
			{Success: false, Timestamp: testNow.Add(-time.Minute)},
		}},
		{Group: "apps", Name: "Grafana", Key: "apps_grafana", Healthy: true, Results: []model.CheckResult{{Success: true}}},
	}
	items := Attention(in, nil)
	if len(items) != 1 || items[0].Severity != model.SeverityCrit || !items[0].Since.Equal(testNow.Add(-2*time.Minute)) {
		t.Errorf("want one crit item since the first failure of the streak, got %+v", items)
	}
}

func TestHostRules(t *testing.T) {
	tests := []struct {
		name string
		host model.HostData
		id   string
		want model.Severity
	}{
		{"pool online", model.HostData{Pools: []model.Pool{{Name: "bulk", State: "online"}}}, "zfs:pool:bulk", ""},
		{"pool degraded", model.HostData{Pools: []model.Pool{{Name: "bulk", State: "degraded"}}}, "zfs:pool:bulk", model.SeverityCrit},
		{"scrub clean", model.HostData{Pools: []model.Pool{{Name: "bulk", State: "online"}}}, "zfs:scrub:bulk", ""},
		{"scrub errors", model.HostData{Pools: []model.Pool{{Name: "bulk", State: "online", ScrubErrors: 2}}}, "zfs:scrub:bulk", model.SeverityWarn},
		{"smart ok", model.HostData{Disks: []model.Disk{{Bay: "3", Healthy: true}}}, "smart:3", ""},
		{"smart failing", model.HostData{Disks: []model.Disk{{Bay: "3", Healthy: false}}}, "smart:3", model.SeverityCrit},
		{"temp 84.9", model.HostData{PackageTempsC: []float64{70, 84.9}}, "host:cpu-temp", ""},
		{"temp 85", model.HostData{PackageTempsC: []float64{70, 85}}, "host:cpu-temp", model.SeverityWarn},
		{"temp 89.9", model.HostData{PackageTempsC: []float64{89.9}}, "host:cpu-temp", model.SeverityWarn},
		{"temp 90", model.HostData{PackageTempsC: []float64{90, 60}}, "host:cpu-temp", model.SeverityCrit},
	}
	for _, tc := range tests {
		in := input()
		in.Host = tc.host
		if got := severityOf(Attention(in, nil), tc.id); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOverallStatusAndOrdering(t *testing.T) {
	if got := OverallStatus(nil); got != model.HealthOK {
		t.Errorf("empty = %s", got)
	}
	in := input()
	in.Alerts = []model.Alert{
		{Fingerprint: "old-warn", Name: "A", Severity: "warning", State: "active", StartsAt: testNow.Add(-2 * time.Hour)},
		{Fingerprint: "new-warn", Name: "B", Severity: "warning", State: "active", StartsAt: testNow.Add(-time.Minute)},
	}
	items := Attention(in, nil)
	if OverallStatus(items) != model.HealthWarn {
		t.Errorf("only warnings must give warn, got %s", OverallStatus(items))
	}
	if items[0].ID != "alert:new-warn" {
		t.Errorf("newest first within a severity, got %s", items[0].ID)
	}
	in.Alerts = append(in.Alerts, model.Alert{Fingerprint: "crit", Name: "C", Severity: "critical", State: "active", StartsAt: testNow.Add(-24 * time.Hour)})
	items = Attention(in, nil)
	if OverallStatus(items) != model.HealthCrit {
		t.Errorf("a critical item must make the status crit, got %s", OverallStatus(items))
	}
	if items[0].ID != "alert:crit" {
		t.Errorf("crit sorts first even when older, got %s", items[0].ID)
	}
	if items[0].Links == nil {
		t.Error("links must never be nil")
	}
}

func TestTrackerKeepsFirstSeen(t *testing.T) {
	tr := NewTracker()
	in := input()
	in.VMs.VMs = []model.VM{{VMID: 3000, Name: "omp-cloud", Status: "stopped"}}
	first := Attention(in, tr)[0].Since
	if !first.Equal(testNow) {
		t.Fatalf("first sighting = %v, want now", first)
	}
	in.Now = testNow.Add(10 * time.Minute)
	if got := Attention(in, tr)[0].Since; !got.Equal(first) {
		t.Errorf("since moved to %v, must stay %v", got, first)
	}
	in.VMs.VMs[0].Status = "running"
	Attention(in, tr)
	in.VMs.VMs[0].Status = "stopped"
	in.Now = testNow.Add(20 * time.Minute)
	if got := Attention(in, tr)[0].Since; !got.Equal(in.Now) {
		t.Errorf("a recovered then failing item starts over, got %v", got)
	}
}

func TestClassifyNamespace(t *testing.T) {
	conv := config.Load(func(string) string { return "" }).Conventions
	tests := []struct {
		name   string
		labels map[string]string
		want   nsInfo
	}{
		{"platform", map[string]string{"kustomize.toolkit.fluxcd.io/name": "monitoring", "homelab.io/protected": "true", "pod-security.kubernetes.io/enforce": "privileged"},
			nsInfo{group: model.GroupPlatform, protected: true, privileged: true}},
		{"app", map[string]string{"kustomize.toolkit.fluxcd.io/name": "app-homepage"}, nsInfo{group: model.GroupApps}},
		{"tenant wins over app", map[string]string{"homelab.io/tenant": "yahwai", "kustomize.toolkit.fluxcd.io/name": "app-x"},
			nsInfo{group: model.GroupTenants, tenant: "yahwai"}},
		{"unlabelled is platform", nil, nsInfo{group: model.GroupPlatform}},
	}
	for _, tc := range tests {
		if got := classifyNamespace(tc.labels, conv); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestIconURL(t *testing.T) {
	tests := map[string]string{
		"grafana.png":           "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/grafana.png",
		"zot.svg":               "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/svg/zot.svg",
		"mdi-monitor-dashboard": "",
		"../x.png":              "",
		"a.gif":                 "",
		"":                      "",
	}
	for in, want := range tests {
		if got := iconURL(in); got != want {
			t.Errorf("iconURL(%q) = %q, want %q", in, got, want)
		}
	}
}
