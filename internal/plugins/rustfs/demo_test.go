package rustfs

import (
	"context"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// In demo mode the twin runs in place of the plugin, enabled and configured with nothing set,
// publishes the buckets of the fake world and contacts no upstream.
func TestDemoRunsTheTwinAndContactsNoUpstream(t *testing.T) {
	r := newRig(t, rigOpts{demo: true, unset: true})
	d, srcs := waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) > 0 })

	want := world.New(time.Now, time.Now(), noLinks{}).RustFS()
	if len(d.Buckets) != len(want.Buckets) || d.Capacity != want.Capacity {
		t.Errorf("usage = %+v, want the fake world's %+v", d, want)
	}
	if !hasSource(srcs, "prometheus", true, "") {
		t.Errorf("sources = %+v", srcs)
	}
	plugintest.AssertRefs(t, r.sup, ID, d, model.Bucket{})
	for _, b := range d.Buckets {
		if b.Ref != "rustfs:Bucket/"+b.Name {
			t.Errorf("ref = %q for %q", b.Ref, b.Name)
		}
	}
	if st := r.waitState(model.PluginHealthy); st.Message != "" || len(st.Actions) != 0 {
		t.Errorf("row = %+v", st)
	}
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("the twin sent %d queries to the querier", n)
	}

	// It contributes attention with the plugin's own rule, never nil.
	c, ok := r.contributor()
	if !ok {
		t.Fatal("the twin is not a contributor")
	}
	plugintest.AssertAttentionNonNil(t, c)
}

// The twin's manifest is the plugin's, and its goroutines (none) end with Stop.
func TestDemoManifestMatchesThePlugin(t *testing.T) {
	var p plugin.Plugin = New()
	twin := p.(plugin.Demo).Demo()
	a, b := p.Manifest(), twin.Manifest()
	if a.ID != b.ID || a.Name != b.Name || len(a.Topics) != len(b.Topics) || len(a.Requires) != len(b.Requires) {
		t.Errorf("manifests differ: %+v vs %+v", a, b)
	}
	if err := twin.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}

// The twin raises the plugin's items for the buckets it shows: with a world whose buckets are
// below their quotas, none.
func TestDemoAttentionAppliesThePluginsRule(t *testing.T) {
	tw := &twin{}
	if tw.Attention(time.Now()) != nil {
		t.Error("attention before Start is not nil")
	}
	tw.data, tw.done = model.RustFSUsageData{Buckets: []model.Bucket{{Ref: "rustfs:Bucket/x", Name: "x", UsedBytes: 95, QuotaBytes: 100}}}, true
	if got := tw.Attention(time.Now()); len(got) != 1 || got[0].ID != "bucket:x" {
		t.Errorf("items = %+v", got)
	}
}
