package main

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/secret"
)

// startDemo runs the framework as run does in demo mode (every plugin's twin, the overview
// publisher on the supervisor's contributors, the reloader's loop) and returns it with its
// configuration once started; it shuts down with the test. env adds to the environment.
func startDemo(t *testing.T, env ...string) (*framework, config.Config) {
	t.Helper()
	startEnv(t, t.TempDir(), append([]string{"DASH_DEMO=true"}, env...)...)
	res, store, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	fw := newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, nil, nil, nil, nil, true)
	ctx, cancel := context.WithCancel(context.Background())
	ov := overview.New(overview.Deps{Hosts: fw.sup, Contributors: fw.sup.Contributors})
	if err := fw.start(ctx, registry(nil), func() { ov.Run(ctx) }); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		fw.sup.Shutdown(context.Background())
	})
	return fw, res.Config
}

// demoTopics are the topics a demo run publishes, in the stream's order: the core's two, every
// plugin's twin's, the launcher's last.
var demoTopics = []string{
	"core.plugins", "core.overview", "alertmanager.alerts", "gatus.status", "proxmox.vms", "proxmox.node",
	"proxmox.storage", "hardware.hosts", "hardware.machines", "kubernetes.cluster", "kubernetes.storage",
	"kubernetes.apps", "flux.objects", "cnpg.clusters", "rustfs.usage", "launcher.links",
}

// awaitDemoTopics waits for the hub to hold exactly demoTopics, in order, and returns its snapshot.
func awaitDemoTopics(t *testing.T, fw *framework) []model.Envelope {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, _, cancel := fw.hub.Subscribe("", nil)
		cancel()
		var got []string
		var out []model.Envelope
		for _, ev := range snap {
			got = append(got, ev.Topic)
			var e model.Envelope
			if err := json.Unmarshal(ev.Data, &e); err != nil {
				t.Fatalf("%s: %v", ev.Topic, err)
			}
			out = append(out, e)
		}
		if slices.Equal(got, demoTopics) {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("topics = %v, want %v (in this order)", got, demoTopics)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Every object a demo run publishes names itself: each struct with a `Ref string` field has it
// set, `ParseRef` accepts it, and its plugin is the plugin that published the topic (for
// core.overview, the plugin of the attention item) and a registered plugin id. The walk covers
// every topic of the hub, so a topic whose type forgot its ref, a producer that left it empty or
// one that stamped another plugin's ref fails here and names the path.
func TestEveryPublishedObjectHasARef(t *testing.T) {
	fw, _ := startDemo(t)
	envs := awaitDemoTopics(t, fw)

	registered := map[plugin.ID]bool{}
	for _, m := range fw.sup.Manifests() {
		registered[m.ID] = true
	}
	types := model.TopicTypes()
	refTypes := 0
	for _, env := range envs {
		typ, ok := types[model.Topic(env.Topic)]
		if !ok {
			t.Errorf("%s: no entry in model.TopicTypes", env.Topic)
			continue
		}
		payload := reflect.New(typ)
		if err := json.Unmarshal(env.Data, payload.Interface()); err != nil {
			t.Errorf("%s: %v", env.Topic, err)
			continue
		}
		owner, _, _ := strings.Cut(env.Topic, ".")
		switch owner {
		case string(plugin.Core):
			if env.Topic != string(model.TopicOverview) {
				continue // core.plugins carries plugin rows, which have no refs
			}
			// The overview holds every plugin's items: each carries its own plugin's ref.
			ov := payload.Elem().Interface().(model.OverviewData)
			if len(ov.Attention) == 0 {
				t.Error("core.overview: the demo raises no attention item, so no ref was checked")
			}
			for _, it := range ov.Attention {
				if it.Plugin == "" || !registered[plugin.ID(it.Plugin)] {
					t.Errorf("core.overview: item %q names plugin %q, which is not registered", it.ID, it.Plugin)
					continue
				}
				plugintest.AssertRefs(t, fw.sup, plugin.ID(it.Plugin), it, model.AttentionItem{})
				refTypes++
			}
		default:
			if !registered[plugin.ID(owner)] {
				t.Errorf("%s: %q is not a registered plugin", env.Topic, owner)
				continue
			}
			plugintest.AssertRefs(t, fw.sup, plugin.ID(owner), payload.Interface())
			if countRefs(payload.Elem()) == 0 && !refless[env.Topic] {
				t.Errorf("%s: the payload carries no `Ref string` struct, so nothing was checked; add the topic to refless only if its objects have no identity", env.Topic)
			}
			refTypes++
		}
	}
	if refTypes == 0 {
		t.Fatal("no topic was walked")
	}
}

// refless are the topics whose payloads hold no object with an identity.
var refless = map[string]bool{"launcher.links": true}

// countRefs is how many structs under v have a `Ref string` field.
func countRefs(v reflect.Value) int {
	n := 0
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			n += countRefs(v.Elem())
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			n += countRefs(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			n += countRefs(v.MapIndex(k))
		}
	case reflect.Struct:
		if f := v.FieldByName("Ref"); f.IsValid() && f.Kind() == reflect.String {
			n++
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				n += countRefs(v.Field(i))
			}
		}
	}
	return n
}
