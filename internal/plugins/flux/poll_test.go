package flux

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
)

// stubHost is a plugin.Host for the tests that run the plugin without a supervisor. It counts the
// goroutines started through Go that have not ended, and the lookups of the kubernetes service.
type stubHost struct {
	mu        sync.Mutex
	cluster   svc.Cluster
	live      atomic.Int32
	lookups   atomic.Int64
	published atomic.Int32
}

func (h *stubHost) setCluster(c svc.Cluster) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cluster = c
}

func (h *stubHost) Publish(string, any, []model.SourceStatus) { h.published.Add(1) }
func (h *stubHost) Logger() *slog.Logger                      { return slog.New(slog.DiscardHandler) }
func (h *stubHost) HTTPClient(plugin.HTTPConfig) (*http.Client, error) {
	return nil, errors.New("flux makes no request of its own")
}
func (h *stubHost) ScrubLog(s string) string { return s }
func (h *stubHost) CutLog(s string, n int) (string, bool) {
	return (*secret.Registry)(nil).CutRunes(s, n)
}
func (h *stubHost) Links() plugin.LinkResolver { return plugin.NoLinks }
func (h *stubHost) IconBaseURL() string        { return "" }
func (h *stubHost) Now() time.Time             { return t0 }
func (h *stubHost) Service(id plugin.ID) (any, bool) {
	h.lookups.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if id != "kubernetes" || h.cluster == nil {
		return nil, false
	}
	return h.cluster, true
}
func (h *stubHost) Go(_ string, fn func()) {
	h.live.Add(1)
	go func() {
		defer h.live.Add(-1)
		fn()
	}()
}

// wantIdle fails unless every goroutine started through Go has ended. A plugin's Stop returns
// when its own function has (its WaitGroup, its done channel), which is before the wrapper above
// has counted the goroutine out, so the count settles instead of being read once.
func (h *stubHost) wantIdle(t *testing.T, when string) {
	t.Helper()
	eventually(t, "the goroutines started through Go to end "+when, func() bool { return h.live.Load() == 0 })
}

