package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// overviewOf is the core.overview the hub holds now, and its source chips.
func overviewOf(t *testing.T, fw *framework) (model.OverviewData, []model.SourceStatus) {
	t.Helper()
	ev, ok := fw.hub.Latest("", model.TopicOverview)
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

// topicData decodes the payload of a topic the hub holds into out.
func topicData(t *testing.T, fw *framework, topic model.Topic, out any) {
	t.Helper()
	ev, ok := fw.hub.Latest("", topic)
	if !ok {
		t.Fatalf("%s is not published", topic)
	}
	var env model.Envelope
	if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, out) != nil {
		t.Fatalf("%s: bad envelope (%v)", topic, err)
	}
}

// kpisOf is the KPIs of one plugin on an overview, by key.
func kpisOf(d model.OverviewData, plugin string) map[string]model.KPI {
	out := map[string]model.KPI{}
	for _, k := range d.KPIList {
		if k.Plugin == plugin {
			out[k.Key] = k
		}
	}
	return out
}

func itemsOf(d model.OverviewData, plugin string) (out []model.AttentionItem) {
	for _, a := range d.Attention {
		if a.Plugin == plugin {
			out = append(out, a)
		}
	}
	return out
}

// The demo's overview is what the twins contribute, composed as in production: every plugin's
// tiles, items, topology and source chip are there, each KPI and each chip once, and the values
// are the fake world's (the pages behind the tiles show the same numbers).
func TestDemoOverviewIsComposedFromTheTwins(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	d, srcs := overviewOf(t, fw)

	seen := map[string]bool{}
	for _, k := range d.KPIList {
		if id := k.Plugin + "/" + k.Key; seen[id] {
			t.Errorf("KPI %s is on the overview twice: %+v", id, d.KPIList)
		} else {
			seen[id] = true
		}
	}
	for _, want := range []string{
		"alertmanager/firing", "flux/ready", "flux/suspended", "flux/total", "flux/verified", "hardware/cpuTemp", "hardware/power",
		"kubernetes/nodes.ready", "kubernetes/nodes.total", "kubernetes/pvc.worst", "proxmox/vms.running", "proxmox/vms.total",
	} {
		if !seen[want] {
			t.Errorf("KPI %s is missing from the overview: %+v", want, d.KPIList)
		}
	}
	if len(d.KPIList) != 12 {
		t.Errorf("%d KPIs, want the 12 the twins raise: %+v", len(d.KPIList), d.KPIList)
	}
	names := map[string]int{}
	for _, s := range srcs {
		names[s.Name]++
	}
	for name, n := range names {
		if n != 1 {
			t.Errorf("source chip %q appears %d times, want once: %+v", name, n, srcs)
		}
	}
	for _, want := range []string{"alertmanager", "kubernetes", "proxmox"} {
		if names[want] != 1 {
			t.Errorf("no %s chip on the overview: %+v", want, srcs)
		}
	}
	if d.Status != model.HealthCrit {
		t.Errorf("status = %s, want crit (the crash-looping pod)", d.Status)
	}

	// alertmanager: the firing alerts of the alerts topic.
	var alerts model.AlertsData
	topicData(t, fw, model.TopicAlerts, &alerts)
	firing := 0
	for _, a := range alerts.Alerts {
		if a.State == "active" {
			firing++
		}
	}
	if k := kpisOf(d, "alertmanager")["firing"]; k.Value != float64(firing) || firing == 0 {
		t.Errorf("alertmanager firing = %+v, want %d", k, firing)
	}

	// proxmox: the guests of the VMs topic, which are the topology's VMs.
	var vms model.VMsData
	topicData(t, fw, model.TopicVMs, &vms)
	running, total := 0, 0
	for _, vm := range vms.VMs {
		if vm.Template {
			continue
		}
		total++
		if vm.Status == "running" {
			running++
		}
	}
	pk := kpisOf(d, "proxmox")
	if pk["vms.running"].Value != float64(running) || pk["vms.total"].Value != float64(total) || total == 0 {
		t.Errorf("proxmox KPIs %+v, guests %d running of %d", pk, running, total)
	}
	if len(d.Topology.VMs) != total {
		t.Errorf("topology has %d VMs, want %d", len(d.Topology.VMs), total)
	}

	// hardware: the host's power and CPU temperature, which are also the topology's host.
	hk := kpisOf(d, "hardware")
	if hk["power"].Unit != "W" || hk["power"].Value <= 0 || hk["cpuTemp"].Unit != "°C" || hk["cpuTemp"].Value <= 0 {
		t.Errorf("hardware KPIs %+v", hk)
	}
	if h := d.Topology.Host; h.Name == "" || h.PowerWatts != hk["power"].Value || h.CPUTempC != hk["cpuTemp"].Value {
		t.Errorf("topology host %+v does not agree with the KPIs %+v", h, hk)
	}

	// kubernetes: the node and PVC tiles, the three groups, and items with refs.
	kk := kpisOf(d, "kubernetes")
	if kk["nodes.ready"].Value != 3 || kk["nodes.ready"].Health != model.HealthOK || kk["nodes.total"].Value != 3 {
		t.Errorf("node KPIs = %+v", kk)
	}
	if k := kk["pvc.worst"]; k.Label != "monitoring/vmsingle-data" || k.Health != model.HealthWarn {
		t.Errorf("pvc.worst = %+v", k)
	}
	if len(d.Topology.Groups) != 3 || len(itemsOf(d, "kubernetes")) == 0 {
		t.Errorf("topology groups %d, kubernetes items %d", len(d.Topology.Groups), len(itemsOf(d, "kubernetes")))
	}

	// flux: the four GitOps tiles count the objects of flux.objects, and the suspended release is
	// the one warning.
	var flux model.FluxData
	topicData(t, fw, model.TopicFlux, &flux)
	fk, objects := kpisOf(d, "flux"), float64(len(flux.Kustomizations)+len(flux.HelmReleases))
	if fk["ready"].Value != objects || fk["total"].Value != objects || fk["suspended"].Value != 1 ||
		fk["verified"].Value != 1 || fk["verified"].Health != model.HealthOK || fk["ready"].Health != model.HealthOK {
		t.Errorf("flux KPIs %+v, want ready and total %v, suspended 1, verified 1", fk, objects)
	}
	if items := itemsOf(d, "flux"); len(items) != 1 || items[0].ID != "flux:suspended/HelmRelease/acme/acme-preview" ||
		items[0].Severity != model.SeverityWarn || items[0].Ref != "flux:HelmRelease/acme/acme-preview" ||
		items[0].Route != "/gitops" || items[0].Detail == "" {
		t.Errorf("flux attention %+v", itemsOf(d, "flux"))
	}
}

