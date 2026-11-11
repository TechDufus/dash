package main

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// objectPlugins are the plugins that implement Objects (design N.4).
var objectPlugins = []plugin.ID{"alertmanager", "gatus", "proxmox", "hardware", "ssh", "kubernetes", "flux", "cnpg"}

// In demo mode every Objects plugin's twin finds, reads and describes the objects of its topics:
// each hit is the plugin's own ref, Get answers it and Context describes it.
func TestDemoObjectsSearchGetAndContext(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	if got := fw.sup.ObjectInstances(); !slices.Equal(sorted(got), sorted(objectPlugins)) {
		t.Fatalf("ObjectInstances = %v, want %v", got, objectPlugins)
	}
	ctx := context.Background()
	for _, id := range objectPlugins {
		o, ok := fw.sup.Objects(id)
		if !ok {
			t.Fatalf("%s: no Objects", id)
		}
		hits := o.Search("", nil, 25)
		if len(hits) == 0 {
			t.Errorf("%s: Search found nothing", id)
			continue
		}
		for _, h := range hits {
			p, kind, _, ok := model.ParseRef(h.Ref)
			if !ok || p != string(id) || kind != h.Kind || h.Title == "" {
				t.Errorf("%s: hit %+v", id, h)
				continue
			}
			if _, err := o.Get(ctx, h.Ref); err != nil {
				t.Errorf("%s: Get(%s) = %v", id, h.Ref, err)
			}
			part, err := o.Context(ctx, h.Ref, model.ContextOptions{})
			if err != nil || part.Ref != h.Ref || part.Kind != h.Kind || len(part.Facts) == 0 {
				t.Errorf("%s: Context(%s) = %+v, %v", id, h.Ref, part, err)
			}
		}
		if _, err := o.Get(ctx, string(id)+":Nothing/here"); !errors.Is(err, plugin.ErrNotFound) {
			t.Errorf("%s: Get of an unknown ref = %v", id, err)
		}
	}
}

