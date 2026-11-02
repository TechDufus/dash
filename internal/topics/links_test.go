package topics

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
)

// podCluster is a cluster of two running pods whose informers sync at once.
func podCluster() *kube.Cluster {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	pod := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour))},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	return kube.NewFromClients(kubefake.NewClientset(pod("app-0"), pod("app-1")),
		dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
}

// linkRig is a Manager over a real supervisor that runs the real grafana and headlamp plugins
// next to the Manager's shims, as cmd/dash registers them.
type linkRig struct {
	m   *Manager
	sup *plugin.Supervisor
	set *plugintest.Settings
	h   *hub.Hub
	// providerReads counts Supervisor.Links calls: one per rebuild, not one per object.
	providerReads atomic.Int32
}

func newLinkRig(t *testing.T, cfg config.Config, lab ...plugin.InstanceConfig) *linkRig {
	t.Helper()
	r := &linkRig{h: hub.New(), set: &plugintest.Settings{}}
	r.sup = plugin.NewSupervisor(plugin.Options{Publisher: r.h, Settings: r.set, StatusEvery: 10 * time.Millisecond})
	r.h.SetOrder(r.sup.TopicOrder)
	for _, ic := range lab {
		r.set.Set(ic)
	}
	r.m = New(Deps{
		Cfg: cfg, Cluster: podCluster(), Hosts: r.sup, Contributors: r.sup.Contributors,
		LinkProviders: func() plugin.LinkResolver { r.providerReads.Add(1); return r.sup.Links() },
	})
	if err := r.sup.Add(append(r.m.Plugins(), grafana.New, headlamp.New, alertmanager.New)...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.sup.Shutdown(context.Background()) })
	r.m.Run(ctx)
	return r
}

func headlampOn(url string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: "headlamp", Config: &pluginconf.Headlamp{URL: url, Cluster: "main"}}
}

func grafanaOn(url string) plugin.InstanceConfig {
	return plugin.InstanceConfig{Name: "grafana", Config: &pluginconf.Grafana{URL: url}}
}

func headlampOff() plugin.InstanceConfig {
	off := false
	return plugin.InstanceConfig{Name: "headlamp", Enabled: &off, Config: &pluginconf.Headlamp{URL: "https://hl.example.com", Cluster: "main"}}
}

func grafanaOff() plugin.InstanceConfig {
	off := false
	return plugin.InstanceConfig{Name: "grafana", Enabled: &off, Config: &pluginconf.Grafana{URL: "https://gf.example.com"}}
}

// kinds of the links of every pod of the cluster topic, and of the host topic.
func (r *linkRig) podLinks(t *testing.T) (pods int, kinds []model.LinkKind) {
	t.Helper()
	env, ok := latest(r.h, model.TopicCluster)
	if !ok {
		return 0, nil
	}
	var d model.ClusterData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Pods {
		for _, l := range p.Links {
			kinds = append(kinds, l.Kind)
		}
	}
	return len(d.Pods), kinds
}

func (r *linkRig) hostLinks(t *testing.T) (model.HostData, bool) {
	t.Helper()
	env, ok := latest(r.h, model.TopicHost)
	if !ok {
		return model.HostData{}, false
	}
	var d model.HostData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d, true
}

func hasKind(kinds []model.LinkKind, k model.LinkKind) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

func hostHas(d model.HostData, k model.LinkKind) bool {
	for _, l := range d.Links {
		if l.Kind == k {
			return true
		}
	}
	return false
}

