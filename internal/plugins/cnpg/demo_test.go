package cnpg

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// In demo mode the twin runs in place of the plugin, with nothing set, publishes the cluster of
// the fake world with its ref and contacts no upstream: not the querier, not the cluster.
func TestDemoRunsTheTwinAndContactsNoUpstream(t *testing.T) {
	r := newRig(t, rigOpts{demo: true, link: func(req plugin.LinkRequest) []model.Link {
		return []model.Link{{Label: "Headlamp", Href: "https://headlamp.example.com/" + req.Namespace + "/" + req.Name}}
	}})
	d, srcs := waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) > 0 })
	if len(d.Clusters) != 1 || d.Clusters[0].Name != "acme-db" || d.Clusters[0].ReadyInstances != d.Clusters[0].Instances {
		t.Errorf("clusters = %+v", d.Clusters)
	}
	if d.Clusters[0].Ref != "cnpg:Cluster/acme/acme-db" || d.Clusters[0].LastBackupAgeSeconds == nil || len(d.Clusters[0].Links) != 1 {
		t.Errorf("cluster = %+v", d.Clusters[0])
	}
	if !hasSource(srcs, "kubernetes", true, "") || !hasSource(srcs, "prometheus", true, "") {
		t.Errorf("sources = %+v", srcs)
	}
	plugintest.AssertRefs(t, r.sup, ID, d, model.CNPGCluster{})
	if st := r.waitState(model.PluginHealthy); st.Message != "" || len(st.Actions) != 0 {
		t.Errorf("row = %+v", st)
	}
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("the twin sent %d queries to the querier", n)
	}
	if w := r.kc.Watched(); len(w) != 0 {
		t.Errorf("the twin watched %v", w)
	}
}

// The twin's manifest is the plugin's, and its ticker ends with Stop.
func TestDemoManifestMatchesThePluginAndStopEndsTheTicker(t *testing.T) {
	var p plugin.Plugin = New()
	tw := p.(plugin.Demo).Demo()
	a, b := p.Manifest(), tw.Manifest()
	if a.ID != b.ID || a.Name != b.Name || len(a.Topics) != len(b.Topics) || len(a.Requires) != len(b.Requires) || len(a.Uses) != len(b.Uses) {
		t.Errorf("manifests differ: %+v vs %+v", a, b)
	}
	if err := tw.Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}

	h := &stubHost{}
	if err := tw.Start(context.Background(), h, nil); err != nil {
		t.Fatal(err)
	}
	if h.pubs.Load() != 1 {
		t.Errorf("Start published %d times, want once", h.pubs.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tw.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	// Stop returns when the twin's function has ended, which is before this host has counted the
	// goroutine out: the count settles.
	eventually(t, "the twin's goroutine to end after Stop", func() bool { return h.live.Load() == 0 })
}

// stubHost runs the twin without a supervisor: it counts publishes and the goroutines it started.
type stubHost struct {
	plugin.Host
	pubs, live atomic.Int32
}

func (h *stubHost) Publish(string, any, []model.SourceStatus) { h.pubs.Add(1) }
func (h *stubHost) Now() time.Time                            { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
func (h *stubHost) Links() plugin.LinkResolver                { return plugin.NoLinks }
func (h *stubHost) IconBaseURL() string                       { return "" }
func (h *stubHost) Go(_ string, fn func()) {
	h.live.Add(1)
	go func() { defer h.live.Add(-1); fn() }()
}
