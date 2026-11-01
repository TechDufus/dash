package topics

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// source is a plugin's contribution to the overview, changeable between rounds.
type source struct {
	mu     sync.Mutex
	items  []model.AttentionItem
	kpis   []model.KPI
	topo   *model.Topology
	panics bool
}

func (s *source) set(f func(*source)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *source) Attention(time.Time) []model.AttentionItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panics {
		panic("boom")
	}
	return s.items
}

func (s *source) KPIs() []model.KPI {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kpis
}

func (s *source) Topology() model.Topology {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.topo == nil {
		return model.Topology{}
	}
	return *s.topo
}

func contributing(id plugin.ID, s *source) func() []plugin.Contributor {
	return func() []plugin.Contributor {
		return []plugin.Contributor{{ID: id, Attention: s, KPIs: s, Topology: s}}
	}
}

func overviewOf(t *testing.T, h *hub.Hub) model.OverviewData {
	t.Helper()
	env, ok := latest(h, model.TopicOverview)
	if !ok {
		t.Fatal("the overview is not published")
	}
	var ov model.OverviewData
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}

func ids(items []model.AttentionItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return strings.Join(out, " ")
}

// A plugin that contributes is enough: the overview comes out on the first round, with no topic
// built and no poller done.
func TestOverviewPublishesWithOneContributor(t *testing.T) {
	h := hub.New()
	src := &source{items: []model.AttentionItem{{ID: "fake:1", Severity: model.SeverityWarn, Source: "Fake", Title: "something"}}}
	m := testManagerWith(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...), contributing("fake", src))
	m.rebuild()

	ov := overviewOf(t, h)
	if len(ov.Attention) != 1 || ov.Attention[0].ID != "fake:1" || ov.Attention[0].Plugin != "fake" || ov.Status != model.HealthWarn {
		t.Fatalf("overview = %+v", ov)
	}
	if ov.Attention[0].Links == nil {
		t.Error("an item's links are never null")
	}
	for _, topic := range legacyTopics {
		if _, ok := h.Latest("", topic); ok {
			t.Errorf("%s is published although nothing built it", topic)
		}
	}
}

// With nothing to say and nothing to rebuild, a round publishes nothing.
func TestOverviewNeedsADirtyTopicOrAContributor(t *testing.T) {
	h := hub.New()
	m := testManager(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...))
	m.rebuild()
	if _, ok := h.Latest("", model.TopicOverview); ok {
		t.Fatal("an overview from nothing")
	}
	m.mark(model.TopicHost)
	m.done["host"] = true
	m.rebuild()
	if _, ok := h.Latest("", model.TopicOverview); !ok {
		t.Fatal("no overview after a topic changed")
	}
}

// The overview comes with the first topic that is ready; the others fill in later (it used to
// wait for all eight).
func TestOverviewDoesNotWaitForAllTopics(t *testing.T) {
	h := hub.New()
	m := testManager(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...))
	m.done["host"] = true
	m.markAll()
	m.rebuild()

	if _, ok := h.Latest("", model.TopicHost); !ok {
		t.Fatal("the host topic is not published")
	}
	for _, topic := range []model.Topic{model.TopicVMs, model.TopicCluster, model.TopicStorage} {
		if _, ok := h.Latest("", topic); ok {
			t.Fatalf("%s is published before its prerequisites", topic)
		}
	}
	ov := overviewOf(t, h)
	if ov.Topology.Host.Name != "" || len(ov.Topology.Groups) != 3 {
		t.Errorf("the early overview should be the zero-valued one: %+v", ov.Topology)
	}
}

// The overview is published as core.overview through the core host, and under no other name.
func TestCoreOverviewPublishesThroughCoreHost(t *testing.T) {
	rec := &plugintest.Hub{}
	m := testManager(config.Defaults(), "", plugintest.AllRunning(rec, LegacyManifests()...))
	m.mark(model.TopicHost)
	m.done["host"] = true
	m.rebuild()

	var names []string
	for _, ev := range rec.Published() {
		names = append(names, string(ev.Topic))
	}
	got := strings.Join(names, " ")
	if !strings.Contains(got, "core.overview") || strings.Contains(" "+got+" ", " overview ") {
		t.Fatalf("published %q: want core.overview and no overview", got)
	}
	if model.TopicOverview != "core.overview" {
		t.Fatalf("TopicOverview = %q", model.TopicOverview)
	}
}