// Stop returns when the round has ended, and nothing touches the cluster afterwards.
func TestStopEndsEveryGoroutine(t *testing.T) {
	kc := plugintest.NewKubeCluster()
	h := &stubHost{cluster: kc}
	p := &Plugin{t: timing{tick: time.Millisecond}}
	if err := p.Start(context.Background(), h, cfgPtr()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first publish", func() bool { return h.published.Load() > 0 })
	if h.live.Load() != 1 {
		t.Fatalf("%d goroutines running through Go, want the round alone", h.live.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	h.wantIdle(t, "after Stop returned")
	published, lookups := h.published.Load(), h.lookups.Load()
	synced := kc.Watched()
	time.Sleep(25 * time.Millisecond) // 25 rounds of a loop that had leaked
	if h.published.Load() != published || h.lookups.Load() != lookups || len(kc.Watched()) != len(synced) {
		t.Error("the plugin touched the service or the hub after Stop returned")
	}
	if err := p.Stop(ctx); err != nil {
		t.Errorf("a second Stop = %v", err)
	}
	if err := (&Plugin{}).Stop(ctx); err != nil {
		t.Errorf("Stop before Start = %v", err)
	}
}

// The plugin itself contributes nothing while its row is hidden, so what a hidden plugin last built
// does not reach the overview even where the supervisor's own guard is not in the way: here the
// service is replaced by one whose lists are not complete, and the payload built from the old one
// is a payload the row must not show.
func TestContributionsStopWhileTheRowIsHidden(t *testing.T) {
	kc := plugintest.NewKubeCluster()
	kc.SetObjects(kube.GVRHelmReleases, obj(hr, "media", "app", ready("ok"), suspended()))
	h := &stubHost{cluster: kc}
	p := &Plugin{t: timing{tick: time.Millisecond}}
	if err := p.Start(context.Background(), h, cfgPtr()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	eventually(t, "the contribution", func() bool { return len(p.Attention(t0)) == 1 && len(p.KPIs()) > 0 })
	if st := p.Status(); st.State != plugin.StateHealthy {
		t.Fatalf("status = %+v", st)
	}

	kc2 := plugintest.NewKubeCluster()
	kc2.SetListed(kube.GVRHelmReleases, false)
	h.setCluster(kc2)
	eventually(t, "the hidden wait", func() bool { return p.Status().State == plugin.StateUnavailable })
	if p.Attention(t0) != nil || p.KPIs() != nil {
		t.Errorf("a hidden plugin contributes: %v %v", p.Attention(t0), p.KPIs())
	}
	kc2.SetListed(kube.GVRHelmReleases, true)
	eventually(t, "the rebuilt contribution", func() bool { return p.Status().State == plugin.StateHealthy && p.Attention(t0) != nil })
}

// Start does not fail without a service (a kubernetes in error is not an error of flux): the
// round waits for it, and builds as soon as it appears.
func TestStartDoesNotFailWithoutAService(t *testing.T) {
	h := &stubHost{}
	p := &Plugin{t: timing{tick: time.Millisecond}}
	if err := p.Start(context.Background(), h, cfgPtr()); err != nil {
		t.Fatalf("Start without a service = %v", err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	if st := p.Status(); st.State != plugin.StateStarting || st.Message != "waiting for the kubernetes service" || len(st.Sources) != 0 {
		t.Errorf("status = %+v", st)
	}
	before := h.lookups.Load()
	eventually(t, "rounds without a service", func() bool { return h.lookups.Load() >= before+20 })
	if h.published.Load() != 0 {
		t.Error("published without a service")
	}
	if p.Attention(t0) != nil || p.KPIs() != nil {
		t.Error("an opinion without a service")
	}

	kc := plugintest.NewKubeCluster()
	h.setCluster(kc)
	eventually(t, "the publish after the service appeared", func() bool { return h.published.Load() > 0 })
	if got := kc.Watched(); len(got) != 1 || !slices.Equal(got[0], gvrs) {
		t.Errorf("watched = %v, want the five resources once", got)
	}
}

// Watch is asked once per service: at Start, never again for the same one (Watch is idempotent, but
// a plugin that asks every round would hammer the discovery wake-up), and again for a new one.
func TestFluxWatchesOnceAndAfterEveryKubernetesRestart(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.waitState(model.PluginHealthy)
	eventually(t, "the watch", func() bool { return len(r.kc.Watched()) >= 1 })
	if got := r.kc.Watched(); len(got) != 1 || !slices.Equal(got[0], gvrs) {
		t.Fatalf("watched = %v, want the five resources once", got)
	}

	// Churn: many rounds, many changes.
	for i := range 5 {
		r.setKind(kst, obj(kst, "gitops", "k"+string(rune('a'+i)), ready("ok")))
	}
	r.waitPublished("the last change", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 && d.Kustomizations[0].Name == "ke" })
	before := r.kube.calls.Load()
	eventually(t, "more rounds", func() bool { return r.kube.calls.Load() >= before+20 })
	if got := r.kc.Watched(); len(got) != 1 {
		t.Fatalf("watched %d times for one service", len(got))
	}

	// The kubernetes plugin restarted: its new service has no registrations.
	kc2 := plugintest.NewKubeCluster()
	kc2.SetObjects(kube.GVRHelmReleases, obj(hr, "media", "after", ready("ok")))
	r.kube.setService(kc2)
	r.waitPublished("the new service's objects", func(d model.FluxData) bool { return len(d.HelmReleases) == 1 })
	eventually(t, "the new service to be watched", func() bool { return len(kc2.Watched()) == 1 })
	if got := kc2.Watched(); !slices.Equal(got[0], gvrs) {
		t.Errorf("watched = %v", got)
	}

	// It goes away and the same service returns: nothing more to ask.
	r.kube.setService(nil)
	r.waitState(model.PluginStarting)
	r.kube.setService(kc2)
	r.waitState(model.PluginHealthy)
	before = r.kube.calls.Load()
	eventually(t, "more rounds", func() bool { return r.kube.calls.Load() >= before+20 })
	if got := kc2.Watched(); len(got) != 1 {
		t.Errorf("watched %d times for one service across its absence", len(got))
	}
}

// With kubernetes in error there is no service: the row waits, nothing is published, the plugin
// contributes nothing and has not failed to start; recovery is the service coming back.
func TestFluxWithKubernetesInError(t *testing.T) {
	r := newRig(t, rigOpts{noService: true, kubeState: plugin.StateError})
	row := r.waitState(model.PluginStarting)
	if row.Message != "waiting for the kubernetes service" {
		t.Fatalf("row = %+v", row)
	}
	if !plugin.Visible(row.State) {
		t.Error("the row is hidden: it is visible, and claims nothing else")
	}
	before := r.kube.calls.Load()
	eventually(t, "rounds without a service", func() bool { return r.kube.calls.Load() >= before+20 })
	noPayload(t, r)
	if _, ok := r.contributor(); !ok {
		t.Fatal("a running flux is a contributor, with no opinion")
	}
	if r.attention() != nil || r.kpis() != nil || len(r.rowSources()) != 0 {
		t.Errorf("flux contributes without a service: %v %v %v", r.attention(), r.kpis(), r.rowSources())
	}
	if r.kc.Watched() != nil && len(r.kc.Watched()) != 0 {
		t.Error("watched a cluster it was never given")
	}

	// Kubernetes recovers.
	r.setKind(hr, obj(hr, "media", "app", ready("ok"), suspended()))
	r.kube.setState(plugin.StateHealthy)
	r.kube.setService(r.kc)
	r.waitState(model.PluginHealthy)
	r.waitPublished("the objects", func(d model.FluxData) bool { return len(d.HelmReleases) == 1 })
	eventually(t, "the contribution", func() bool { return len(r.attention()) == 1 })

	// And fails again: its last contribution describes a cluster nobody can reach.
	r.kube.setState(plugin.StateError)
	r.kube.setService(nil)
	r.waitState(model.PluginStarting)
	eventually(t, "the contribution to stop", func() bool { return r.attention() == nil && r.kpis() == nil })
}

// An API-server outage of 10 s or more makes flux blocked, and a suspended object keeps the time it
// was first seen: blocked is not a restart of the clock (F1).
func TestSuspendedSinceSurvivesBlocked(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.setKind(hr, obj(hr, "media", "app", ready("ok"), suspended()))
	}})
	var first time.Time
	eventually(t, "the suspended item", func() bool {
		items := r.attention()
		if len(items) != 1 {
			return false
		}
		first = items[0].Since
		return true
	})
	if !first.Equal(t0) {
		t.Fatalf("first sighting = %v, want %v", first, t0)
	}

	r.clk.advance(time.Hour)
	r.kube.setState(plugin.StateUnavailable)
	r.waitState(model.PluginBlocked)
	if _, ok := r.contributor(); ok {
		t.Error("a blocked flux contributes")
	}
	r.clk.advance(time.Hour)
	r.kube.setState(plugin.StateHealthy)
	r.waitState(model.PluginHealthy)
	var again []model.AttentionItem
	eventually(t, "the item after the outage", func() bool { again = r.attention(); return len(again) == 1 })
	if !again[0].Since.Equal(t0) {
		t.Errorf("since = %v after a blocked period, want the first sighting %v", again[0].Since, t0)
	}
}

// rowVisibleIn reports whether the core.plugins envelope body shows flux's row in a visible state.
func rowVisibleIn(t *testing.T, body []byte) bool {
	t.Helper()
	var env model.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	var data model.PluginsData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	for _, pi := range data.Plugins {
		if pi.ID == string(ID) {
			return plugin.Visible(pi.State)
		}
	}
	return false
}

// awaitVisibleThenPayload reads the open stream until flux's row has turned visible and its topic
// has reached the stream after that, and fails on any flux.objects event on the way that carries
// no object: the stale or emptied payload of a hidden row must never be what a visible one shows.
func awaitVisibleThenPayload(t *testing.T, ch <-chan hub.Event) {
	t.Helper()
	visible, delivered := false, false
	deadline := time.After(10 * time.Second)
	for !(visible && delivered) {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("the subscriber was dropped")
			}
			switch ev.Topic {
			case string(model.TopicPlugins):
				visible = visible || rowVisibleIn(t, ev.Data)
			case string(topic):
				var env model.Envelope
				if err := json.Unmarshal(ev.Data, &env); err != nil {
					t.Fatal(err)
				}
				var d model.FluxData
				if err := json.Unmarshal(env.Data, &d); err != nil {
					t.Fatal(err)
				}
				if len(d.Sources)+len(d.Kustomizations)+len(d.HelmReleases) == 0 {
					t.Fatalf("an empty flux.objects reached the open stream (row visible: %v)", visible)
				}
				delivered = delivered || visible
			}
		case <-deadline:
			t.Fatalf("visible = %v, delivered after it = %v", visible, delivered)
		}
	}
}

