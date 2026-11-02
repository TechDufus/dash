package topics

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// The Manager publishes through the Host of each topic's owner, and the SSE name stays the one
// the SPA reads: "flux". The payload is what the Manager builds from the kubernetes service.
func TestManagerPublishesFluxThroughItsHost(t *testing.T) {
	h := hub.New()
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", false))
	m := New(Deps{Cfg: config.Defaults(), Cluster: kubeOf(f), Hosts: plugintest.AllRunning(h, LegacyManifests()...)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)

	eventually(t, "every topic", func() bool {
		for _, topic := range []model.Topic{model.TopicOverview, model.TopicFlux} {
			if _, ok := h.Latest("", topic); !ok {
				return false
			}
		}
		return true
	})
	snap, _, stop := h.Subscribe("", nil)
	defer stop()
	var got []string
	for _, ev := range snap {
		got = append(got, ev.Topic)
	}
	slices.Sort(got)
	if want := []string{"core.overview", "flux"}; !slices.Equal(got, want) {
		t.Fatalf("published %v, want exactly %v", got, want)
	}

	env, _ := latest(h, model.TopicFlux)
	wantJSON, err := json.Marshal(m.buildFlux(f))
	if err != nil {
		t.Fatal(err)
	}
	if string(env.Data) != string(wantJSON) {
		t.Errorf("flux payload differs from the Manager's build:\n got %s\nwant %s", env.Data, wantJSON)
	}
	if env.Topic != string(model.TopicFlux) {
		t.Errorf("flux envelope is named %q", env.Topic)
	}
	if s, ok := sourceNamed(env.Sources, "kubernetes"); !ok || !s.OK {
		t.Errorf("flux sources = %+v, want kubernetes ok", env.Sources)
	}
}

// A flux that is switched off publishes nothing, and the overview does not wait for it.
func TestDisabledFluxPublishesNothing(t *testing.T) {
	h := hub.New()
	hp := plugintest.Without(plugintest.AllRunning(h, LegacyManifests()...), "flux")
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", false))
	m := New(Deps{Cfg: config.Defaults(), Cluster: kubeOf(f), Hosts: hp})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)

	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	time.Sleep(2 * rebuildEvery) // the flux build would have happened by now
	if n := hp.Publishes("flux"); n != 0 {
		t.Errorf("the Manager called the disabled flux' Host %d times", n)
	}
	if _, ok := h.Latest("", model.TopicFlux); ok {
		t.Error("the flux topic is published although flux does not run")
	}
}

// The shim, the supervisor, the Manager and the hub together: an explicitly disabled plugin shows
// as disabled in core.plugins and its topic never reaches a viewer, while the overview still comes.
func TestExplicitDisableRemovesShimTopics(t *testing.T) {
	h := hub.New()
	off := false
	set := &plugintest.Settings{}
	set.Set(plugin.InstanceConfig{Name: "flux", Enabled: &off, Config: &config.FluxConfig{}})
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: set, StatusEvery: 10 * time.Millisecond})
	h.SetOrder(sup.TopicOrder)
	m := testManager(config.Defaults(), sup)
	m.mark(model.TopicFlux) // no kubernetes service runs: the overview is what a dirty round publishes
	if err := sup.Add(m.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())
	m.Run(ctx)

	eventually(t, "the overview", func() bool { _, ok := h.Latest("", model.TopicOverview); return ok })
	if _, ok := h.Latest("", model.TopicFlux); ok {
		t.Error("flux is disabled but its topic is still served")
	}
	env, ok := latest(h, model.TopicPlugins)
	if !ok {
		t.Fatal("core.plugins is not published")
	}
	var pd model.PluginsData
	if err := json.Unmarshal(env.Data, &pd); err != nil {
		t.Fatal(err)
	}
	states := map[string]model.PluginInfo{}
	for _, in := range pd.Plugins {
		states[in.ID] = in
	}
	if states["flux"].State != model.PluginDisabled || !slices.Equal(states["flux"].Topics, []string{"flux"}) {
		t.Errorf("flux = %+v, want disabled with the topic flux", states["flux"])
	}
	if len(pd.Plugins) != 1 {
		t.Errorf("%d plugins, want only flux", len(pd.Plugins))
	}
}

