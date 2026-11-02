package demo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// published is a hub the demo has published to, its plugins all running.
func published(t *testing.T) *hub.Hub {
	t.Helper()
	h := hub.New()
	s := New(config.Defaults())
	s.Attach(plugintest.AllRunning(h))
	s.publish()
	return h
}

// The demo publishes the overview and nothing else: every other topic is its plugin's twin's.
func TestPublishesTheOverview(t *testing.T) {
	h := published(t)
	ev, ok := h.Latest("", model.TopicOverview)
	if !ok {
		t.Fatalf("topic %s not published", model.TopicOverview)
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil || len(env.Data) < 3 {
		t.Fatalf("topic %s: bad envelope (%v)", model.TopicOverview, err)
	}
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	if len(snap) != 1 {
		t.Errorf("the demo published %d topics, want only the overview", len(snap))
	}
}

func TestFixturesStayFake(t *testing.T) {
	h := published(t)
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	for _, ev := range snap {
		body := strings.ToLower(string(ev.Data))
		for _, bad := range []string{"techdufus", "home.", "10.0.", "192.168."} {
			if strings.Contains(body, bad) {
				t.Errorf("topic %s contains %q", ev.Topic, bad)
			}
		}
	}
}

func TestWrapSignsInAndPassesEveryRequestOn(t *testing.T) {
	var user string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user = r.Header.Get("X-Forwarded-User")
		w.WriteHeader(http.StatusTeapot)
	})
	// Nothing answers for the retired routes here: the request goes on to the API, which refuses it.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/actions/reconcile"}, {http.MethodPost, "/api/silences"},
		{http.MethodDelete, "/api/silences/abc"}, {http.MethodPost, "/api/actions/restart"},
	} {
		rec := httptest.NewRecorder()
		Wrap(next).ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s: got %d, want the request to reach the API", c.method, c.path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	Wrap(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	if rec.Code != http.StatusTeapot || user != "demo" {
		t.Fatalf("passthrough: got %d user %q", rec.Code, user)
	}
}

// demoSupervisor is a real supervisor over a stand-in for the flux plugin (the real plugins' twins
// are registered by cmd/dash, whose tests run them) and a real hub, as main wires them: the stand-in
// switches off and on with plugins.flux.enabled like a plugin does.
func demoSupervisor(t *testing.T, set *plugintest.Settings) (*Source, *plugin.Supervisor, *hub.Hub) {
	t.Helper()
	if set == nil {
		set = &plugintest.Settings{}
	}
	h := hub.New()
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond})
	s := New(config.Defaults())
	s.Attach(sup)
	fluxStandIn := plugintest.New("flux", plugintest.Config(func() any { return &pluginconf.Flux{} }))
	if err := sup.Add(fluxStandIn.Factory()); err != nil {
		t.Fatal(err)
	}
	h.SetOrder(sup.TopicOrder)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	return s, sup, h
}

func TestDemoPublishesTheOverviewAndCorePlugins(t *testing.T) {
	s, _, h := demoSupervisor(t, nil)
	s.publish()
	snap, _, cancel := h.Subscribe("", nil)
	defer cancel()
	var got []string
	for _, ev := range snap {
		got = append(got, ev.Topic)
	}
	// core.plugins first, then the overview: the plugins' own topics are their twins'.
	want := []string{"core.plugins", "core.overview"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("topics = %v, want %v", got, want)
	}
	var env model.Envelope
	if err := json.Unmarshal(snap[0].Data, &env); err != nil {
		t.Fatal(err)
	}
	var pd model.PluginsData
	if err := json.Unmarshal(env.Data, &pd); err != nil {
		t.Fatal(err)
	}
	if len(pd.Plugins) != 1 { // the flux stand-in; the real plugins' twins are registered by cmd/dash
		t.Fatalf("core.plugins lists %d plugins, want 1", len(pd.Plugins))
	}
}