// No object, KPI or attention item of the demo names a lab: every topic of a twin-driven demo hub
// stays fake, against the five literals the repository's hygiene test denies (internal/hygiene)
// and the real-world markers the fixtures must avoid.
func TestFixturesStayFake(t *testing.T) {
	fw, _ := startDemo(t)
	envs := awaitDemoTopics(t, fw)
	snap, _, cancel := fw.hub.Subscribe("", nil)
	cancel()
	if len(snap) != len(envs) || len(snap) != len(demoTopics) {
		t.Fatalf("%d events for %d topics", len(snap), len(demoTopics))
	}
	denied := []string{
		"mac-mini", "rpi-control", "2020 && vmid", "flux-system", "gateway/home",
		"techdufus", "home.", "10.0.", "192.168.",
	}
	for _, ev := range snap {
		body := strings.ToLower(string(ev.Data))
		for _, bad := range denied {
			if strings.Contains(body, bad) {
				t.Errorf("topic %s contains %q", ev.Topic, bad)
			}
		}
	}
}

// Every plugin of a demo run is its twin and healthy, and the demo is ready.
func TestDemoPluginsAreHealthy(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	infos := fw.sup.Infos()
	if len(infos) != len(registry(nil)) {
		t.Fatalf("%d plugin rows, want one per plugin dash ships (%d)", len(infos), len(registry(nil)))
	}
	for _, in := range infos {
		if in.State != model.PluginHealthy || in.Message != "" {
			t.Errorf("%s: %s %q, want healthy with no message", in.ID, in.State, in.Message)
		}
	}
	if !fw.sup.Ready() {
		t.Error("the demo is not ready")
	}
}

// A plugin disabled in demo mode contributes nothing to the overview, which stays; every other
// plugin's contribution is untouched.
func TestDemoDisabledPluginContributesNothing(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    enabled: false\n")
	fw, _ := startDemo(t, "DASH_CONFIG="+cfgPath)
	waitFor(t, func() bool {
		ev, ok := fw.hub.Latest("", model.TopicOverview)
		return ok && len(ev.Data) > 0
	})
	waitFor(t, func() bool {
		d, _ := overviewOf(t, fw)
		return len(kpisOf(d, "kubernetes")) == 3
	})
	d, srcs := overviewOf(t, fw)
	if k := kpisOf(d, "flux"); len(k) != 0 || len(itemsOf(d, "flux")) != 0 {
		t.Errorf("flux is disabled: its KPIs %+v and items %+v are on the overview", k, itemsOf(d, "flux"))
	}
	if n := slices.IndexFunc(srcs, func(s model.SourceStatus) bool { return s.Name == "kubernetes" }); n < 0 {
		t.Errorf("without flux the kubernetes chip is still the kubernetes plugin's: %+v", srcs)
	}
	if _, ok := fw.hub.Latest("", model.TopicFlux); ok {
		t.Error("flux.objects is still published by a disabled plugin")
	}
}

// A plugin follows a config reload: disabling it removes its tiles and lists it as disabled,
// enabling it again brings them back on the next round.
func TestDemoPluginSwitchesOffAndOnWithoutRestart(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    enabled: true\n")
	fw, _ := startDemo(t, "DASH_CONFIG="+cfgPath)
	state := func() model.PluginState {
		for _, in := range fw.sup.Infos() {
			if in.ID == "flux" {
				return in.State
			}
		}
		t.Fatal("flux is not listed")
		return ""
	}
	fluxParts := func() (map[string]model.KPI, []model.AttentionItem) {
		d, _ := overviewOf(t, fw)
		return kpisOf(d, "flux"), itemsOf(d, "flux")
	}
	waitFor(t, func() bool {
		if _, ok := fw.hub.Latest("", model.TopicOverview); !ok {
			return false
		}
		k, _ := fluxParts()
		return len(k) == 4
	})

	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    enabled: false\n")
	waitFor(t, func() bool { return state() == model.PluginDisabled })
	waitFor(t, func() bool { k, i := fluxParts(); return len(k) == 0 && len(i) == 0 })

	writeTestFile(t, cfgPath, "plugins:\n  flux:\n    enabled: true\n")
	waitFor(t, func() bool { return state() == model.PluginHealthy })
	waitFor(t, func() bool { k, i := fluxParts(); return len(k) == 4 && len(i) == 1 })
}
