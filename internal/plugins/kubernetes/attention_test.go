package kubernetes

import (
	"fmt"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// atItem is the item with id, or false.
func atItem(items []model.AttentionItem, id string) (model.AttentionItem, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return model.AttentionItem{}, false
}

// atSeverity is the severity of the item with id, or "" when there is none.
func atSeverity(items []model.AttentionItem, id string) model.Severity {
	it, _ := atItem(items, id)
	return it.Severity
}

func TestAttentionNodes(t *testing.T) {
	links := []model.Link{{Label: "Headlamp", Href: "https://hl.example.com/node"}}
	items := attentionOf(model.ClusterData{Nodes: []model.Node{
		{Name: "cp-1", IP: "192.0.2.11", Ready: false, Links: links},
		{Name: "cp-2", IP: "192.0.2.12", Ready: true},
	}}, model.StorageData{}, clNow)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one for the node that is not ready", items)
	}
	want := model.AttentionItem{
		ID: "node:cp-1", Severity: model.SeverityCrit, Source: "kubernetes", Plugin: "kubernetes",
		Title: "Node cp-1 not ready", Detail: "192.0.2.11", Route: "/cluster", Links: links, Ref: "kubernetes:Node/cp-1",
	}
	if fmt.Sprintf("%+v", items[0]) != fmt.Sprintf("%+v", want) {
		t.Errorf("item = %+v\nwant   %+v", items[0], want)
	}
}

func TestAttentionPods(t *testing.T) {
	created := clNow.Add(-10 * time.Minute)
	items := attentionOf(model.ClusterData{Pods: []model.Pod{
		{Namespace: "a", Name: "crash", Health: model.HealthCrit, Reason: "CrashLoopBackOff", CreatedAt: created},
		{Namespace: "a", Name: "stuck", Health: model.HealthWarn, CreatedAt: created},
		{Namespace: "a", Name: "stuck-why", Health: model.HealthWarn, Reason: "Unschedulable", CreatedAt: created},
		{Namespace: "a", Name: "fine", Health: model.HealthOK, CreatedAt: created},
		{Namespace: "a", Name: "unknown", Health: model.HealthUnknown, CreatedAt: created},
	}}, model.StorageData{}, clNow)
	if len(items) != 3 {
		t.Fatalf("items = %+v, want the crashing and the two stuck pods", items)
	}
	crash, _ := atItem(items, "pod:a/crash")
	if crash.Severity != model.SeverityCrit || crash.Title != "Pod a/crash is failing" || crash.Detail != "CrashLoopBackOff" ||
		crash.Route != "/cluster/pods/a/crash" || !crash.Since.Equal(created) || crash.Ref != "kubernetes:Pod/a/crash" || crash.Source != "kubernetes" {
		t.Errorf("crash = %+v", crash)
	}
	stuck, _ := atItem(items, "pod:a/stuck")
	if stuck.Severity != model.SeverityWarn || stuck.Title != "Pod a/stuck is stuck pending" || stuck.Detail != "Pending for over 5 minutes" {
		t.Errorf("stuck = %+v", stuck)
	}
	if why, _ := atItem(items, "pod:a/stuck-why"); why.Detail != "Unschedulable" {
		t.Errorf("a pending pod with a reason keeps it: %+v", why)
	}
}

func TestAttentionWorkloads(t *testing.T) {
	items := attentionOf(model.ClusterData{Workloads: []model.Workload{
		{Kind: model.WorkloadKindDeployment, Namespace: "a", Name: "down", Ready: 0, Desired: 2, Health: model.HealthCrit},
		{Kind: model.WorkloadKindStatefulSet, Namespace: "a", Name: "partial", Ready: 1, Desired: 3, Health: model.HealthWarn},
		{Kind: model.WorkloadKindDeployment, Namespace: "a", Name: "fine", Ready: 1, Desired: 1, Health: model.HealthOK},
		{Kind: model.WorkloadKindJob, Namespace: "a", Name: "job", Ready: 0, Desired: 1, Health: model.HealthOK},
	}}, model.StorageData{}, clNow)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	down, _ := atItem(items, "workload:Deployment/a/down")
	if down.Severity != model.SeverityCrit || down.Title != "Deployment a/down not ready" || down.Detail != "0 of 2 ready" ||
		down.Route != "/cluster" || down.Ref != "kubernetes:Workload/Deployment/a/down" || !down.Since.IsZero() {
		t.Errorf("down = %+v", down)
	}
	if sev := atSeverity(items, "workload:StatefulSet/a/partial"); sev != model.SeverityWarn {
		t.Errorf("partial severity = %q", sev)
	}
}

func TestAttentionCertificateBoundaries(t *testing.T) {
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
			if got := health.CertHealth(clNow.Add(tc.left), clNow); got != tc.want {
				t.Fatalf("CertHealth = %s, want %s", got, tc.want)
			}
			items := attentionOf(model.ClusterData{Certificates: []model.Cert{
				{Namespace: "gateway", Name: "wild", NotAfter: new(clNow.Add(tc.left))},
			}}, model.StorageData{}, clNow)
			want := model.Severity(tc.want)
			if tc.want == model.HealthOK {
				want = ""
			}
			if got := atSeverity(items, "cert:gateway/wild"); got != want {
				t.Errorf("severity = %q, want %q", got, want)
			}
		})
	}
}

