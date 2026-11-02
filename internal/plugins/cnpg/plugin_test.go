package cnpg

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The manifest requires kubernetes, uses prometheus and declares the one topic; the plugin
// switches itself on (no Configured) and says what to set.
func TestManifest(t *testing.T) {
	m := New().Manifest()
	if m.ID != "cnpg" || !slices.Equal(m.Requires, []plugin.ID{"kubernetes"}) || !slices.Equal(m.Uses, []plugin.ID{"prometheus"}) {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Topics) != 1 || m.Topics[0].Name != "clusters" {
		t.Errorf("topics = %+v", m.Topics)
	}
	if _, ok := New().(plugin.Configured); ok {
		t.Error("the plugin implements Configured: it is meant to switch itself on")
	}
	if got := New().(plugin.ConfigHinter).ConfigHint(); !strings.HasPrefix(got, "plugins.cnpg.") {
		t.Errorf("hint = %q", got)
	}
	if _, ok := m.Config().(*pluginconf.CNPG); !ok {
		t.Errorf("config type = %T", m.Config())
	}
}

// The age query is the one the storage topic sent, with the configured metric in it.
func TestBackupMetricIsInTheQuery(t *testing.T) {
	if got := ageQuery(pluginconf.CNPGBackupMetric); got != defaultQuery {
		t.Errorf("default query = %q", got)
	}
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.CNPG) { c.BackupMetric = "my_backup_ts" }, prefill: func(r *rig) {
		r.kc.SetObjects(gvr, cnpgCluster("db", "pg", nil, nil))
	}})
	r.waitState(model.PluginHealthy)
	want := `time() - max by (namespace,cluster)(my_backup_ts{role="primary"})`
	asked := r.q.asked()
	if !slices.Contains(asked, want) {
		t.Errorf("%q was never asked; asked %v", want, asked)
	}
	for _, q := range asked {
		if strings.Contains(q, "barman") {
			t.Errorf("the default metric was queried: %q", q)
		}
	}
}

// An empty metric is the default, so a section that never went through the defaults still
// queries the Barman series.
func TestEmptyBackupMetricUsesTheDefault(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.CNPG) { c.BackupMetric = "" }, prefill: func(r *rig) {
		r.kc.SetObjects(gvr, cnpgCluster("db", "pg", nil, nil))
	}})
	r.waitState(model.PluginHealthy)
	if !slices.Contains(r.q.asked(), defaultQuery) {
		t.Errorf("asked %v", r.q.asked())
	}
}

// The metric is put into PromQL: Validate refuses anything that is not a metric name, and the
// row says unconfigured instead of sending the text to Prometheus.
func TestBackupMetricIsValidatedAgainstInjection(t *testing.T) {
	p := New().(*Plugin)
	for _, bad := range []string{`x{role="replica"}`, "a) or (b", "a-b", "x or vector(1)"} {
		c := pluginconf.DefaultCNPG()
		c.BackupMetric = bad
		if err := p.Validate(&c); err == nil {
			t.Errorf("Validate accepted %q", bad)
		}
	}
	good := pluginconf.DefaultCNPG()
	if err := p.Validate(&good); err != nil {
		t.Errorf("defaults: %v", err)
	}
	if err := p.Validate("nope"); err == nil {
		t.Error("Validate accepted another type")
	}

	r := newRig(t, rigOpts{cfg: func(c *pluginconf.CNPG) { c.BackupMetric = `a-b` }})
	r.waitState(model.PluginUnconfigured)
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("%d queries sent with an invalid metric name", n)
	}
}

// Visibility rule (a) on the stop/start path: the topic reaches an open stream after the row that
// shows it. The harness runs the plugin alone, so Requires is dropped and the services are given.
func TestVisibilityRuleA(t *testing.T) {
	q := newFakeQuerier()
	kc := plugintest.NewKubeCluster()
	kc.SetObjects(gvr, cnpgCluster("db", "pg", nil, nil))
	factory := func() plugin.Plugin {
		return withServices{newPlugin(fastTiming()), map[plugin.ID]any{"kubernetes": kc, "prometheus": q}}
	}
	cfg := pluginconf.DefaultCNPG()
	plugintest.AssertTopicsReachOpenStream(t, factory,
		plugin.InstanceConfig{Enabled: new(false), Config: &cfg}, plugin.InstanceConfig{Config: &cfg})
}

// withServices lets the single-plugin harness see the services a real supervisor would give,
// and drops Requires, which the harness cannot satisfy.
type withServices struct {
	*Plugin
	svcs map[plugin.ID]any
}

type serviceHost struct {
	plugin.Host
	svcs map[plugin.ID]any
}

func (h serviceHost) Service(id plugin.ID) (any, bool) { v, ok := h.svcs[id]; return v, ok }

func (w withServices) Manifest() plugin.Manifest {
	m := w.Plugin.Manifest()
	m.Requires = nil
	return m
}

func (w withServices) Start(ctx context.Context, h plugin.Host, cfg any) error {
	return w.Plugin.Start(ctx, serviceHost{h, w.svcs}, cfg)
}

// Stop ends the loops: they are gone when Stop returns, so no request can follow. A poll in
// flight is cut short and reports nothing.
func TestStopEndsEveryGoroutine(t *testing.T) {
	obj := func(r *rig) { r.kc.SetObjects(gvr, cnpgCluster("db", "pg", nil, nil)) }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	r := newRig(t, rigOpts{prefill: obj})
	r.waitState(model.PluginHealthy)
	if r.p.live.Load() == 0 {
		t.Fatal("the plugin runs no goroutine")
	}
	if err := r.p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n := r.p.live.Load(); n != 0 {
		t.Errorf("%d goroutines outlive Stop", n)
	}
	asked := r.q.n.Load()
	r.kc.SetObjects(gvr, cnpgCluster("db", "pg2", nil, nil)) // an event after Stop is nobody's business
	if got := r.q.n.Load(); got != asked {
		t.Errorf("a query followed Stop: %d then %d", asked, got)
	}

	g := newRig(t, rigOpts{gated: true, prefill: obj})
	g.waitState(model.PluginStarting)
	eventually(t, "the poll to start", func() bool { return g.q.n.Load() > 0 })
	if err := g.p.Stop(ctx); err != nil {
		t.Fatalf("Stop with a poll in flight: %v", err)
	}
	if n := g.p.live.Load(); n != 0 {
		t.Errorf("%d goroutines outlive a Stop during a poll", n)
	}
	if g.published() {
		t.Error("an interrupted poll published")
	}
}