// With headlamp running a pod's payload carries its "Open in Headlamp" link; with headlamp
// disabled it carries none, and with grafana disabled the host's links lose "Grafana". The
// Manager reads the providers through Supervisor.Links, so a change of the rows reaches the next
// rebuild without a restart.
func TestLinksFollowTheGrafanaAndHeadlampRows(t *testing.T) {
	r := newLinkRig(t, config.Defaults(), headlampOn("https://hl.example.com"), grafanaOn("https://gf.example.com"))

	eventually(t, "headlamp links on the pods", func() bool {
		n, kinds := r.podLinks(t)
		return n == 2 && hasKind(kinds, model.LinkKindHeadlamp)
	})
	eventually(t, "the grafana link on the host", func() bool {
		d, ok := r.hostLinks(t)
		return ok && hostHas(d, model.LinkKindGrafana)
	})

	r.set.Set(headlampOff())
	r.set.Set(grafanaOff())
	r.sup.Reload()
	eventually(t, "both rows to be disabled", func() bool {
		states := map[string]model.PluginState{}
		for _, in := range r.sup.Infos() {
			states[in.ID] = in.State
		}
		return states["headlamp"] == model.PluginDisabled && states["grafana"] == model.PluginDisabled
	})
	r.m.mark(model.TopicCluster, model.TopicHost)
	eventually(t, "the pods to lose their headlamp links", func() bool {
		n, kinds := r.podLinks(t)
		return n == 2 && !hasKind(kinds, model.LinkKindHeadlamp)
	})
	eventually(t, "the host to lose its grafana link", func() bool {
		d, ok := r.hostLinks(t)
		return ok && !hostHas(d, model.LinkKindGrafana)
	})

	// Back on: the links come back.
	r.set.Set(headlampOn("https://hl.example.com"))
	r.set.Set(grafanaOn("https://gf.example.com"))
	r.sup.Reload()
	eventually(t, "headlamp to run again", func() bool { return r.sup.Running("headlamp") && r.sup.Running("grafana") })
	r.m.mark(model.TopicCluster, model.TopicHost)
	eventually(t, "the links to return", func() bool {
		_, kinds := r.podLinks(t)
		d, _ := r.hostLinks(t)
		return hasKind(kinds, model.LinkKindHeadlamp) && hostHas(d, model.LinkKindGrafana)
	})
}

// The providers are read once per rebuild: two pods, the host and the overview are linked from
// one snapshot, and the next rebuild reads them again.
func TestProvidersAreReadOncePerRebuild(t *testing.T) {
	cfg := config.Defaults()
	var reads atomic.Int32
	m := New(Deps{
		Cfg: cfg, Cluster: podCluster(),
		Hosts:         plugintest.AllRunning(hub.New(), LegacyManifests()...),
		LinkProviders: func() plugin.LinkResolver { reads.Add(1); return plugin.NoLinks },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.d.Cluster.Start(ctx)
	eventually(t, "the informers to sync", m.d.Cluster.HasSynced)
	m.mark(model.TopicCluster, model.TopicFlux, model.TopicStorage)
	m.rebuild()
	if n := reads.Load(); n != 1 {
		t.Errorf("providers read %d times for a rebuild of three topics and the overview, want once", n)
	}
	m.mark(model.TopicCluster)
	m.rebuild()
	if n := reads.Load(); n != 2 {
		t.Errorf("providers read %d times after two rebuilds, want 2", n)
	}
	if m.linkSet.Load() != nil {
		t.Error("the snapshot outlives its rebuild")
	}
}

// A Manager with no supervisor (tests, tools) builds every link-bearing payload with empty,
// non-nil link lists instead of panicking.
func TestManagerWithoutProvidersBuildsEmptyLinks(t *testing.T) {
	cfg := config.Defaults()
	m := New(Deps{Cfg: cfg, Cluster: podCluster()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.d.Cluster.Start(ctx)
	eventually(t, "the informers to sync", m.d.Cluster.HasSynced)
	for _, p := range m.buildCluster(time.Now()).Pods {
		if p.Links == nil || len(p.Links) != 0 {
			t.Errorf("%s: links = %#v, want an empty non-nil slice", p.Name, p.Links)
		}
	}
	if h := m.buildHost(); h.Links == nil {
		t.Error("host links are nil")
	}
}

// The host's model comes from the flat HostModel (plugins.hardware.hosts[0].model) and reaches
// the host envelope, which is what the SPA's hardware page reads.
func TestHostModelReachesTheHostEnvelope(t *testing.T) {
	cfg := config.Defaults()
	cfg.HostModel = "Dell R730"
	h := hub.New()
	m := testManager(cfg, plugintest.AllRunning(h, LegacyManifests()...))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)
	var got model.HostData
	eventually(t, "the host topic", func() bool {
		env, ok := latest(h, model.TopicHost)
		return ok && json.Unmarshal(env.Data, &got) == nil
	})
	if got.Model != "Dell R730" {
		t.Errorf("host.model = %q, want the configured model", got.Model)
	}
	// Unset it shows nothing.
	if m2 := testManager(config.Defaults(), nil); m2.buildHost().Model != "" {
		t.Errorf("host.model = %q without a configured model", m2.buildHost().Model)
	}
}