// A demo pod reads as its detail, with at most 20 events, and its context names its charts, its
// logs, its node and its namespace.
func TestDemoPodGetAndContext(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	o, _ := fw.sup.Objects("kubernetes")
	pods := o.Search("", []string{"Pod"}, 25)
	i := slices.IndexFunc(pods, func(h model.SearchHit) bool { return h.Kind == "Pod" })
	if i < 0 {
		t.Fatal("no demo pod")
	}
	ref := pods[i].Ref
	v, err := o.Get(context.Background(), ref)
	d, ok := v.(model.PodDetail)
	if err != nil || !ok || d.Pod.Ref != ref || len(d.Events) > model.ContextMaxEvents || len(d.Conditions) == 0 {
		t.Fatalf("Get(%s) = %#v, %v", ref, v, err)
	}
	part, err := o.Context(context.Background(), ref, model.ContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var series []string
	for _, s := range part.Series {
		series = append(series, s.Name+" "+s.Args["namespace"]+"/"+s.Args["pod"])
	}
	want := d.Pod.Namespace + "/" + d.Pod.Name
	if !slices.Equal(series, []string{"pod.cpu " + want, "pod.mem " + want}) {
		t.Errorf("series = %v", series)
	}
	if part.Logs == nil || part.Logs.Namespace+"/"+part.Logs.Pod != want {
		t.Errorf("logs = %+v", part.Logs)
	}
	if len(part.Conditions) == 0 || len(part.Events) > model.ContextMaxEvents {
		t.Errorf("conditions %v, %d events", part.Conditions, len(part.Events))
	}
	related := strings.Join(part.Related, " ")
	if !strings.Contains(related, "kubernetes:Node/"+d.Pod.Node) || !strings.Contains(related, "kubernetes:Namespace/"+d.Pod.Namespace) {
		t.Errorf("related = %v", part.Related)
	}
	narrow, err := o.Context(context.Background(), ref, model.ContextOptions{Include: []model.ContextInclude{model.ContextIncludeSeries}})
	if err != nil || len(narrow.Events) != 0 || len(narrow.Related) != 0 {
		t.Errorf("a context without events or related: %+v, %v", narrow, err)
	}
}

// The other parts name what core adds to them: a VM's and the host's charts, and the objects
// next to them.
func TestDemoContextPartsNameTheirSeriesAndRelatedObjects(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	first := func(id plugin.ID, kind string) model.ContextPart {
		t.Helper()
		o, _ := fw.sup.Objects(id)
		hits := o.Search("", []string{kind}, 1)
		i := slices.IndexFunc(hits, func(h model.SearchHit) bool { return h.Kind == kind })
		if i < 0 {
			t.Fatalf("no %s %s", id, kind)
		}
		part, err := o.Context(context.Background(), hits[i].Ref, model.ContextOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return part
	}
	names := func(p model.ContextPart) (out []string) {
		for _, s := range p.Series {
			out = append(out, s.Name)
		}
		return out
	}
	vm := first("proxmox", "VM")
	_, _, vmid, _ := model.ParseRef(vm.Ref)
	if n := names(vm); len(n) != 6 || n[0] != "vm.cpu" || vm.Series[0].Args["vmid"] != vmid {
		t.Errorf("VM series = %+v", vm.Series)
	}
	if !slices.ContainsFunc(vm.Related, func(r string) bool { return strings.HasPrefix(r, "proxmox:Node/") }) {
		t.Errorf("VM related = %v", vm.Related)
	}
	host := first("hardware", "Host")
	if n := names(host); !slices.Contains(n, "host.cpu") || !slices.ContainsFunc(host.Related, func(r string) bool { return strings.HasPrefix(r, "hardware:Disk/") }) {
		t.Errorf("host series %v, related %v", n, host.Related)
	}
	if fx := first("flux", "Kustomization"); len(fx.Conditions) != 1 || !slices.ContainsFunc(fx.Related, func(r string) bool { return strings.HasPrefix(r, "kubernetes:Namespace/") }) {
		t.Errorf("flux part = %+v", fx)
	}
}

// A workload's part sums up its unhealthy pods, adds their events (still at most 20) and names
// the logs of the worst one; a workload whose pods are all healthy has none of them, and its
// bundle says it has no logs.
func TestDemoWorkloadContextCarriesItsUnhealthyPods(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	o, _ := fw.sup.Objects("kubernetes")
	sick, podEvents, healthy := 0, 0, ""
	for _, h := range o.Search("", []string{"Workload"}, 1000) {
		part, err := o.Context(context.Background(), h.Ref, model.ContextOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var pods []model.ContextFact
		for _, f := range part.Facts {
			if strings.HasPrefix(f.Name, "pod.") {
				pods = append(pods, f)
			}
		}
		if len(pods) == 0 {
			if part.Logs != nil {
				t.Errorf("%s: logs %+v without an unhealthy pod", h.Ref, part.Logs)
			}
			healthy = h.Ref
			continue
		}
		sick++
		if part.Logs == nil {
			t.Errorf("%s: no logs for the unhealthy pods %v", h.Ref, pods)
			continue
		}
		worst := slices.IndexFunc(pods, func(f model.ContextFact) bool { return f.Name == "pod."+part.Logs.Pod })
		crit := slices.ContainsFunc(pods, func(f model.ContextFact) bool { return strings.HasPrefix(f.Value, "health crit") })
		if worst < 0 || (crit && !strings.HasPrefix(pods[worst].Value, "health crit")) || !strings.Contains(pods[worst].Value, ", restarts ") {
			t.Errorf("%s: logs %+v for the unhealthy pods %v", h.Ref, part.Logs, pods)
		}
		if len(part.Events) > model.ContextMaxEvents {
			t.Errorf("%s: %d events", h.Ref, len(part.Events))
		}
		for _, e := range part.Events {
			if strings.HasPrefix(e.Object, "Pod/") {
				podEvents++
			}
		}
	}
	if sick == 0 || healthy == "" || podEvents == 0 {
		t.Fatalf("the demo has %d workloads with unhealthy pods (%d pod events) and healthy one %q", sick, podEvents, healthy)
	}
	if b := decodeBundle(t, contextGet(t, fw, cfg, demoAdmin, url.Values{"ref": {healthy}})); b.LogsNote != model.LogsNotApplicable {
		t.Errorf("a healthy workload's logs note: %q", b.LogsNote)
	}
}

func sorted(ids []plugin.ID) []plugin.ID {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