// Every shim's Config returns the type of its section in config.PluginsConfig, so the two
// cannot drift (the schema of a plugin is reflected from Config).
func TestLegacyManifestsMatchConfigSections(t *testing.T) {
	sections := map[string]reflect.Type{}
	pt := reflect.TypeFor[config.PluginsConfig]()
	for i := range pt.NumField() {
		name, _, _ := strings.Cut(pt.Field(i).Tag.Get("json"), ",")
		sections[name] = pt.Field(i).Type
	}
	// The sections of the integrations that moved out of the Manager have no shim: each runs as
	// the plugin of internal/plugins/<id>.
	for _, moved := range []string{"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "grafana", "headlamp", "launcher", "kubernetes"} {
		delete(sections, moved)
	}
	ms := LegacyManifests()
	if len(ms) != len(sections) {
		t.Fatalf("%d manifests for %d sections", len(ms), len(sections))
	}
	for _, man := range ms {
		typ, ok := sections[string(man.ID)]
		if !ok {
			t.Errorf("%s has no plugins.%s section", man.ID, man.ID)
			continue
		}
		if got := reflect.TypeOf(man.Config()); got != reflect.PointerTo(typ) {
			t.Errorf("%s: Config returns %v, want *%v", man.ID, got, typ)
		}
		if man.Requires != nil {
			t.Errorf("%s declares Requires %v; a shim must not block on a missing source", man.ID, man.Requires)
		}
	}
	// The same ids and legacy topic names are valid to the supervisor, which checks the rest.
	sup := plugin.NewSupervisor(plugin.Options{})
	if err := sup.Add((&Manager{}).Plugins()...); err != nil {
		t.Fatalf("the shims are not a valid plugin set: %v", err)
	}
}

func TestLegacyShimsAreRestartOnly(t *testing.T) {
	for _, f := range (&Manager{}).Plugins() {
		if _, ok := f().(plugin.RestartOnly); !ok {
			t.Errorf("%s can be reconfigured in-process; the Manager cannot", f().Manifest().ID)
		}
	}
}

// The status of the flux shim follows the kubernetes service: unavailable without it, starting
// until its informers sync, healthy after.
func TestLegacyStatusFollowsTheKubernetesService(t *testing.T) {
	f := newFakeKube()
	holder := &kubeHolder{}
	m := New(Deps{Cfg: config.Defaults(), Cluster: holder.get, Hosts: plugintest.AllRunning(hub.New(), LegacyManifests()...)})
	if got := m.legacyStatus(); got.State != plugin.StateUnavailable {
		t.Errorf("without the kubernetes service: %s, want unavailable", got.State)
	}
	f.set(func(f *fakeKube) { f.synced = false })
	holder.cur.Store(f)
	if got := m.legacyStatus(); got.State != plugin.StateStarting || got.Message != "informers syncing" {
		t.Errorf("before the informers sync: %s %q", got.State, got.Message)
	}
	f.set(func(f *fakeKube) { f.synced = true })
	if got := m.legacyStatus(); got.State != plugin.StateStarting {
		t.Errorf("synced and listed but not yet built: %s, want starting", got.State)
	}
	m.rebuild()
	if got := m.legacyStatus(); got.State != plugin.StateHealthy {
		t.Errorf("after the first build: %s", got.State)
	}
	// A viewer never reads a URL or host out of a plugin's status: the shims name no upstream.
	for _, s := range m.legacyStatus().Sources {
		if s.Name != "kubernetes" || !s.OK {
			t.Errorf("source %+v on a shim's status", s)
		}
	}
	holder.cur.Store(nil)
	if s := m.legacyStatus().Sources; len(s) != 1 || s[0].OK || s[0].Error != "not_configured" {
		t.Errorf("sources without the service = %+v, want one not_configured", s)
	}
}

// owners is keyed by the wire name of each topic: today's legacy names stay exactly as they
// were (nothing observable moves), and a topic without one is "<plugin>.<name>", so a shim topic
// that has no legacy name still has an owner. The kubernetes plugin's topics are its own: the
// Manager owns none of them.
func TestOwnersAreKeyedByWireName(t *testing.T) {
	want := map[model.Topic]owner{"flux": {"flux", "objects"}}
	if len(owners) != len(want) {
		t.Errorf("owners has %d entries, want %d: %v", len(owners), len(want), owners)
	}
	for topic, o := range want {
		if owners[topic] != o {
			t.Errorf("owners[%q] = %+v, want %+v", topic, owners[topic], o)
		}
		if id, local, ok := Owner(topic); !ok || id != o.plugin || local != o.local {
			t.Errorf("Owner(%q) = %s %s %v", topic, id, local, ok)
		}
	}
	if _, _, ok := Owner(model.TopicOverview); ok {
		t.Error("the overview has an owner")
	}
	for _, gone := range []model.Topic{"host", model.TopicHost, model.TopicHardwareMachines, model.TopicCluster, model.TopicStorage, model.TopicApps} {
		if _, _, ok := Owner(gone); ok {
			t.Errorf("%s has an owner in the Manager: its plugin publishes it", gone)
		}
	}

	got := ownersOf([]plugin.Manifest{{ID: "aa", Topics: []plugin.TopicSpec{
		{Name: "rows", Legacy: "old"}, {Name: "plain"},
	}}})
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"aa.plain", "old"}) {
		t.Fatalf("keys = %v", keys)
	}
	if got["aa.plain"] != (owner{"aa", "plain"}) {
		t.Errorf("aa.plain owner = %+v", got["aa.plain"])
	}
}