func TestDemoPluginsAreHealthy(t *testing.T) {
	_, sup, _ := demoSupervisor(t, nil)
	infos := sup.Infos()
	if len(infos) != 1 {
		t.Fatalf("%d plugins, want 1", len(infos))
	}
	for _, in := range infos {
		if in.State != model.PluginHealthy || in.Message != "" {
			t.Errorf("%s: %s %q, want healthy with no message", in.ID, in.State, in.Message)
		}
	}
	if !sup.Ready() {
		t.Error("the demo is not ready")
	}
}

// The overview's alerts tile and source chip are the alertmanager plugin's: the demo adds them
// while its twin runs and leaves both out when the plugin does not run.
func TestDemoOverviewHasTheAlertmanagerKPIOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) (model.OverviewData, []model.SourceStatus) {
		s := New(config.Defaults())
		s.Attach(hp)
		s.publish()
		ev, ok := h.Latest("", model.TopicOverview)
		if !ok {
			t.Fatal("no overview")
		}
		var env model.Envelope
		var d model.OverviewData
		if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
			t.Fatalf("bad overview envelope (%v)", err)
		}
		return d, env.Sources
	}
	hasChip := func(srcs []model.SourceStatus) bool {
		for _, s := range srcs {
			if s.Name == "alertmanager" {
				return true
			}
		}
		return false
	}

	h := hub.New()
	d, srcs := overview(plugintest.Without(plugintest.AllRunning(h), "proxmox", "hardware", "kubernetes", "flux"), h)
	if len(d.KPIList) != 1 || d.KPIList[0].Plugin != "alertmanager" || d.KPIList[0].Key != "firing" || d.KPIList[0].Value != 2 || !hasChip(srcs) {
		t.Errorf("with alertmanager running: kpiList %+v, sources %+v", d.KPIList, srcs)
	}
	h = hub.New()
	d, srcs = overview(plugintest.Without(plugintest.AllRunning(h), "alertmanager", "proxmox", "hardware", "kubernetes", "flux"), h)
	if len(d.KPIList) != 0 || hasChip(srcs) {
		t.Errorf("without alertmanager: kpiList %+v, sources %+v", d.KPIList, srcs)
	}
}

// The overview's VM tiles, its VMs in the topology and the proxmox source chip are the proxmox
// plugin's: the demo adds them while its twin runs and leaves them out when the plugin does not run.
func TestDemoOverviewHasTheProxmoxKPIsOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) (model.OverviewData, []model.SourceStatus) {
		s := New(config.Defaults())
		s.Attach(hp)
		s.publish()
		ev, ok := h.Latest("", model.TopicOverview)
		if !ok {
			t.Fatal("no overview")
		}
		var env model.Envelope
		var d model.OverviewData
		if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
			t.Fatalf("bad overview envelope (%v)", err)
		}
		return d, env.Sources
	}
	hasChip := func(srcs []model.SourceStatus) bool {
		for _, s := range srcs {
			if s.Name == "proxmox" {
				return true
			}
		}
		return false
	}
	proxmoxKPIs := func(d model.OverviewData) (n int) {
		for _, k := range d.KPIList {
			if k.Plugin == "proxmox" {
				n++
			}
		}
		return n
	}

	h := hub.New()
	d, srcs := overview(plugintest.AllRunning(h), h)
	if proxmoxKPIs(d) != 2 || len(d.Topology.VMs) == 0 || !hasChip(srcs) {
		t.Errorf("with proxmox running: kpiList %+v, topology VMs %d, sources %+v", d.KPIList, len(d.Topology.VMs), srcs)
	}
	h = hub.New()
	d, srcs = overview(plugintest.Without(plugintest.AllRunning(h), "proxmox"), h)
	if proxmoxKPIs(d) != 0 || len(d.Topology.VMs) != 0 || d.Topology.VMs == nil || hasChip(srcs) {
		t.Errorf("without proxmox: kpiList %+v, topology VMs %v, sources %+v", d.KPIList, d.Topology.VMs, srcs)
	}
}

