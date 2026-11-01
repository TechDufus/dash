package main

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// Every plugins.* section of the tree is an instance, of the type its shim's Config returns.
func TestTreeSettingsListsEverySectionOnce(t *testing.T) {
	off := false
	tree := config.DefaultTree()
	tree.Plugins.Flux.Enabled = &off
	ics := treeSettings{func() config.Tree { return tree }}.Instances()

	manifests := map[plugin.ID]plugin.Manifest{}
	for _, m := range builtinManifests() {
		manifests[m.ID] = m
	}
	if len(ics) != len(manifests) {
		t.Fatalf("%d instances for %d manifests", len(ics), len(manifests))
	}
	for _, ic := range ics {
		m, ok := manifests[ic.Name]
		if !ok {
			t.Errorf("section %s has no plugin", ic.Name)
			continue
		}
		if ic.Type != ic.Name {
			t.Errorf("%s: type %q, want the section name", ic.Name, ic.Type)
		}
		if reflect.TypeOf(ic.Config) != reflect.TypeOf(m.Config()) {
			t.Errorf("%s: config is %T, the plugin takes %T", ic.Name, ic.Config, m.Config())
		}
		if (ic.Name == "flux") != (ic.Enabled != nil && !*ic.Enabled) {
			t.Errorf("%s: enabled = %v", ic.Name, ic.Enabled)
		}
	}
}

func TestBuiltinManifestsAreTheElevenInDisplayOrder(t *testing.T) {
	var ids []string
	for _, m := range builtinManifests() {
		ids = append(ids, string(m.ID))
	}
	want := []string{"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "kubernetes", "flux", "grafana", "headlamp", "launcher"}
	if !slices.Equal(ids, want) {
		t.Fatalf("manifests = %v, want %v", ids, want)
	}
}

// main's order: Supervisor.Run returns before Manager.Run starts, so the Manager's first
// publishes find every Host running and none is dropped. Everything the SPA reads arrives, and
// the shims are live, and restart-only.
func TestRunOrderPublishesFirstPoll(t *testing.T) {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	cluster := kube.NewFromClients(kubefake.NewClientset(), dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
	cfg := config.Defaults()
	tree := config.DefaultTree()

	h := hub.New()
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: treeSettings{func() config.Tree { return tree }}})
	h.SetOrder(sup.TopicOrder)
	mgr := topics.New(topics.Deps{
		Cfg: cfg, Cluster: cluster, Hosts: sup, Contributors: sup.Contributors, Links: links.New(cfg),
		Prom: upstream.NewProm("", nil), AM: upstream.NewAlertmanager("", nil), Gatus: upstream.NewGatus("", nil),
		PVE: upstream.NewProxmox("", "", "", cfg.ProxmoxTokenSecret, nil),
	})
	if err := sup.Add(mgr.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	mgr.Run(ctx)

	want := []string{"core.plugins", "core.overview", "alerts", "status", "vms", "host", "cluster", "storage", "apps", "flux"}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _, cancelSub := h.Subscribe("", nil)
		cancelSub()
		var got []string
		for _, ev := range snap {
			got = append(got, ev.Topic)
		}
		if slices.Equal(got, want) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("topics = %v, want %v (in this order)", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sup.Live("plugins.proxmox") {
		t.Error("a legacy plugin's section applies live, but only a restart can change the Manager")
	}
	for _, in := range sup.Infos() {
		if in.State == model.PluginDisabled || in.State == model.PluginError || in.State == model.PluginBlocked {
			t.Errorf("%s is %s %q with nothing configured", in.ID, in.State, in.Message)
		}
	}
}
