package rustfs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// rustfs is opt-in: Configured is true only for enabled: true, so the supervisor leaves an unset
// or false section disabled (never unconfigured) and runs a true one.
func TestRustFSIsOptIn(t *testing.T) {
	p := New().(*Plugin)
	for name, tc := range map[string]struct {
		cfg  any
		want bool
	}{
		"unset":         {pluginconf.DefaultRustFS(), false},
		"false":         {pluginconf.RustFS{PluginBase: pluginconf.PluginBase{Enabled: new(false)}}, false},
		"true":          {pluginconf.RustFS{PluginBase: pluginconf.PluginBase{Enabled: new(true)}}, true},
		"true, pointer": {&pluginconf.RustFS{PluginBase: pluginconf.PluginBase{Enabled: new(true)}}, true},
		"wrong type":    {"nope", false},
	} {
		if got := p.Configured(tc.cfg); got != tc.want {
			t.Errorf("%s: Configured = %v, want %v", name, got, tc.want)
		}
	}

	for name, o := range map[string]rigOpts{"unset": {unset: true}, "false": {enabled: new(false)}} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, o)
			pi := r.waitState(model.PluginDisabled, model.PluginUnconfigured)
			if pi.State != model.PluginDisabled {
				t.Fatalf("state = %s, want disabled: the plugin is never unconfigured", pi.State)
			}
			// Unset says what to make it; an explicit false is the operator's own word.
			if name == "unset" && !strings.Contains(pi.Message, "plugins.rustfs.enabled") {
				t.Errorf("message = %q, want the setting to make", pi.Message)
			}
			if n := r.q.n.Load(); n != 0 {
				t.Errorf("a disabled plugin sent %d queries", n)
			}
			if _, ok := r.hub.Latest("", "rustfs.usage"); ok {
				t.Error("a disabled plugin published")
			}
		})
	}

	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	r.waitState(model.PluginHealthy)
}

// The row says what to set, and names no URL or secret.
func TestRustFSConfigHint(t *testing.T) {
	if got := New().(plugin.ConfigHinter).ConfigHint(); got != "plugins.rustfs.enabled (set true)" {
		t.Errorf("hint = %q", got)
	}
}

// Without the prometheus plugin there is nothing to read: the row is blocked, nothing is
// published, and the plugin starts when prometheus does.
func TestRustFSBlockedWithoutPrometheus(t *testing.T) {
	r := newRig(t, rigOpts{noProm: true, prefill: (*fakeQuerier).fill})
	pi := r.waitState(model.PluginBlocked)
	if !strings.Contains(pi.Message, "prometheus") {
		t.Errorf("message = %q", pi.Message)
	}
	if _, ok := r.hub.Latest("", "rustfs.usage"); ok {
		t.Error("a blocked plugin published")
	}
	r.switchOn()
	r.waitState(model.PluginHealthy)
	d, _ := r.usage()
	if len(d.Buckets) != 3 {
		t.Errorf("buckets = %+v", d.Buckets)
	}
}

// The row is starting, with nothing published, until the first poll has ended; the topic and
// the healthy row follow it.
func TestRustFSStartingUntilTheFirstPollEnds(t *testing.T) {
	r := newRig(t, rigOpts{gated: true, prefill: (*fakeQuerier).fill})
	pi := r.waitState(model.PluginStarting)
	if pi.Message == "" {
		t.Error("a starting row with no message")
	}
	if _, ok := r.hub.Latest("", "rustfs.usage"); ok {
		t.Error("published before the first poll ended")
	}
	if c, ok := r.contributor(); ok && c.Attention != nil && c.Attention.Attention(time.Now()) != nil {
		t.Error("attention is not nil before the first poll")
	}
	close(r.q.gate)
	r.waitState(model.PluginHealthy)
	d, srcs := r.usage()
	if len(d.Buckets) != 3 || d.Capacity.UsedBytes != 3e12 || d.Capacity.UsableBytes != 8e12 {
		t.Errorf("usage = %+v", d)
	}
	if !hasSource(srcs, "prometheus", true, "") || len(srcs) != 1 {
		t.Errorf("sources = %+v", srcs)
	}
}

