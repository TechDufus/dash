package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// objectPlugins are the plugins that implement Objects (design N.4).
var objectPlugins = []plugin.ID{"alertmanager", "gatus", "proxmox", "hardware", "kubernetes", "flux", "cnpg"}

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
		hits := o.Search("", 25)
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
	pods := o.Search("Pod", 25)
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
		hits := o.Search("", 1000)
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

func sorted(ids []plugin.ID) []plugin.ID {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}
