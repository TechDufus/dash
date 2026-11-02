package proxmox

import (
	"context"
	"net/http"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// current is the plugin instance that runs now (a reload makes a new one).
func (e *env) current() *Plugin {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.p
}

func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "proxmox" || m.Name != "Proxmox VE" || m.Version != "1.0.0" || m.Description == "" {
		t.Errorf("manifest = %+v", m)
	}
	if !slices.Equal(m.Uses, []plugin.ID{"kubernetes", "prometheus"}) || len(m.Requires) != 0 {
		t.Errorf("uses %v requires %v: want a soft use of kubernetes and prometheus and no requirement", m.Uses, m.Requires)
	}
	var names []string
	for _, tp := range m.Topics {
		names = append(names, tp.Name)
	}
	if !slices.Equal(names, []string{"vms", "node", "storage"}) {
		t.Errorf("topics = %v, want the wire order vms, node, storage", names)
	}
	var series []string
	for _, s := range m.Series {
		series = append(series, s.Name)
	}
	if !slices.Equal(series, []string{"vm.cpu", "vm.mem", "vm.netIn", "vm.netOut", "vm.diskRead", "vm.diskWrite"}) {
		t.Errorf("series = %v", series)
	}
	if len(m.Actions) != 0 {
		t.Errorf("actions = %v: the plugin has none", m.Actions)
	}
	// The nav entry is the shim's, so the rail does not move.
	want := model.NavItem{Path: "/vms", Label: "VMs", Title: "Virtual machines", Icon: "Server"}
	if len(m.UI.Nav) != 1 || m.UI.Nav[0] != want {
		t.Errorf("nav = %+v, want %+v", m.UI.Nav, want)
	}
	if _, ok := m.Config().(*pluginconf.Proxmox); !ok {
		t.Errorf("Config() is %T", m.Config())
	}
}

// A URL alone is not enough: the console and shell links used to appear with a URL and no token,
// and now the plugin is off until it can read Proxmox.
func TestProxmoxConfiguredNeedsURLAndToken(t *testing.T) {
	p := New().(*Plugin)
	full := defaultConfig("https://pve.example.com:8006")
	for _, tc := range []struct {
		name string
		mod  func(*pluginconf.Proxmox)
		want bool
	}{
		{"everything set", func(*pluginconf.Proxmox) {}, true},
		{"no url", func(c *pluginconf.Proxmox) { c.URL = "" }, false},
		{"no token id", func(c *pluginconf.Proxmox) { c.TokenID = "" }, false},
		{"no token secret", func(c *pluginconf.Proxmox) { c.TokenSecret = secret.Value{} }, false},
		{"a url alone", func(c *pluginconf.Proxmox) { c.TokenID, c.TokenSecret = "", secret.Value{} }, false},
	} {
		c := full
		tc.mod(&c)
		if got := p.Configured(&c); got != tc.want {
			t.Errorf("%s: Configured = %v, want %v", tc.name, got, tc.want)
		}
	}
	if p.Configured(&pluginconf.Proxmox{}) || p.Configured("not a section") {
		t.Error("an empty or foreign configuration is configured")
	}
}

func TestEveryRealPluginSaysWhatToSet(t *testing.T) {
	hint := New().(plugin.ConfigHinter).ConfigHint()
	if !strings.HasPrefix(hint, "plugins.proxmox.") {
		t.Errorf("hint = %q, want it to start with plugins.proxmox.", hint)
	}
}

func TestValidateDelegatesToTheSection(t *testing.T) {
	p := New().(*Plugin)
	c := defaultConfig("https://pve.example.com:8006")
	if err := p.Validate(&c); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	c.Series.VMSelector = `id="x"` // no {vmid}
	if err := p.Validate(&c); err == nil {
		t.Error("a selector with no {vmid} passed")
	}
	if err := p.Validate(42); err == nil {
		t.Error("a foreign configuration passed")
	}
}