// One source: a failing poll is error, never degraded, however many of the four queries fail. The
// topic is still published (empty lists, never null) with the source's category, and the row
// recovers when the poll does.
func TestRustFSErrorWhenThePollFails(t *testing.T) {
	for name, fail := range map[string]func(q *fakeQuerier){
		"every query": func(q *fakeQuerier) { q.setFail(errors.New("dial tcp 192.0.2.7:9090: refused")) },
		"one query":   func(q *fakeQuerier) { q.setErr(exprCapacityUsable, errors.New("dial tcp 192.0.2.7:9090: refused")) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, rigOpts{prefill: func(q *fakeQuerier) { q.fill(); fail(q) }})
			pi := r.waitState(model.PluginError, model.PluginDegraded, model.PluginHealthy)
			if pi.State != model.PluginError {
				t.Fatalf("state = %s, want error", pi.State)
			}
			if pi.Message != "prometheus: unreachable" {
				t.Errorf("message = %q", pi.Message)
			}
			d, srcs := r.usage()
			if d.Buckets == nil {
				t.Error("buckets are null on the wire")
			}
			if !hasSource(srcs, "prometheus", false, "unreachable") || len(srcs) != 1 {
				t.Errorf("sources = %+v", srcs)
			}
			for _, body := range []string{pi.Message, string(mustLatest(t, r, "rustfs.usage"))} {
				if strings.Contains(body, "192.0.2.7") {
					t.Errorf("the upstream's address reached a viewer: %s", body)
				}
			}

			// Recovery: the row turns healthy and the buckets come.
			r.q.setFail(nil)
			r.q.mu.Lock()
			r.q.errs = map[string]error{}
			r.q.mu.Unlock()
			r.waitState(model.PluginHealthy)
			waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) == 3 })
		})
	}
}

func mustLatest(t *testing.T, r *rig, topic string) []byte {
	t.Helper()
	ev, ok := r.hub.Latest("", model.Topic(topic))
	if !ok {
		t.Fatalf("%s has no snapshot", topic)
	}
	return ev.Data
}

// A query that fails keeps the last good value of what it reads; the others take the new.
func TestRustFSFailedQueryKeepsItsOldValue(t *testing.T) {
	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) == 3 })
	r.q.setErr(exprBucketUsed, errors.New("boom"))
	r.q.set(exprCapacityUsed, sample(4e12))
	d, _ := waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return d.Capacity.UsedBytes == 4e12 })
	if len(d.Buckets) != 3 || d.Buckets[0].UsedBytes != 95 {
		t.Errorf("the failed query lost its old values: %+v", d.Buckets)
	}
}

// An answer with no samples replaces the old values (a bucket that is gone is gone).
func TestRustFSEmptyAnswerClearsTheBuckets(t *testing.T) {
	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) == 3 })
	r.q.set(exprBucketUsed)
	r.q.set(exprBucketQuota)
	d, _ := waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) == 0 })
	if d.Buckets == nil {
		t.Error("an empty bucket list is null on the wire")
	}
}

// The default queries are the ones the storage topic always sent.
func TestRustFSDefaultQueriesAreTheOldOnes(t *testing.T) {
	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	r.waitState(model.PluginHealthy)
	asked := r.q.asked()
	for _, want := range []string{exprBucketUsed, exprBucketQuota, exprCapacityUsed, exprCapacityUsable} {
		if !slices.Contains(asked, want) {
			t.Errorf("%q was never asked; asked %v", want, asked)
		}
	}
}

// Each configured metric name is the one in its query, and the default is not sent.
func TestRustFSMetricNamesAreInTheQueries(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.RustFS) {
		c.Metrics = pluginconf.RustFSMetrics{BucketUsed: "s3_used", BucketQuota: "s3_quota", CapacityUsed: "s3_cap_used", CapacityUsable: "s3_cap_usable"}
	}})
	r.waitState(model.PluginHealthy)
	asked := r.q.asked()
	for _, want := range []string{
		`max by (bucket)(s3_used)`, `max by (bucket)(s3_quota)`, `max(s3_cap_used)`, `max(s3_cap_usable)`,
	} {
		if !slices.Contains(asked, want) {
			t.Errorf("%q was never asked; asked %v", want, asked)
		}
	}
	for _, q := range asked {
		if strings.Contains(q, "rustfs_") {
			t.Errorf("the default metric was queried: %q", q)
		}
	}
}

// An empty name is the default: a section that never went through the defaults still works.
func TestRustFSEmptyMetricNamesUseTheDefaults(t *testing.T) {
	r := newRig(t, rigOpts{cfg: func(c *pluginconf.RustFS) { c.Metrics = pluginconf.RustFSMetrics{} }})
	r.waitState(model.PluginHealthy)
	if !slices.Contains(r.q.asked(), exprBucketUsed) {
		t.Errorf("asked %v", r.q.asked())
	}
}