// The overview's power and CPU temperature tiles, the host in its topology are the hardware
// plugin's: the demo adds them while its twin runs and leaves them out when the plugin does not
// run. The tiles are the ones the plugin's KPIs() raises (internal/plugins/hardware).
func TestDemoOverviewHasTheHardwareKPIsOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) model.OverviewData {
		s := New(config.Defaults())
		s.Attach(hp)
		s.publish()
		ev, ok := h.Latest("", model.TopicOverview)
		if !ok {
			t.Fatal("no overview")
		}
		var env model.Envelope
		var d model.OverviewData
		if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
			t.Fatalf("bad overview envelope (%v)", err)
		}
		return d
	}
	kpis := func(d model.OverviewData) map[string]model.KPI {
		out := map[string]model.KPI{}
		for _, k := range d.KPIList {
			if k.Plugin == "hardware" {
				out[k.Key] = k
			}
		}
		return out
	}

	h := hub.New()
	d := overview(plugintest.AllRunning(h), h)
	got := kpis(d)
	if len(got) != 2 || got["power"].Unit != "W" || got["power"].Value <= 0 || got["cpuTemp"].Unit != "°C" || got["cpuTemp"].Value <= 0 {
		t.Errorf("with hardware running: kpiList %+v", d.KPIList)
	}
	if host := d.Topology.Host; host.Name != world.HostName || host.PowerWatts != got["power"].Value || host.CPUTempC != got["cpuTemp"].Value ||
		host.CPUTempHealth != model.HealthOK || host.Health != model.HealthOK {
		t.Errorf("with hardware running: topology host %+v", host)
	}
	h = hub.New()
	d = overview(plugintest.Without(plugintest.AllRunning(h), "hardware"), h)
	if len(kpis(d)) != 0 || d.Topology.Host != (model.TopologyHost{}) {
		t.Errorf("without hardware: kpiList %+v, topology host %+v", d.KPIList, d.Topology.Host)
	}
}

// The overview's node and PVC tiles, the cluster groups in its topology, the kubernetes attention
// items and the kubernetes source chip are the kubernetes plugin's: the demo adds them while its
// twin runs, each KPI once, and leaves them out when the plugin does not run.
func TestDemoOverviewHasTheKubernetesKPIsOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) (model.OverviewData, []model.SourceStatus) {
		s := New(config.Defaults())
		s.Attach(hp)
		s.publish()
		ev, ok := h.Latest("", model.TopicOverview)
		if !ok {
			t.Fatal("no overview")
		}
		var env model.Envelope
		var d model.OverviewData
		if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
			t.Fatalf("bad overview envelope (%v)", err)
		}
		return d, env.Sources
	}
	kpis := func(d model.OverviewData) (keys []string) {
		for _, k := range d.KPIList {
			if k.Plugin == "kubernetes" {
				keys = append(keys, k.Key)
			}
		}
		return keys
	}
	hasChip := func(srcs []model.SourceStatus) bool {
		for _, s := range srcs {
			if s.Name == "kubernetes" {
				return true
			}
		}
		return false
	}
	kubeItems := func(d model.OverviewData) (n int) {
		for _, a := range d.Attention {
			if a.Plugin == "kubernetes" {
				n++
			}
		}
		return n
	}

	h := hub.New()
	d, srcs := overview(plugintest.AllRunning(h), h)
	if got := strings.Join(kpis(d), " "); got != "nodes.ready nodes.total pvc.worst" {
		t.Errorf("with kubernetes running: kubernetes KPIs %q, want each once", got)
	}
	for _, k := range d.KPIList {
		if k.Key == "nodes.ready" && (k.Value != 3 || k.Health != model.HealthOK) {
			t.Errorf("nodes.ready = %+v", k)
		}
		if k.Key == "pvc.worst" && (k.Label != "monitoring/vmsingle-data" || k.Health != model.HealthWarn) {
			t.Errorf("pvc.worst = %+v", k)
		}
	}
	if len(d.Topology.Groups) != 3 || kubeItems(d) == 0 || !hasChip(srcs) {
		t.Errorf("with kubernetes running: topology groups %d, attention %d, sources %+v", len(d.Topology.Groups), kubeItems(d), srcs)
	}
	h = hub.New()
	d, srcs = overview(plugintest.Without(plugintest.AllRunning(h), "kubernetes", "flux"), h)
	if len(kpis(d)) != 0 || len(d.Topology.Groups) != 0 || d.Topology.Groups == nil || kubeItems(d) != 0 || hasChip(srcs) {
		t.Errorf("without kubernetes: kpiList %+v, topology groups %v, attention %+v, sources %+v", d.KPIList, d.Topology.Groups, d.Attention, srcs)
	}
}