// Every visible state: stopped and started again, the topics reach an open stream after the row.
func TestVisibilityRuleA(t *testing.T) {
	pve := newFakePVE(t, false)
	off := plugin.InstanceConfig{Config: &pluginconf.Proxmox{}} // nothing set: disabled
	c := defaultConfig(pve.URL)
	on := plugin.InstanceConfig{Config: &c}
	plugintest.AssertTopicsReachOpenStream(t, func() plugin.Plugin { return &Plugin{t: fastTiming()} }, off, on)
}

// The status follows design 5.3: starting until the node is known and the first VM poll has ended,
// healthy when every source answers, degraded when some do not, error when none does.
func TestStatusStates(t *testing.T) {
	t.Run("healthy and the sources", func(t *testing.T) {
		e := newEnv(t, envOpts{})
		e.state(t, plugin.StateHealthy)
		srcs := e.current().Status().Sources
		if len(srcs) != 1 || srcs[0].Name != "proxmox" || !srcs[0].OK {
			t.Errorf("sources = %+v, want one ok proxmox source", srcs)
		}
	})
	t.Run("degraded when a source fails, with the category", func(t *testing.T) {
		pve := newFakePVE(t, false)
		pve.fail("/nodes/pve-1/status", http.StatusForbidden)
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateDegraded)
		if !strings.Contains(pi.Message, "node: auth") || strings.Contains(pi.Message, "vms") {
			t.Errorf("message = %q", pi.Message)
		}
		if srcs := e.current().Status().Sources; len(srcs) != 1 || srcs[0].OK || srcs[0].Error != "auth" {
			t.Errorf("sources = %+v, want proxmox failing with auth", srcs)
		}
	})
	t.Run("error when every source fails", func(t *testing.T) {
		pve := newFakePVE(t, false)
		for _, p := range []string{"/cluster/resources", "/nodes/pve-1/status", "/nodes/pve-1/storage"} {
			pve.fail(p, http.StatusBadGateway)
		}
		e := newEnv(t, envOpts{pve: pve})
		pi := e.state(t, plugin.StateError)
		if !strings.Contains(pi.Message, "vms: http_502") {
			t.Errorf("message = %q", pi.Message)
		}
	})
	t.Run("never unavailable while it runs", func(t *testing.T) {
		pve := newFakePVE(t, false)
		pve.fail("/cluster/resources", http.StatusInternalServerError)
		e := newEnv(t, envOpts{pve: pve})
		e.state(t, plugin.StateDegraded)
		pve.heal("/cluster/resources")
		e.state(t, plugin.StateHealthy)
	})
	t.Run("starting before the first poll", func(t *testing.T) {
		p := New().(*Plugin)
		if st := p.Status(); st.State != plugin.StateStarting {
			t.Errorf("before Start: %s", st.State)
		}
	})
}

// A poll that fails keeps the last good payload and marks its source; the next good poll clears it.
func TestAFailedPollKeepsTheLastGoodPayload(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve})
	e.vms(t, "the first payload", func(d model.VMsData) bool { return len(d.VMs) == 4 })

	pve.fail("/cluster/resources", http.StatusBadGateway)
	d, srcs := e.vms(t, "a failing source", func(model.VMsData) bool {
		var cur model.VMsData
		s, _ := e.payload(t, "proxmox.vms", &cur)
		return len(s) == 1 && !s[0].OK
	})
	_ = d
	var kept model.VMsData
	srcs, _ = e.payload(t, "proxmox.vms", &kept)
	if len(kept.VMs) != 4 || srcs[0].Name != "proxmox" || srcs[0].Error != "http_502" {
		t.Errorf("after a failed poll: %d VMs, sources %+v; want the last good four and http_502", len(kept.VMs), srcs)
	}
	pve.heal("/cluster/resources")
	waitFor(t, "the source to recover", func() bool {
		s, _ := e.payload(t, "proxmox.vms", &kept)
		return len(s) == 1 && s[0].OK
	})
}