// Visibility rule (a), the stop/start path: flux is blocked, kubernetes comes up, and the topic
// reaches an open stream after the row turns visible.
func TestFluxTopicsReachAnOpenStreamAfterTheRowTurnsVisible(t *testing.T) {
	r := newRig(t, rigOpts{noKube: true, prefill: func(r *rig) {
		r.setKind(kst, obj(kst, "gitops", "platform", ready("ok")))
	}})
	r.waitState(model.PluginBlocked)
	_, ch, cancel := r.hub.Subscribe("", nil)
	defer cancel()
	r.set.Set(plugin.InstanceConfig{Name: "kubernetes", Type: "kubernetes"})
	r.reload()
	awaitVisibleThenPayload(t, ch)
}

// The CRDs of a cluster that had no Flux come back while flux runs: the row stays hidden until the
// rebuilt payload is published, so the resend that follows the visible row is the fresh one, never
// the empty payload flux published on the way into unavailable.
func TestFluxRowStaysHiddenUntilBuiltForTheReturningCRDs(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) { serveOnly(r) }})
	r.waitState(model.PluginUnavailable)
	if d := r.waitPublished("the empty payload", func(model.FluxData) bool { return true }); len(d.Kustomizations) != 0 {
		t.Fatalf("payload = %+v", d)
	}
	_, ch, cancel := r.hub.Subscribe("", nil)
	defer cancel()

	// The CRDs are served again but the lists have not completed: nothing built yet.
	for _, g := range gvrs {
		r.kc.SetListed(g, false)
		r.kc.SetServed(g, true)
	}
	eventually(t, "the hidden wait", func() bool {
		row := r.row()
		return row.State == model.PluginUnavailable && row.Message == "reading the flux resources"
	})
	// While the row is visible, the payload it shows is built from objects.
	r.setKind(kst, obj(kst, "gitops", "platform", ready("ok")))
	for _, g := range gvrs {
		r.kc.SetListed(g, true)
	}
	eventually(t, "the row to turn visible", func() bool {
		if row := r.row(); plugin.Visible(row.State) {
			d, _, ok := r.published()
			if !ok || len(d.Kustomizations) != 1 {
				t.Fatalf("the row is %s but the topic holds %+v", row.State, d)
			}
			return true
		}
		return false
	})
	awaitVisibleThenPayload(t, ch)
	if r.row().State != model.PluginHealthy {
		t.Errorf("row = %+v", r.row())
	}
}