// latestOverview is the overview the demo last published to h, and its source chips.
func latestOverview(t *testing.T, h *hub.Hub) (model.OverviewData, []model.SourceStatus) {
	t.Helper()
	ev, ok := h.Latest("", model.TopicOverview)
	if !ok {
		t.Fatal("no overview")
	}
	var env model.Envelope
	var d model.OverviewData
	if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
		t.Fatalf("bad overview envelope (%v)", err)
	}
	return d, env.Sources
}

// fluxPart is what the flux plugin contributes to an overview: its KPIs by key and its items.
func fluxPart(d model.OverviewData) (kpis map[string]model.KPI, items []model.AttentionItem) {
	kpis = map[string]model.KPI{}
	for _, k := range d.KPIList {
		if k.Plugin == "flux" {
			kpis[k.Key] = k
		}
	}
	for _, a := range d.Attention {
		if a.Plugin == "flux" {
			items = append(items, a)
		}
	}
	return kpis, items
}

// The overview's four GitOps tiles, the suspended release's warning and the source chip are the
// flux plugin's (design 1.9: its twin's contribution): the demo adds them while the plugin runs,
// each KPI once, and leaves them out when it does not. The values are the fake world's.
func TestDemoOverviewHasTheFluxKPIsOnlyWhileItRuns(t *testing.T) {
	overview := func(hp plugin.HostProvider, h *hub.Hub) (model.OverviewData, []model.SourceStatus, *Source) {
		s := New(config.Defaults())
		s.Attach(hp)
		s.publish()
		d, srcs := latestOverview(t, h)
		return d, srcs, s
	}
	chips := func(srcs []model.SourceStatus, name string) (n int) {
		for _, s := range srcs {
			if s.Name == name {
				n++
			}
		}
		return n
	}

	h := hub.New()
	d, srcs, s := overview(plugintest.AllRunning(h), h)
	kpis, items := fluxPart(d)
	flux := s.world.Flux()
	total := float64(len(flux.Kustomizations) + len(flux.HelmReleases))
	if len(kpis) != 4 || kpis["ready"].Value != total || kpis["total"].Value != total || kpis["suspended"].Value != 1 ||
		kpis["verified"].Value != 1 || kpis["verified"].Health != model.HealthOK || kpis["ready"].Health != model.HealthOK {
		t.Errorf("with flux running: flux KPIs %+v, want ready and total %v, suspended 1, verified 1, each once", kpis, total)
	}
	if len(items) != 1 || items[0].ID != "flux:suspended/HelmRelease/acme/acme-preview" || items[0].Severity != model.SeverityWarn ||
		items[0].Ref != "flux:HelmRelease/acme/acme-preview" || items[0].Route != "/gitops" || items[0].Detail == "" {
		t.Errorf("with flux running: flux attention %+v", items)
	}
	if n := chips(srcs, "kubernetes"); n != 1 {
		t.Errorf("with flux and kubernetes running: %d kubernetes chips in %+v, want one", n, srcs)
	}

	// Without flux the tiles and the item go, and the chip stays the kubernetes plugin's.
	h = hub.New()
	d, srcs, _ = overview(plugintest.Without(plugintest.AllRunning(h), "flux"), h)
	if kpis, items = fluxPart(d); len(kpis) != 0 || len(items) != 0 {
		t.Errorf("without flux: flux KPIs %+v, attention %+v", kpis, items)
	}
	if n := chips(srcs, "kubernetes"); n != 1 {
		t.Errorf("without flux, with kubernetes: %d kubernetes chips, want one", n)
	}

	// With neither, no chip is left.
	h = hub.New()
	_, srcs, _ = overview(plugintest.Without(plugintest.AllRunning(h), "flux", "kubernetes"), h)
	if n := chips(srcs, "kubernetes"); n != 0 {
		t.Errorf("without flux and kubernetes: %d kubernetes chips, want none", n)
	}

	// Every KPI of the overview is there once, whatever the plugin.
	h = hub.New()
	d, _, _ = overview(plugintest.AllRunning(h), h)
	seen := map[string]bool{}
	for _, k := range d.KPIList {
		if id := k.Plugin + "/" + k.Key; seen[id] {
			t.Errorf("KPI %s is on the overview twice: %+v", id, d.KPIList)
		} else {
			seen[id] = true
		}
	}
}