// A change in a plugin's contribution reaches the overview on the next round, with no topic
// changing.
func TestOverviewFollowsAContributorChange(t *testing.T) {
	h := hub.New()
	src := &source{}
	m := testManagerWith(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...), contributing("fake", src))
	m.rebuild()
	if ov := overviewOf(t, h); len(ov.Attention) != 0 || ov.Status != model.HealthOK {
		t.Fatalf("before: %+v", ov)
	}

	src.set(func(s *source) {
		s.items = []model.AttentionItem{{ID: "x", Severity: model.SeverityCrit, Title: "down"}}
		s.kpis = []model.KPI{{Key: "up", Label: "Up", Value: 3, Health: model.HealthOK}}
	})
	m.rebuild()
	ov := overviewOf(t, h)
	if ids(ov.Attention) != "x" || ov.Status != model.HealthCrit || len(ov.KPIList) != 1 || ov.KPIList[0].Plugin != "fake" {
		t.Fatalf("after the change: %+v", ov)
	}

	src.set(func(s *source) { s.items, s.kpis = nil, nil })
	m.rebuild()
	if ov := overviewOf(t, h); len(ov.Attention) != 0 || ov.Status != model.HealthOK || len(ov.KPIList) != 0 {
		t.Fatalf("after it cleared: %+v", ov)
	}
}

// The ticker, not just a direct call, publishes it: one rebuild round after Run.
func TestOverviewIsPublishedByTheRebuildLoop(t *testing.T) {
	h := hub.New()
	src := &source{items: []model.AttentionItem{{ID: "x", Severity: model.SeverityWarn}}}
	m := testManagerWith(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...), contributing("fake", src))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)
	eventually(t, "the overview with the contributor's item", func() bool {
		env, ok := latest(h, model.TopicOverview)
		return ok && strings.Contains(string(env.Data), `"plugin":"fake"`)
	})
}

// A plugin whose source panics is skipped, logged once, and the others still count.
func TestPanickingContributorDoesNotStopTheOverview(t *testing.T) {
	h := hub.New()
	bad := &source{panics: true}
	good := &source{items: []model.AttentionItem{{ID: "ok", Severity: model.SeverityWarn}}}
	m := testManagerWith(config.Defaults(), "", plugintest.AllRunning(h, LegacyManifests()...), func() []plugin.Contributor {
		return []plugin.Contributor{{ID: "bad", Attention: bad}, {ID: "good", Attention: good}}
	})
	m.rebuild()
	m.rebuild()
	if ov := overviewOf(t, h); ids(ov.Attention) != "ok" {
		t.Fatalf("overview = %+v", ov)
	}
	bad.set(func(s *source) { s.panics = false })
	m.rebuild()
	m.mu.RLock()
	_, still := m.badSources["bad"]
	m.mu.RUnlock()
	if still {
		t.Error("a recovered plugin is still marked bad")
	}
}

// Each legacy rule's items name the plugin that owns the source (design A.7).
func TestAttentionItemsCarryPlugin(t *testing.T) {
	m := goldenManager(t)
	items := m.buildOverview(goldenSnapshots(), nil, testNow).Attention
	want := map[string]string{
		"alert:f1": "alertmanager", "flux:source-unverified": "flux", "flux:Kustomization/flux-system/apps": "flux",
		"node:cp-2": "kubernetes", "pod:media/app-0": "kubernetes", "workload:Deployment/media/app": "kubernetes",
		"cert:media/tls": "kubernetes", "pvc:media/data": "kubernetes", "bucket:backups": "kubernetes",
		"vm:3000": "proxmox", "gatus:apps_grafana": "gatus",
		"zfs:pool:bulk": "hardware", "smart:4": "hardware", "host:cpu-temp": "hardware",
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.ID] = it.Plugin
		if it.Plugin == "" {
			t.Errorf("%s carries no plugin", it.ID)
		}
	}
	for id, plug := range want {
		if got[id] != plug {
			t.Errorf("%s: plugin %q, want %q", id, got[id], plug)
		}
	}
}

// Compose orders what it is given as the table always did, whoever contributed it, and stamps
// a contributing plugin's id over whatever the item says.
func TestComposeOrdersAndStampsAttention(t *testing.T) {
	at := func(d time.Duration) time.Time { return testNow.Add(-d) }
	ov := compose([]contribution{
		{attention: []model.AttentionItem{
			{ID: "legacy-warn-new", Severity: model.SeverityWarn, Since: at(time.Minute), Plugin: "kubernetes"},
			{ID: "legacy-crit-old", Severity: model.SeverityCrit, Since: at(time.Hour), Plugin: "flux"},
		}},
		{plugin: "zed", attention: []model.AttentionItem{
			{ID: "b-warn-new", Severity: model.SeverityWarn, Since: at(time.Minute), Plugin: "liar"},
			{ID: "crit-new", Severity: model.SeverityCrit, Since: at(time.Second)},
		}},
	}, legacyOverview{kpis: model.Kpis{NodesTotal: 4}})

	if got, want := ids(ov.Attention), "crit-new legacy-crit-old b-warn-new legacy-warn-new"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}
	for _, it := range ov.Attention {
		if it.ID == "b-warn-new" && it.Plugin != "zed" || it.ID == "legacy-crit-old" && it.Plugin != "flux" {
			t.Errorf("%s: plugin %q", it.ID, it.Plugin)
		}
		if it.Links == nil {
			t.Errorf("%s: nil links", it.ID)
		}
	}
	if ov.Status != model.HealthCrit || ov.Kpis.NodesTotal != 4 || ov.KPIList == nil {
		t.Errorf("status %s, kpis %+v, kpiList %v", ov.Status, ov.Kpis, ov.KPIList)
	}
}