// The envelope of each topic names its own source only: proxmox for the guests and the node, and
// the kubernetes source that proxmox.vms carried is gone.
func TestEnvelopeSourcesNameOnlyTheirOwn(t *testing.T) {
	e := newEnv(t, envOpts{})
	e.state(t, plugin.StateHealthy)
	for _, topic := range []string{"proxmox.vms", "proxmox.node", "proxmox.storage"} {
		var any_ map[string]any
		waitFor(t, topic, func() bool { _, ok := e.payload(t, topic, &any_); return ok })
		srcs, _ := e.payload(t, topic, &any_)
		if len(srcs) != 1 || srcs[0].Name != "proxmox" || !srcs[0].OK {
			t.Errorf("%s sources = %+v, want one ok proxmox source", topic, srcs)
		}
	}
}

func TestNodeTopicCarriesTheFacts(t *testing.T) {
	e := newEnv(t, envOpts{})
	var n pvemodel.NodeData
	waitFor(t, "node facts", func() bool {
		e.payload(t, "proxmox.node", &n)
		return n.Version != ""
	})
	want := pvemodel.NodeData{
		Ref: "proxmox:Node/pve-1", Node: "pve-1", Version: "9.0.5", Kernel: "6.8.12-4-pve",
		CPUModel: "Example CPU @ 2.10GHz", UptimeSeconds: 2422190,
	}
	if n != want {
		t.Errorf("node = %+v, want %+v", n, want)
	}
	plugintest.AssertRefs(t, e.sup, ID, n, pvemodel.NodeData{})
}

func TestVMsTopic(t *testing.T) {
	e := newEnv(t, envOpts{})
	d, _ := e.vms(t, "the guests", func(d model.VMsData) bool { return len(d.VMs) == 4 })
	ids := []int{}
	for _, vm := range d.VMs {
		ids = append(ids, vm.VMID)
	}
	if !slices.Equal(ids, []int{101, 102, 103, 9000}) {
		t.Errorf("ids = %v, want them in order", ids)
	}
	vm := d.VMs[0]
	if vm.Ref != "proxmox:VM/101" || vm.Name != "k8s-cp-1" || vm.Status != "running" || !slices.Equal(vm.Tags, []string{"k8s", "control-plane"}) {
		t.Errorf("vm = %+v", vm)
	}
	if vm.K8sNode == nil || vm.K8sNode.Name != "k8s-cp-1" || !vm.K8sNode.Ready {
		t.Errorf("k8sNode = %+v, want the index's node", vm.K8sNode)
	}
	if d.VMs[1].K8sNode != nil {
		t.Errorf("db-1 is not a node: %+v", d.VMs[1].K8sNode)
	}
	if len(d.Tasks) != 1 || d.Tasks[0].Type != "qmstart" {
		t.Errorf("tasks = %+v", d.Tasks)
	}
	plugintest.AssertRefs(t, e.sup, ID, d, model.VM{})
}

func TestStorageTopicFromTheAPISkipsInactiveStorages(t *testing.T) {
	e := newEnv(t, envOpts{})
	var s pvemodel.PVEStorageData
	waitFor(t, "storages", func() bool { e.payload(t, "proxmox.storage", &s); return len(s.Storages) > 0 })
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/local", Name: "local", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/tank", Name: "tank", UsedBytes: 50, SizeBytes: 100},
	}
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v, want %+v", s.Storages, want)
	}
	plugintest.AssertRefs(t, e.sup, ID, s, pvemodel.PVEStorage{})
}