// watchRig is a Manager over a kubernetes service the test swaps and a hub that records flux.
type watchRig struct {
	m      *Manager
	h      *hub.Hub
	holder *kubeHolder
}

func newWatchRig(t *testing.T, f *fakeKube) *watchRig {
	t.Helper()
	h := hub.New()
	holder := &kubeHolder{}
	if f != nil {
		holder.cur.Store(f)
	}
	return &watchRig{
		m:      New(Deps{Cfg: config.Defaults(), Cluster: holder.get, Hosts: plugintest.AllRunning(h, LegacyManifests()...)}),
		h:      h,
		holder: holder,
	}
}

// flux is the decoded flux topic, or ok=false while none is published.
func (r *watchRig) flux(t *testing.T) (model.FluxData, bool) {
	t.Helper()
	env, ok := latest(r.h, model.TopicFlux)
	if !ok {
		return model.FluxData{}, false
	}
	var d model.FluxData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d, true
}

// The Manager asks each kubernetes service to watch the five flux resources once, follows its
// revision and its sync, and starts over with a restarted service.
func TestWatchClusterFollowsTheService(t *testing.T) {
	f := newFakeKube()
	f.add(kube.GVRKustomizations, kustomization("apps", true))
	r := newWatchRig(t, f)

	r.m.rebuild()
	r.m.rebuild()
	if n := f.watchCalls(); n != 1 {
		t.Fatalf("Watch called %d times for one service over two rounds, want once", n)
	}
	if !reflect.DeepEqual(f.watched[0], fluxGVRs) || len(fluxGVRs) != 5 {
		t.Errorf("watched %v, want the five flux resources %v", f.watched[0], fluxGVRs)
	}
	d, ok := r.flux(t)
	if !ok || len(d.Kustomizations) != 1 {
		t.Fatalf("flux = %+v, %v; want the one kustomization", d, ok)
	}

	// An informer event moves the revision: the next round republishes without anything marking.
	f.add(kube.GVRKustomizations, kustomization("infra", false))
	r.m.rebuild()
	if d, _ := r.flux(t); len(d.Kustomizations) != 2 {
		t.Errorf("after an event flux holds %d kustomizations, want 2", len(d.Kustomizations))
	}
	if n := f.watchCalls(); n != 1 {
		t.Errorf("Watch called %d times after an event, want once", n)
	}

	// A restart hands out a new service: it gets its own Watch and flux is built from it.
	g := newFakeKube()
	g.add(kube.GVRKustomizations, kustomization("only", true))
	r.holder.cur.Store(g)
	r.m.rebuild()
	if n := g.watchCalls(); n != 1 {
		t.Errorf("the new service's Watch called %d times, want once", n)
	}
	if d, _ := r.flux(t); len(d.Kustomizations) != 1 || d.Kustomizations[0].Name != "only" {
		t.Errorf("after the restart flux = %+v, want the new service's kustomization", d.Kustomizations)
	}

	// A service that has not synced publishes nothing new; its sync republishes.
	h := newFakeKube()
	h.set(func(h *fakeKube) { h.synced = false })
	h.add(kube.GVRKustomizations, kustomization("late", true))
	r.holder.cur.Store(h)
	r.m.rebuild()
	if d, _ := r.flux(t); len(d.Kustomizations) != 1 || d.Kustomizations[0].Name != "only" {
		t.Errorf("an unsynced service was built from: %+v", d.Kustomizations)
	}
	h.set(func(h *fakeKube) { h.synced = true })
	r.m.rebuild()
	if d, _ := r.flux(t); len(d.Kustomizations) != 1 || d.Kustomizations[0].Name != "late" {
		t.Errorf("after the sync flux = %+v, want the late kustomization", d.Kustomizations)
	}
}