// A metric name that is not a metric name is refused by Validate, and the row says
// unconfigured instead of sending the text to Prometheus.
func TestRustFSMetricNamesAreValidated(t *testing.T) {
	p := New().(*Plugin)
	bad := pluginconf.DefaultRustFS()
	bad.Metrics.BucketUsed = `x) or vector(1`
	if err := p.Validate(&bad); err == nil {
		t.Error("Validate accepted an injected metric name")
	}
	good := pluginconf.DefaultRustFS()
	if err := p.Validate(&good); err != nil {
		t.Errorf("defaults: %v", err)
	}
	if err := p.Validate("nope"); err == nil {
		t.Error("Validate accepted another type")
	}

	r := newRig(t, rigOpts{cfg: func(c *pluginconf.RustFS) { c.Metrics.CapacityUsed = `a-b` }})
	r.waitState(model.PluginUnconfigured)
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("%d queries sent with an invalid metric name", n)
	}
}

// The upstream's detail goes to the log scrubbed, the category to the viewers.
func TestRustFSFailingPrometheusLeaksNothing(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, canary, "[scrubbed]") }
	r := newRig(t, rigOpts{scrub: scrub, prefill: func(q *fakeQuerier) {
		q.setFail(errors.New("get https://prom.internal:9090/api?token=" + canary + ": connection refused"))
	}})
	r.waitState(model.PluginError)
	eventually(t, "the failure to be logged", func() bool { return strings.Contains(r.logs.String(), "upstream source failed") })

	bodies := []string{string(mustLatest(t, r, "rustfs.usage")), string(mustLatest(t, r, string(model.TopicPlugins)))}
	for _, pi := range r.sup.Infos() {
		bodies = append(bodies, pi.Message)
	}
	for _, b := range bodies {
		for _, secret := range []string{canary, "prom.internal", "token="} {
			if strings.Contains(b, secret) {
				t.Errorf("%q reached a viewer: %s", secret, b)
			}
		}
	}
	logs := r.logs.String()
	if strings.Contains(logs, canary) {
		t.Errorf("the secret reached the log unscrubbed:\n%s", logs)
	}
	if !strings.Contains(logs, "[scrubbed]") || !strings.Contains(logs, "category=unreachable") {
		t.Errorf("the log lacks the scrubbed detail and the category:\n%s", logs)
	}
}

// Every bucket has a ref of the plugin, and no two share one.
func TestRustFSRefs(t *testing.T) {
	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	d, _ := waitTopic(t, r.hub, "rustfs.usage", func(d model.RustFSUsageData) bool { return len(d.Buckets) == 3 })
	plugintest.AssertRefs(t, r.sup, ID, d, model.Bucket{})
	for _, b := range d.Buckets {
		if want := "rustfs:Bucket/" + b.Name; b.Ref != want {
			t.Errorf("ref = %q, want %q", b.Ref, want)
		}
	}
}

// The row is never hidden while the plugin runs: visibility needs only the stop/start path,
// which rule (a) covers. The topic reaches an open stream after the row that shows it.
func TestRustFSVisibilityRuleA(t *testing.T) {
	q := newFakeQuerier()
	q.fill()
	factory := func() plugin.Plugin { return withServices{newPlugin(fastTiming()), map[plugin.ID]any{"prometheus": q}} }
	before := pluginconf.DefaultRustFS() // enabled unset: disabled
	after := pluginconf.DefaultRustFS()
	after.Enabled = new(true)
	plugintest.AssertTopicsReachOpenStream(t, factory, plugin.InstanceConfig{Config: &before}, plugin.InstanceConfig{Enabled: new(true), Config: &after})
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

// Stop ends the poller: it is gone when Stop returns, so no query can follow.
func TestStopEndsEveryGoroutine(t *testing.T) {
	r := newRig(t, rigOpts{prefill: (*fakeQuerier).fill})
	r.waitState(model.PluginHealthy)
	p := r.p
	if p.live.Load() == 0 {
		t.Fatal("the plugin runs no goroutine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if n := p.live.Load(); n != 0 {
		t.Errorf("%d goroutines outlive Stop", n)
	}
	// A poll in flight is cut short by Stop and reports nothing.
	g := newRig(t, rigOpts{gated: true, prefill: (*fakeQuerier).fill})
	g.waitState(model.PluginStarting)
	gp := g.p
	if err := gp.Stop(ctx); err != nil {
		t.Fatalf("Stop with a poll in flight: %v", err)
	}
	if n := gp.live.Load(); n != 0 {
		t.Errorf("%d goroutines outlive a Stop during a poll", n)
	}
	if _, ok := g.hub.Latest("", "rustfs.usage"); ok {
		t.Error("an interrupted poll published")
	}
}