// Both sources fill proxmox.storage, and the Prometheus one assembles exactly what the Manager's
// builder did for the same samples: the name from pve_storage_info (the id when there is none),
// usage by id, sorted by name.
func TestStorageSourceAPIAndPrometheus(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg: func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		// Loaded before the plugin starts: a poll that ran in the middle of loading it published
		// three storages without their names, and the wait below saw them.
		prefill: func(e *env) {
			e.prom.set(exprStorageUsed,
				sample(1000, "id", "storage/pve-1/local"), sample(50, "id", "storage/pve-1/tank"), sample(7, "id", "storage/pve-1/nameless"))
			e.prom.set(exprStorageSize,
				sample(4000, "id", "storage/pve-1/local"), sample(100, "id", "storage/pve-1/tank"), sample(70, "id", "storage/pve-1/nameless"))
			e.prom.set(exprStorageInfo,
				sample(1, "id", "storage/pve-1/local", "storage", "local"), sample(1, "id", "storage/pve-1/tank", "storage", "tank"))
		},
	})
	var s pvemodel.PVEStorageData
	var srcs []model.SourceStatus
	waitFor(t, "storages from prometheus", func() bool {
		srcs, _ = e.payload(t, "proxmox.storage", &s)
		return len(s.Storages) == 3
	})
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/local", Name: "local", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/storage/pve-1/nameless", Name: "storage/pve-1/nameless", UsedBytes: 7, SizeBytes: 70},
		{Ref: "proxmox:Storage/tank", Name: "tank", UsedBytes: 50, SizeBytes: 100},
	}
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v\nwant      %+v", s.Storages, want)
	}
	if len(srcs) != 1 || srcs[0].Name != "prometheus" || !srcs[0].OK {
		t.Errorf("sources = %+v, want one ok prometheus source", srcs)
	}
	if e.pve.count("/nodes/pve-1/storage") != 0 {
		t.Error("the prometheus source asked the Proxmox API for the storages")
	}
	// A failing query leaves the other values and its own old ones; the source fails.
	e.prom.setErr(exprStorageSize, upstreamErr("down"))
	waitFor(t, "the prometheus source to fail", func() bool {
		srcs, _ = e.payload(t, "proxmox.storage", &s)
		return len(srcs) == 1 && !srcs[0].OK
	})
	if !slices.Equal(s.Storages, want) {
		t.Errorf("after a failed query: %+v, want the last good list", s.Storages)
	}
	if srcs[0].Error != "unreachable" {
		t.Errorf("error = %q, want a category", srcs[0].Error)
	}
}

// An exporter that reports the storages of several nodes: this node's are listed, with its own
// numbers, and no ref repeats.
func TestStoragePrometheusSelectsThisNodesStorages(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg:     func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		prefill: twoNodeStorages,
	})
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/local", Name: "local", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/tank", Name: "tank", UsedBytes: 50, SizeBytes: 100},
	}
	var s pvemodel.PVEStorageData
	waitFor(t, "this node's storages", func() bool { e.payload(t, "proxmox.storage", &s); return len(s.Storages) > 0 })
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v\nwant      %+v", s.Storages, want)
	}
}

// When no id carries this node's name, every storage stays and the ones that share a name get
// their id as the ref, each with its own numbers.
func TestStoragePrometheusRefsNeverRepeat(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg: func(c *pluginconf.Proxmox) {
			c.Storage.Source = pluginconf.ProxmoxStoragePrometheus
			c.Node = "pve-9"
		},
		prefill: twoNodeStorages,
	})
	want := []pvemodel.PVEStorage{
		{Ref: "proxmox:Storage/storage/pve-1/local", Name: "local", UsedBytes: 1000, SizeBytes: 4000},
		{Ref: "proxmox:Storage/storage/pve-2/local", Name: "local", UsedBytes: 2000, SizeBytes: 8000},
		{Ref: "proxmox:Storage/tank", Name: "tank", UsedBytes: 50, SizeBytes: 100},
	}
	var s pvemodel.PVEStorageData
	waitFor(t, "every node's storages", func() bool { e.payload(t, "proxmox.storage", &s); return len(s.Storages) > 0 })
	if !slices.Equal(s.Storages, want) {
		t.Errorf("storages = %+v\nwant      %+v", s.Storages, want)
	}
	refs := map[string]bool{}
	for _, st := range s.Storages {
		if refs[st.Ref] {
			t.Errorf("ref %s repeats", st.Ref)
		}
		refs[st.Ref] = true
	}
}