// listsOnRead is a cluster whose list of one resource completes between two reads of it: the
// read that finds it pending flips it, so the next read says listed.
type listsOnRead struct {
	*plugintest.KubeCluster
	gvr   schema.GroupVersionResource
	armed bool
}

func (c *listsOnRead) Listed(g schema.GroupVersionResource) bool {
	was := c.KubeCluster.Listed(g)
	if g == c.gvr && c.armed && !was {
		c.armed = false
		c.KubeCluster.SetListed(g, true)
	}
	return was
}

// A list that completes while a round is reading must not be recorded as seen without having been
// acted on: the round decided on "pending", so the signature it compares next time says
// "pending" too, and the following round publishes.
func TestRoundDoesNotRecordAFlipItDidNotActOn(t *testing.T) {
	kc := plugintest.NewKubeCluster()
	kc.SetListed(kube.GVRHelmReleases, false)
	h := &stubHost{cluster: &listsOnRead{KubeCluster: kc, gvr: kube.GVRHelmReleases, armed: true}}
	r := &runtime{h: h, cfg: *cfgPtr(), t: fastTiming(), ctx: context.Background()}
	r.round()
	if h.published.Load() != 0 {
		t.Fatal("published while a resource was still being listed")
	}
	r.round()
	if h.published.Load() != 1 {
		t.Errorf("published %d times after the list completed, want 1", h.published.Load())
	}
}