// A plugin disabled in demo mode contributes nothing to the overview, which stays.
func TestDemoDisabledPluginContributesNothing(t *testing.T) {
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &pluginconf.Flux{}})
	s, _, h := demoSupervisor(t, set)
	s.publish()
	d, _ := latestOverview(t, h)
	if kpis, items := fluxPart(d); len(kpis) != 0 || len(items) != 0 {
		t.Errorf("flux is disabled: its KPIs %+v and attention %+v are on the overview", kpis, items)
	}
}

// A plugin follows a config reload: disabling it removes its tiles and lists it as disabled,
// enabling it again brings them back on the next tick.
func TestDemoPluginSwitchesOffAndOnWithoutRestart(t *testing.T) {
	set := &plugintest.Settings{}
	s, sup, h := demoSupervisor(t, set)
	s.publish()
	d, _ := latestOverview(t, h)
	if kpis, _ := fluxPart(d); len(kpis) != 4 {
		t.Fatalf("flux KPIs before it is disabled: %+v", kpis)
	}
	state := func() model.PluginState {
		for _, in := range sup.Infos() {
			if in.ID == "flux" {
				return in.State
			}
		}
		t.Fatal("flux is not listed")
		return ""
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	off := false
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &pluginconf.Flux{}})
	sup.Reload()
	waitFor("flux to be disabled", func() bool { return state() == model.PluginDisabled })
	s.publish()
	d, _ = latestOverview(t, h)
	if kpis, items := fluxPart(d); len(kpis) != 0 || len(items) != 0 {
		t.Errorf("flux is disabled: its KPIs %+v and attention %+v are on the overview", kpis, items)
	}

	on := true
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &on, Config: &pluginconf.Flux{}})
	sup.Reload()
	waitFor("flux to run again", func() bool { return state() == model.PluginHealthy })
	s.publish() // what the next tick does
	d, _ = latestOverview(t, h)
	if kpis, items := fluxPart(d); len(kpis) != 4 || len(items) != 1 {
		t.Errorf("flux is enabled again: KPIs %+v, attention %+v", kpis, items)
	}
}

// Demo asks no plugin's Host to publish (each ask of one that does not run would log a WARN on
// every tick): the overview goes through the core Host and nothing else is touched.
func TestDemoPublishesOnlyThroughTheCoreHost(t *testing.T) {
	h := hub.New()
	hp := plugintest.Without(plugintest.AllRunning(h), "flux")
	s := New(config.Defaults())
	s.Attach(hp)
	s.publish()
	if n := hp.Publishes("flux"); n != 0 {
		t.Errorf("demo called a stopped flux's Host %d times", n)
	}
	if hp.Publishes(plugin.Core) != 1 {
		t.Errorf("demo published through the core Host %d times, want once", hp.Publishes(plugin.Core))
	}
	if _, ok := h.Latest("", model.TopicOverview); !ok {
		t.Error("the overview is missing")
	}
}