func TestKPIListAndTopologyMerge(t *testing.T) {
	tp := func(host string, vms []model.TopologyVM, groups ...model.TopologyGroup) *model.Topology {
		return &model.Topology{Host: model.TopologyHost{Name: host, Health: model.HealthOK}, VMs: vms, Groups: groups}
	}
	grp := func(name model.Group, h model.Health, pods map[string]int, ns ...string) model.TopologyGroup {
		g := model.TopologyGroup{Name: name, Health: h, PodsByNode: pods, Namespaces: []model.TopologyNamespace{}}
		for _, n := range ns {
			g.Namespaces = append(g.Namespaces, model.TopologyNamespace{Name: n, Health: model.HealthOK, Pods: 1})
		}
		return g
	}
	ov := compose([]contribution{
		{topology: tp("", nil, grp(model.GroupPlatform, model.HealthOK, map[string]int{"n1": 2}, "kube-system"))},
		{plugin: "pb", topology: tp("pve", []model.TopologyVM{{VMID: 1}}, grp(model.GroupPlatform, model.HealthWarn, map[string]int{"n1": 1, "n2": 4}, "apps", "alpha"),
			grp("Edge", model.HealthOK, nil, "gw")),
			kpis: []model.KPI{{Key: "b", Label: "B"}, {Key: "a", Label: "A"}}},
		{plugin: "pa", topology: tp("other", []model.TopologyVM{{VMID: 2}}),
			kpis: []model.KPI{{Key: "z", Label: "Z", Plugin: "ignored"}}},
	}, legacyOverview{})

	var keys []string
	for _, k := range ov.KPIList {
		keys = append(keys, k.Plugin+"."+k.Key)
	}
	if got := strings.Join(keys, " "); got != "pa.z pb.a pb.b" {
		t.Errorf("kpiList = %q, want ordered by plugin then key, stamped with the contributor", got)
	}
	if ov.Topology.Host.Name != "pve" {
		t.Errorf("host = %q, want the first one that names itself", ov.Topology.Host.Name)
	}
	if len(ov.Topology.VMs) != 2 || ov.Topology.VMs[0].VMID != 1 || ov.Topology.VMs[1].VMID != 2 {
		t.Errorf("vms = %+v, want appended in contribution order", ov.Topology.VMs)
	}
	if len(ov.Topology.Groups) != 2 || ov.Topology.Groups[0].Name != model.GroupPlatform || ov.Topology.Groups[1].Name != "Edge" {
		t.Fatalf("groups = %+v, want Platform then Edge", ov.Topology.Groups)
	}
	p := ov.Topology.Groups[0]
	var nss []string
	for _, n := range p.Namespaces {
		nss = append(nss, n.Name)
	}
	if strings.Join(nss, " ") != "alpha apps kube-system" || p.Health != model.HealthWarn || p.PodsByNode["n1"] != 3 || p.PodsByNode["n2"] != 4 {
		t.Errorf("merged Platform = %+v (namespaces %v)", p, nss)
	}
	if e := ov.Topology.Groups[1]; e.PodsByNode == nil || e.Namespaces == nil {
		t.Errorf("Edge must carry empty collections, not null: %+v", e)
	}

	// With nothing at all the collections are still empty, never null.
	empty := compose(nil, legacyOverview{})
	b, _ := json.Marshal(empty)
	for _, null := range []string{`"attention":null`, `"kpiList":null`, `"vms":null`, `"groups":null`} {
		if strings.Contains(string(b), null) {
			t.Errorf("empty overview has %s: %s", null, b)
		}
	}
}

// The host falls back to the first topology when none names one, so a legacy overview with no
// host name keeps the host it computed.
func TestTopologyKeepsTheLegacyHostWhenNoOneNamesIt(t *testing.T) {
	legacyHost := model.TopologyHost{Health: model.HealthWarn, PowerWatts: 12}
	ov := compose([]contribution{{topology: &model.Topology{Host: legacyHost}}, {plugin: "p", topology: &model.Topology{}}}, legacyOverview{})
	if ov.Topology.Host != legacyHost {
		t.Errorf("host = %+v, want %+v", ov.Topology.Host, legacyHost)
	}
}
