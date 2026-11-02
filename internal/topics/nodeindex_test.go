package topics

import (
	"context"
	"slices"
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
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/svc"
)

func k8sNode(name, ip string, ready bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
		},
	}
}

// nodeCluster is a cluster of two nodes, one Ready and one not, whose informers sync at once.
func nodeCluster() *kube.Cluster {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	return kube.NewFromClients(kubefake.NewClientset(k8sNode("node-a", "192.0.2.10", true), k8sNode("node-b", "192.0.2.11", false)),
		dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))
}

func nodeIndexManager(c *kube.Cluster) *Manager {
	cfg := config.Defaults()
	return New(Deps{Cfg: cfg, Cluster: c, Hosts: plugintest.AllRunning(hub.New(), LegacyManifests()...), Links: links.New(cfg)})
}

// The Manager answers svc.NodeIndex from the informers: no node is known before they sync, then
// the VM name finds the node of that name with its readiness and InternalIP.
func TestManagerIsTheNodeIndex(t *testing.T) {
	c := nodeCluster()
	m := nodeIndexManager(c)
	var idx svc.NodeIndex = m

	if idx.Synced() {
		t.Fatal("synced before the informers started")
	}
	if n, ready, ok := idx.K8sNode("node-a"); ok || ready || n != "" {
		t.Errorf("before the sync K8sNode = %q %v %v, want nothing", n, ready, ok)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)
	eventually(t, "the informers to sync", idx.Synced)

	for _, tc := range []struct {
		vm        string
		node      string
		ready, ok bool
	}{
		{"node-a", "node-a", true, true},
		{"node-b", "node-b", false, true},
		{"not-a-node", "", false, false},
		{"NODE-A", "", false, false}, // names are matched exactly, as the VMs build always did
		{"", "", false, false},
	} {
		n, ready, ok := idx.K8sNode(tc.vm)
		if n != tc.node || ready != tc.ready || ok != tc.ok {
			t.Errorf("K8sNode(%q) = %q %v %v, want %q %v %v", tc.vm, n, ready, ok, tc.node, tc.ready, tc.ok)
		}
	}
	if ip, ok := idx.NodeIP("node-a"); !ok || ip != "192.0.2.10" {
		t.Errorf("NodeIP(node-a) = %q %v", ip, ok)
	}
	if ip, ok := idx.NodeIP("ghost"); ok || ip != "" {
		t.Errorf("NodeIP(ghost) = %q %v", ip, ok)
	}
}

// Revision moves when the node set or a node's readiness does, and only then, so a consumer that
// polls it republishes on a node change and not on a pod's.
func TestNodeIndexRevisionMovesWithNodes(t *testing.T) {
	c := nodeCluster()
	m := nodeIndexManager(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Run(ctx)
	eventually(t, "the informers to sync", m.Synced)
	// The initial adds count as changes; wait until they stop moving.
	var rev uint64
	eventually(t, "a settled revision", func() bool {
		r := m.Revision()
		time.Sleep(20 * time.Millisecond)
		settled := r == m.Revision() && r > 0
		rev = r
		return settled
	})

	changed := func(what string, act func()) {
		t.Helper()
		before := m.Revision()
		act()
		eventually(t, what, func() bool { return m.Revision() > before })
	}
	core := c.Client.CoreV1()
	changed("a node joining", func() {
		if _, err := core.Nodes().Create(ctx, k8sNode("node-c", "192.0.2.12", true), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	changed("a node turning NotReady", func() {
		if _, err := core.Nodes().Update(ctx, k8sNode("node-a", "192.0.2.10", false), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	if _, ready, ok := m.K8sNode("node-a"); !ok || ready {
		t.Errorf("node-a after the update: ready %v, ok %v", ready, ok)
	}
	changed("a node leaving", func() {
		if err := core.Nodes().Delete(ctx, "node-c", metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	})

	// A pod changes no node.
	time.Sleep(20 * time.Millisecond)
	before := m.Revision()
	if _, err := core.Pods("media").Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "p"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := m.Revision(); got != before {
		t.Errorf("a pod moved the node revision from %d to %d", before, got)
	}
	if rev == 0 {
		t.Error("the initial nodes never moved the revision")
	}
}

// The kubernetes shim exports the node index through the supervisor, as a running real plugin
// would, and no other shim exports anything: Service answers an untyped nil for them.
func TestKubernetesShimExportsTheNodeIndex(t *testing.T) {
	h := hub.New()
	m := nodeIndexManager(nodeCluster())
	sup := plugin.NewSupervisor(plugin.Options{Publisher: h, Settings: &plugintest.Settings{}, StatusEvery: 10 * time.Millisecond})
	if err := sup.Add(m.Plugins()...); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sup.Run(ctx); err != nil {
		t.Fatal(err)
	}
	defer sup.Shutdown(context.Background())

	got, ok := sup.Service("kubernetes")
	if !ok {
		t.Fatal("a running kubernetes shim has no service")
	}
	idx, isIdx := got.(svc.NodeIndex)
	if !isIdx || idx != svc.NodeIndex(m) {
		t.Fatalf("the service is %T, want the Manager as svc.NodeIndex", got)
	}
	for _, id := range []plugin.ID{"proxmox", "hardware", "flux"} {
		if s, ok := sup.Service(id); ok || s != nil {
			t.Errorf("shim %s exports %v", id, s)
		}
	}
}

// owners is keyed by the wire name of each topic: today's legacy names stay exactly as they
// were (nothing observable moves), and a topic without one is "<plugin>.<name>", so a shim topic
// that has no legacy name, like hardware.machines in P3.6, still has an owner.
func TestOwnersAreKeyedByWireName(t *testing.T) {
	want := map[model.Topic]owner{
		"vms":     {"proxmox", "vms"},
		"host":    {"hardware", "hosts"},
		"cluster": {"kubernetes", "cluster"},
		"storage": {"kubernetes", "storage"},
		"apps":    {"kubernetes", "apps"},
		"flux":    {"flux", "objects"},
	}
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

	got := ownersOf([]plugin.Manifest{{ID: "hardware", Topics: []plugin.TopicSpec{
		{Name: "hosts", Legacy: "host"}, {Name: "machines"},
	}}})
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"hardware.machines", "host"}) {
		t.Fatalf("keys = %v", keys)
	}
	if got["hardware.machines"] != (owner{"hardware", "machines"}) {
		t.Errorf("hardware.machines owner = %+v", got["hardware.machines"])
	}
}