// twoNodeStorages loads pve-1 and pve-2: both have a "local" with different numbers, and only
// pve-1 has "tank".
func twoNodeStorages(e *env) {
	e.prom.set(exprStorageUsed,
		sample(1000, "id", "storage/pve-1/local"), sample(2000, "id", "storage/pve-2/local"), sample(50, "id", "storage/pve-1/tank"))
	e.prom.set(exprStorageSize,
		sample(4000, "id", "storage/pve-1/local"), sample(8000, "id", "storage/pve-2/local"), sample(100, "id", "storage/pve-1/tank"))
	e.prom.set(exprStorageInfo,
		sample(1, "id", "storage/pve-1/local", "storage", "local"), sample(1, "id", "storage/pve-2/local", "storage", "local"),
		sample(1, "id", "storage/pve-1/tank", "storage", "tank"))
}

func TestStoragePrometheusWithoutPrometheusIsNotConfigured(t *testing.T) {
	e := newEnv(t, envOpts{noProm: true, cfg: func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus }})
	waitFor(t, "the source to be reported", func() bool {
		var s pvemodel.PVEStorageData
		srcs, ok := e.payload(t, "proxmox.storage", &s)
		return ok && len(srcs) == 1 && srcs[0].Error == "not_configured" && srcs[0].Name == "prometheus" && s.Storages != nil
	})
	if pi := e.state(t, plugin.StateDegraded); !strings.Contains(pi.Message, "storage: not_configured") {
		t.Errorf("message = %q", pi.Message)
	}
}

// A Prometheus whose service appears after the first storage poll (a credential filled late) is
// polled at once, not at the next interval (an hour here), so the source stops reading
// not_configured.
func TestStoragePollsAtOnceWhenPrometheusAppears(t *testing.T) {
	e := newEnv(t, envOpts{
		lateProm: true,
		cfg:      func(c *pluginconf.Proxmox) { c.Storage.Source = pluginconf.ProxmoxStoragePrometheus },
		timing:   func(tm *timing) { tm.storage = time.Hour },
		prefill: func(e *env) {
			e.prom.set(exprStorageUsed, sample(1000, "id", "storage/pve-1/local"))
			e.prom.set(exprStorageSize, sample(4000, "id", "storage/pve-1/local"))
			e.prom.set(exprStorageInfo, sample(1, "id", "storage/pve-1/local", "storage", "local"))
		},
	})
	waitFor(t, "the source to say not_configured", func() bool {
		var s pvemodel.PVEStorageData
		srcs, ok := e.payload(t, "proxmox.storage", &s)
		return ok && len(srcs) == 1 && srcs[0].Error == "not_configured"
	})

	e.promReleased.Store(true)
	waitFor(t, "the storages from the new querier", func() bool {
		var s pvemodel.PVEStorageData
		srcs, _ := e.payload(t, "proxmox.storage", &s)
		return len(srcs) == 1 && srcs[0].OK && len(s.Storages) == 1
	})
}

// The configured node is known from Start, with a PVE that never answers /nodes.
func TestNodeNameIsOKFromStartWhenConfigured(t *testing.T) {
	pve := newFakePVE(t, false)
	pve.fail("/nodes", http.StatusInternalServerError)
	e := newEnv(t, envOpts{pve: pve})
	n, ok := e.current().Service().(svc.NodeName).NodeName()
	if !ok || n != "pve-1" {
		t.Fatalf("NodeName = %q, %v right after Start", n, ok)
	}
	// The service answers from the supervisor, too.
	v, found := e.sup.Service(ID)
	if !found {
		t.Fatal("the supervisor has no service for the running plugin")
	}
	if n, ok := v.(svc.NodeName).NodeName(); !ok || n != "pve-1" {
		t.Errorf("Supervisor.Service: %q %v", n, ok)
	}
	if pve.count("/nodes") != 0 {
		t.Errorf("a configured node asked /nodes %d times", pve.count("/nodes"))
	}
}