func TestAttentionCertificateItem(t *testing.T) {
	notAfter := time.Date(2026, 11, 4, 23, 30, 0, 0, time.FixedZone("X", 5*3600))
	items := attentionOf(model.ClusterData{Certificates: []model.Cert{
		{Namespace: "gateway", Name: "wild", NotAfter: &notAfter},
		{Namespace: "gateway", Name: "unknown-expiry"},
	}}, model.StorageData{}, clNow)
	if len(items) != 1 {
		t.Fatalf("items = %+v, want one: a certificate without an expiry raises nothing", items)
	}
	it := items[0]
	if it.Source != "cert-manager" || it.Plugin != "kubernetes" || it.Severity != model.SeverityCrit || it.Title != "Certificate gateway/wild expires soon" ||
		it.Detail != "expires 2026-11-04 (5 days)" || it.Route != "/cluster" || it.Ref != "kubernetes:Certificate/gateway/wild" || !it.Since.IsZero() {
		t.Errorf("item = %+v", it)
	}
}

func TestAttentionPVCsAndBuckets(t *testing.T) {
	ratio := func(r float64) *float64 { return &r }
	links := []model.Link{{Label: "Grafana", Href: "https://gf.example.com/pvc"}}
	items := attentionOf(model.ClusterData{}, model.StorageData{
		PVCs: []model.PVC{
			{Namespace: "a", Name: "ok", UsedRatio: ratio(0.79)},
			{Namespace: "a", Name: "warn", UsedRatio: ratio(0.80), Links: links},
			{Namespace: "a", Name: "crit", UsedRatio: ratio(0.90)},
			{Namespace: "a", Name: "unmeasured"},
		},
		Buckets: []model.Bucket{
			{Name: "noquota", UsedBytes: 999},
			{Name: "fine", UsedBytes: 50, QuotaBytes: 100},
			{Name: "full", UsedBytes: 95, QuotaBytes: 100},
		},
	}, clNow)
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	warn, _ := atItem(items, "pvc:a/warn")
	if warn.Severity != model.SeverityWarn || warn.Source != "storage" || warn.Title != "PVC a/warn is 80% full" || warn.Detail != "" ||
		warn.Route != "/storage" || warn.Ref != "kubernetes:PVC/a/warn" || len(warn.Links) != 1 || warn.Plugin != "kubernetes" {
		t.Errorf("warn = %+v", warn)
	}
	if sev := atSeverity(items, "pvc:a/crit"); sev != model.SeverityCrit {
		t.Errorf("crit severity = %q", sev)
	}
	full, _ := atItem(items, "bucket:full")
	if full.Severity != model.SeverityCrit || full.Title != "Bucket full is 95% of its quota" || full.Source != "storage" || full.Route != "/storage" ||
		full.Ref != "" || full.Links == nil || len(full.Links) != 0 {
		t.Errorf("bucket = %+v: a bucket carries no ref and an empty links list", full)
	}
}

func TestAttentionOfNothingIsAnEmptyList(t *testing.T) {
	items := attentionOf(model.ClusterData{}, model.StorageData{}, clNow)
	if items == nil || len(items) != 0 {
		t.Errorf("items = %#v, want an empty non-nil slice", items)
	}
}

func TestAttentionRuleOrderAndRefs(t *testing.T) {
	ratio := 0.95
	cl := model.ClusterData{
		Nodes:        []model.Node{{Ref: "kubernetes:Node/cp-1", Name: "cp-1"}},
		Pods:         []model.Pod{{Namespace: "a", Name: "p", Health: model.HealthCrit}},
		Workloads:    []model.Workload{{Kind: model.WorkloadKindDeployment, Namespace: "a", Name: "w", Health: model.HealthCrit}},
		Certificates: []model.Cert{{Namespace: "a", Name: "c", NotAfter: new(clNow.Add(-time.Hour))}},
	}
	st := model.StorageData{
		PVCs:    []model.PVC{{Namespace: "a", Name: "v", UsedRatio: &ratio}},
		Buckets: []model.Bucket{{Name: "b", UsedBytes: 99, QuotaBytes: 100}},
	}
	items := attentionOf(cl, st, clNow)
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
		if it.Plugin != string(ID) || it.Links == nil {
			t.Errorf("item %s = %+v, want the plugin id and a non-nil links list", it.ID, it)
		}
	}
	want := []string{"node:cp-1", "pod:a/p", "workload:Deployment/a/w", "cert:a/c", "pvc:a/v", "bucket:b"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	for _, it := range items {
		if it.ID == "bucket:b" {
			continue
		}
		p, _, _, ok := plugin.ParseRef(it.Ref)
		if !ok || p != ID {
			t.Errorf("item %s ref = %q, want one of the plugin", it.ID, it.Ref)
		}
	}
	plugintest.AssertRefs(t, nil, ID, items[:5]) // distinct, well-formed refs
}