// Without the kubernetes service the Manager has nothing to read: flux is not published, and when
// the service appears it is.
func TestFluxFollowsTheServiceAppearing(t *testing.T) {
	r := newWatchRig(t, nil)
	r.m.rebuild()
	if _, ok := r.flux(t); ok {
		t.Fatal("flux is published without the kubernetes service")
	}
	if _, ok := latest(r.h, model.TopicOverview); ok {
		if s, found := sourceNamed(mustEnvelope(t, r.h, model.TopicOverview).Sources, "kubernetes"); !found || s.OK || s.Error != "not_configured" {
			t.Errorf("overview kubernetes source = %+v, want not_configured", s)
		}
	}
	f := newFakeKube()
	f.add(kube.GVRHelmReleases, fluxObject("HelmRelease", "apps", "web", "True"))
	r.holder.cur.Store(f)
	r.m.rebuild()
	if d, ok := r.flux(t); !ok || len(d.HelmReleases) != 1 {
		t.Errorf("flux = %+v, %v after the service appeared, want the helm release", d, ok)
	}
}

func mustEnvelope(t *testing.T, h *hub.Hub, topic model.Topic) model.Envelope {
	t.Helper()
	env, ok := latest(h, topic)
	if !ok {
		t.Fatalf("%s is not published", topic)
	}
	return env
}

// The flux build never computes from a partial list: sourceVerified comes from the GitRepository
// flux-system, so while any of the five resources has not completed its first list nothing is
// published for flux and the overview carries no unverified-source alarm; a resource the cluster
// does not serve counts as listed; the round after the last one lists, flux and the overview
// are built from the whole.
func TestManagerFluxWaitsForItsGVRs(t *testing.T) {
	f := newFakeKube()
	repo := fluxObject("GitRepository", "flux-system", "flux-system", "True")
	repo.Object["status"].(map[string]any)["conditions"] = append(
		repo.Object["status"].(map[string]any)["conditions"].([]any),
		map[string]any{"type": "SourceVerified", "status": "True"})
	f.add(kube.GVRGitRepositories, repo)
	f.add(kube.GVRKustomizations, kustomization("apps", true))
	// Everything but the kustomizations' neighbours is pending: nothing has listed yet, except
	// that the cluster serves no OCI repositories.
	f.set(func(f *fakeKube) {
		for _, gvr := range fluxGVRs {
			f.pending[gvr] = true
		}
		f.absent[kube.GVROCIRepositories] = true
	})
	r := newWatchRig(t, f)

	unverified := func() bool {
		env, ok := latest(r.h, model.TopicOverview)
		if !ok {
			return false
		}
		var ov model.OverviewData
		if err := json.Unmarshal(env.Data, &ov); err != nil {
			t.Fatal(err)
		}
		for _, it := range ov.Attention {
			if it.ID == "flux:source-unverified" {
				return true
			}
		}
		return false
	}
	// list completes the first list of gvr and moves the revision, as the informer does.
	list := func(gvr ...schemaGVR) {
		f.set(func(f *fakeKube) {
			for _, g := range gvr {
				delete(f.pending, g)
			}
			f.rev++
		})
	}

	r.m.rebuild()
	if _, ok := r.flux(t); ok {
		t.Fatal("flux published with nothing listed")
	}
	steps := [][]schemaGVR{
		{kube.GVRKustomizations},
		{kube.GVRHelmReleases},
		{kube.GVRHelmRepositories},
	}
	for _, gvrs := range steps {
		list(gvrs...)
		r.m.rebuild()
		if _, ok := r.flux(t); ok {
			t.Fatalf("flux published while %v is not listed", kube.GVRGitRepositories)
		}
		if unverified() {
			t.Fatal("the overview raised the unverified-source alarm from a partial list")
		}
	}

	list(kube.GVRGitRepositories) // the last resource: the OCI repositories are absent, which is listed
	r.m.rebuild()
	d, ok := r.flux(t)
	if !ok {
		t.Fatal("flux was not published once every resource was listed or absent")
	}
	if len(d.Sources) != 1 || d.Sources[0].Verified == nil || !*d.Sources[0].Verified || len(d.Kustomizations) != 1 {
		t.Errorf("flux = %+v, want the verified repository and the kustomization", d)
	}
	if unverified() {
		t.Error("a verified source raised the alarm")
	}
	env := mustEnvelope(t, r.h, model.TopicOverview)
	var ov model.OverviewData
	if err := json.Unmarshal(env.Data, &ov); err != nil {
		t.Fatal(err)
	}
	if !ov.Kpis.SourceVerified {
		t.Errorf("kpis = %+v, want sourceVerified once the repository is listed", ov.Kpis)
	}
}