// Before Start the plugin has no service.
func TestNoServiceBeforeStart(t *testing.T) {
	if s := New().(*Plugin).Service(); s != nil {
		t.Errorf("Service before Start = %v", s)
	}
}

func TestExplicitNodeSkipsDiscovery(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.Node = "pve-1" }})
	e.state(t, plugin.StateHealthy)
	if pve.count("/nodes") != 0 {
		t.Errorf("GET /nodes was called %d times with a node configured", pve.count("/nodes"))
	}
	// Every request of the node client names the configured node.
	if pve.count("/nodes/pve-1/status") == 0 {
		t.Error("the status of the configured node was never read")
	}
}

// A rotated token reaches the nodeless client and the node client at once, without a restart.
func TestReloadedSecretReachesTheProxmoxClient(t *testing.T) {
	pve := newFakePVE(t, false)
	tok := secret.New("first-secret-value", "")
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.Node = ""; c.TokenSecret = tok }})
	e.state(t, plugin.StateHealthy)
	for _, a := range pve.authHeaders() {
		if a != "PVEAPIToken="+testTokenID+"=first-secret-value" {
			t.Fatalf("Authorization = %q", a)
		}
	}
	tok.Update("rotated-secret-value")
	waitFor(t, "the rotated secret on the node client", func() bool {
		hs := pve.authHeaders()
		return len(hs) > 0 && hs[len(hs)-1] == "PVEAPIToken="+testTokenID+"=rotated-secret-value"
	})
	// The nodeless client (discovery) holds the same handle.
	if _, err := e.current().rt.Load().nodeless.Nodes(context.Background()); err != nil {
		t.Fatal(err)
	}
	hs := pve.authHeaders()
	if hs[len(hs)-1] != "PVEAPIToken="+testTokenID+"=rotated-secret-value" {
		t.Errorf("the nodeless client sent %q", hs[len(hs)-1])
	}
}

// runtimeGoroutines counts the goroutines whose stack has a frame of the plugin's runtime: the
// pollers, the node index watcher and discovery, and anything they started.
func runtimeGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := goruntime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "internal/plugins/proxmox.(*runtime)") {
			count++
		}
	}
	return count
}

// Stop ends every goroutine: they are gone shortly after it, no request reaches the fake
// afterwards, and Stop returns in time.
func TestStopEndsEveryGoroutine(t *testing.T) {
	before := runtimeGoroutines()
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve, cfg: func(c *pluginconf.Proxmox) { c.Node = "" }}) // discovery too
	e.state(t, plugin.StateHealthy)
	p := e.current()
	if running := runtimeGoroutines(); running < before+4 {
		t.Fatalf("%d runtime goroutines running, from %d: the count does not see the pollers and the watcher", running, before)
	}

	start := time.Now()
	e.sup.Shutdown(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v", d)
	}
	if err := p.Stop(context.Background()); err != nil { // a second Stop is a no-op
		t.Errorf("second Stop: %v", err)
	}
	// The goroutines have returned (a goroutine leaves a moment after its WaitGroup is done).
	deadline := time.Now().Add(2 * time.Second)
	for runtimeGoroutines() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtimeGoroutines(); after > before {
		t.Errorf("%d runtime goroutines still run after Stop, from %d before Start", after, before)
	}
	requests := pve.total()
	time.Sleep(150 * time.Millisecond) // many poll intervals at the test timing
	if after := pve.total(); after != requests {
		t.Errorf("%d requests reached the fake after Stop", after-requests)
	}
}
