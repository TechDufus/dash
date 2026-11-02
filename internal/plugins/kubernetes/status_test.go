package kubernetes

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// tap is a subscriber of the real hub that records what it is sent.
type tap struct {
	mu     sync.Mutex
	events []tapEvent
	cancel func()
}

type tapEvent struct {
	topic string
	body  []byte
}

func (e *env) tap() *tap {
	_, ch, cancel := e.hub.Subscribe("", nil)
	tp := &tap{cancel: cancel}
	go func() {
		for ev := range ch {
			tp.mu.Lock()
			tp.events = append(tp.events, tapEvent{ev.Topic, ev.Data})
			tp.mu.Unlock()
		}
	}()
	e.t.Cleanup(cancel)
	return tp
}

func (tp *tap) all() []tapEvent {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return append([]tapEvent(nil), tp.events...)
}

// envelopeData is the data of an event body (the marshalled envelope).
func envelopeData(t *testing.T, body []byte) []byte {
	t.Helper()
	var env model.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

// kubeRowState is the state of the kubernetes row in a core.plugins event body.
func kubeRowState(t *testing.T, body []byte) plugin.State {
	t.Helper()
	var env model.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	var d model.PluginsData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		t.Fatal(err)
	}
	for _, pi := range d.Plugins {
		if pi.ID == string(ID) {
			return pi.State
		}
	}
	return ""
}

// probeFailing waits until the plugin has seen the probe fail.
func (e *env) probeFailing() {
	e.t.Helper()
	r := e.runtime()
	waitFor(e.t, "a failed probe", func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return !r.probe.failingSince.IsZero()
	})
}

// probeAnswering waits until the plugin has seen the probe succeed after failures.
func (e *env) probeAnswering() {
	e.t.Helper()
	r := e.runtime()
	waitFor(e.t, "an answered probe", func() bool {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.probe.failingSince.IsZero()
	})
}

func TestNoClusterIsUnavailableNotFatal(t *testing.T) {
	e := newEnv(t, envOpts{connErr: kube.ErrNoCluster})
	pi := e.state(plugin.StateUnavailable)
	if pi.Message != noClusterMsg {
		t.Errorf("message = %q", pi.Message)
	}
	if _, ok := e.sup.Service(ID); ok {
		t.Error("an unavailable plugin exports a service")
	}
	if _, ok := e.hub.Latest("", "kubernetes.cluster"); ok {
		t.Error("a cluster-less plugin published the cluster")
	}
	if got := e.requests.Load(); got != 0 {
		t.Errorf("%d requests reached the API server", got)
	}
}

func TestUnavailableExportsNoService(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true})
	e.healthy()
	if _, ok := e.sup.Service(ID); !ok {
		t.Fatal("a healthy plugin exports no service")
	}
	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(11 * time.Second)
	e.state(plugin.StateUnavailable)
	if v, ok := e.sup.Service(ID); ok || v != nil {
		t.Errorf("service = %v, %v while unavailable", v, ok)
	}
}

func TestExplicitModeWithoutClusterIsError(t *testing.T) {
	e := newEnv(t, envOpts{
		cfg:     func(c *pluginconf.Kubernetes) { c.Mode = pluginconf.KubernetesModeInCluster },
		connErr: errors.New("mode inCluster outside a cluster: /var/run/secrets/token unreadable"),
	})
	pi := e.state(plugin.StateError)
	if !strings.Contains(pi.Message, "plugins.kubernetes.mode inCluster") || strings.Contains(pi.Message, "/var/run") {
		t.Errorf("message = %q: it names the setting and not the detail", pi.Message)
	}
	if !strings.Contains(e.logs.String(), "/var/run/secrets/token") {
		t.Error("the log does not carry the detail")
	}
	if _, ok := e.sup.Service(ID); ok {
		t.Error("an errored plugin exports a service")
	}
}

// The API server must have failed for ten seconds, with no success between, before the row turns
// unavailable; the first answer brings it back at once.
func TestUnavailableAfterTenSecondsOfFailedProbes(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true})
	e.healthy()
	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(9 * time.Second)
	time.Sleep(50 * time.Millisecond) // several status rounds at the same fake time
	if st := e.info().State; st != plugin.StateHealthy {
		t.Fatalf("state after 9 s = %s, want healthy", st)
	}
	e.clock.advance(time.Second)
	pi := e.state(plugin.StateUnavailable)
	if pi.Message != "api server unreachable" {
		t.Errorf("message = %q", pi.Message)
	}
	e.failProbe.Store(false)
	e.state(plugin.StateHealthy)
}

// A failure that ends before the debounce is invisible: no row event with unavailable, and no
// topic is sent again.
func TestKubernetesDebouncesUnavailable(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true})
	e.healthy()
	tp := e.tap()
	time.Sleep(50 * time.Millisecond)
	before := len(tp.all())

	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(9 * time.Second)
	time.Sleep(50 * time.Millisecond)
	e.failProbe.Store(false)
	e.probeAnswering()
	time.Sleep(100 * time.Millisecond)

	for _, ev := range tp.all()[before:] {
		if ev.topic == string(model.TopicPlugins) && kubeRowState(t, ev.body) == plugin.StateUnavailable {
			t.Error("a core.plugins event showed unavailable")
		}
		if strings.HasPrefix(ev.topic, "kubernetes.") {
			t.Errorf("topic %s was sent again after a flap inside the debounce", ev.topic)
		}
	}
}

// TestFlapReversedWithinTenSecondsEmitsNoRowEvent is the same flap seen through a subscriber that
// started before it: nothing about the kubernetes row or topics reaches it.
func TestFlapReversedWithinTenSecondsEmitsNoRowEvent(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true})
	e.healthy()
	tp := e.tap()
	time.Sleep(50 * time.Millisecond)
	n := len(tp.all())
	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(5 * time.Second)
	time.Sleep(30 * time.Millisecond)
	e.failProbe.Store(false)
	e.probeAnswering()
	time.Sleep(100 * time.Millisecond)
	for _, ev := range tp.all()[n:] {
		if strings.HasPrefix(ev.topic, "kubernetes.") || (ev.topic == string(model.TopicPlugins) && kubeRowState(t, ev.body) == plugin.StateUnavailable) {
			t.Errorf("unexpected %s after a flap of five seconds", ev.topic)
		}
	}
}

// The first real flip of rule (a): the API server is unreachable for the debounce, the row
// hides, the server answers again with the informers never having stopped, and the three topics
// reach a subscriber that opened while the row was hidden, after the event that made it visible,
// with their data unchanged.
func TestRecoveryResendsTopicsToAnOpenStream(t *testing.T) {
	e := newEnv(t, envOpts{fakeClock: true, objs: []k8sruntime.Object{node("n1", "192.0.2.11", true), namespace("media", nil)}})
	e.healthy()
	data := map[string][]byte{}
	for _, topic := range []string{"kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"} {
		ev, _ := e.hub.Latest("", model.Topic(topic))
		data[topic] = bytes.Clone(ev.Data)
	}

	e.failProbe.Store(true)
	e.probeFailing()
	e.clock.advance(10 * time.Second)
	e.state(plugin.StateUnavailable)
	tp := e.tap() // opens while the row is hidden
	time.Sleep(50 * time.Millisecond)
	n := len(tp.all())

	e.failProbe.Store(false)
	e.state(plugin.StateHealthy)

	visible := -1
	seen := map[string]bool{}
	waitFor(t, "the topics after the visible row", func() bool {
		evs := tp.all()[n:]
		for i, ev := range evs {
			if visible < 0 && ev.topic == string(model.TopicPlugins) && plugin.Visible(kubeRowState(t, ev.body)) {
				visible = i
			}
			if visible >= 0 && i > visible && strings.HasPrefix(ev.topic, "kubernetes.") {
				seen[ev.topic] = true
				// The data, not the envelope: a poll that runs after the recovery republishes with a
				// new updatedAt and source check times, which is fine; the topic's data must not change.
				if !bytes.Equal(envelopeData(t, ev.body), envelopeData(t, data[ev.topic])) {
					t.Errorf("%s changed across the outage", ev.topic)
				}
			}
		}
		return len(seen) == 3
	})
}

// TestTopicsReachOpenStreamAfterConfigReload is the stop/start path of rule (a): the plugin is
// switched on by a reload.
func TestTopicsReachOpenStreamAfterConfigReload(t *testing.T) {
	e := newEnv(t, envOpts{noRun: true})
	off := false
	cfg := pluginconf.DefaultKubernetes()
	plugintest.AssertTopicsReachOpenStream(t,
		func() plugin.Plugin { return &Plugin{t: fastTiming(), connect: e.connect} },
		plugin.InstanceConfig{Enabled: &off, Config: &cfg},
		plugin.InstanceConfig{Config: &cfg},
	)
}

// The real constant, on the real clock: ten seconds of failed probes. It is the slow test.
func TestUnavailableDebounceConstantIsTenSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ten seconds")
	}
	if got := defaultTiming().unavailableAfter; got != 10*time.Second {
		t.Fatalf("unavailableAfter = %s", got)
	}
	e := newEnv(t, envOpts{timing: func(tm *timing) { tm.unavailableAfter = defaultTiming().unavailableAfter }})
	e.healthy()
	start := time.Now()
	e.failProbe.Store(true)
	e.probeFailing()
	time.Sleep(5 * time.Second)
	if st := e.info().State; st != plugin.StateHealthy {
		t.Fatalf("state after five seconds = %s", st)
	}
	deadline := time.Now().Add(15 * time.Second)
	for e.info().State != plugin.StateUnavailable {
		if time.Now().After(deadline) {
			t.Fatal("never unavailable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if time.Since(start) < 9*time.Second {
		t.Errorf("unavailable after %s", time.Since(start))
	}
}

// Typed informers that cannot sync (a forbidden list) keep the row starting for sixty seconds on
// the plugin's clock, then an error with the category of the refusal and no service.
func TestTypedSyncTimeoutIsErrorWithCategoryAndNoService(t *testing.T) {
	var forbid atomicBool
	forbid.set(true)
	e := newEnv(t, envOpts{fakeClock: true, prefill: func(e *env) {
		e.kc.PrependReactor("list", "pods", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
			if forbid.get() {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no"))
			}
			return false, nil, nil
		})
	}})
	r := e.runtime()
	waitFor(t, "the watch error", func() bool { return r.cl.SyncError() != nil })
	if st := e.info().State; st != plugin.StateStarting {
		t.Fatalf("state = %s, want starting", st)
	}
	if _, ok := e.sup.Service(ID); !ok {
		t.Error("a starting plugin must export its (unsynced) service")
	}
	e.clock.advance(60 * time.Second)
	pi := e.state(plugin.StateError)
	if !strings.Contains(pi.Message, "auth") {
		t.Errorf("message = %q, want the category auth", pi.Message)
	}
	if _, ok := e.sup.Service(ID); ok {
		t.Error("an errored plugin exports a service")
	}
	// It leaves the error by itself when the sync completes.
	forbid.set(false)
	e.state(plugin.StateHealthy)
	if _, ok := e.sup.Service(ID); !ok {
		t.Error("the recovered plugin exports no service")
	}
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

// A cluster with none of the CRDs is degraded, not stuck starting, and says which groups are
// missing; the CNPG group of the transitional storage rows is not one of them.
func TestClusterWithoutAnyCRDIsDegradedNotStarting(t *testing.T) {
	e := newEnv(t, envOpts{noCRDs: true})
	pi := e.state(plugin.StateDegraded)
	for _, name := range []string{"cert-manager", "external-secrets", "gateway-api"} {
		if !strings.Contains(pi.Message, name) {
			t.Errorf("message %q does not name %s", pi.Message, name)
		}
	}
	if strings.Contains(pi.Message, "cnpg") {
		t.Errorf("message %q names cnpg: an absent CNPG is not a note", pi.Message)
	}
	for _, topic := range []string{"kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"} {
		waitFor(t, topic, func() bool { _, ok := e.hub.Latest("", model.Topic(topic)); return ok })
	}
	var cl model.ClusterData
	srcs, _ := e.payload("kubernetes.cluster", &cl)
	if cl.Certificates == nil || cl.ExternalSecrets == nil {
		t.Error("the CRD-backed lists are null on the wire")
	}
	notes := noteNames(srcs)
	if !notes["cert-manager"] || !notes["external-secrets"] || notes["gateway-api"] || notes["cnpg"] {
		t.Errorf("cluster notes = %v", notes)
	}
	var ap model.AppsData
	asrcs, _ := e.payload("kubernetes.apps", &ap)
	if !noteNames(asrcs)["gateway-api"] {
		t.Errorf("apps sources = %+v", asrcs)
	}
}

func noteNames(srcs []model.SourceStatus) map[string]bool {
	out := map[string]bool{}
	for _, s := range srcs {
		if !s.OK && s.Error == "not_configured" {
			out[s.Name] = true
		}
	}
	return out
}

func TestDegradedNamesEachMissingGroup(t *testing.T) {
	e := newEnv(t, envOpts{crds: []schema.GroupVersionResource{kube.GVRCertificates}})
	pi := e.state(plugin.StateDegraded)
	if strings.Contains(pi.Message, "cert-manager") || !strings.Contains(pi.Message, "external-secrets") || !strings.Contains(pi.Message, "gateway-api") {
		t.Errorf("message = %q", pi.Message)
	}
}

func TestOptionalFlagsSilenceTheNote(t *testing.T) {
	e := newEnv(t, envOpts{noCRDs: true, cfg: func(c *pluginconf.Kubernetes) {
		c.Optional = pluginconf.KubernetesOptional{Certificates: true, ExternalSecrets: true, HTTPRoutes: true}
	}})
	e.healthy()
	var cl model.ClusterData
	srcs, _ := e.payload("kubernetes.cluster", &cl)
	if notes := noteNames(srcs); len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	// One flag only silences its own group.
	e2 := newEnv(t, envOpts{noCRDs: true, cfg: func(c *pluginconf.Kubernetes) { c.Optional.Certificates = true }})
	pi := e2.state(plugin.StateDegraded)
	if strings.Contains(pi.Message, "cert-manager") || !strings.Contains(pi.Message, "external-secrets") {
		t.Errorf("message = %q", pi.Message)
	}
}

// stubDisc is a discovery client whose answer a test sets.
type stubDisc struct {
	discovery.ServerResourcesInterface
	mu    sync.Mutex
	lists []*metav1.APIResourceList
	err   error
}

func (d *stubDisc) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return nil, d.lists, d.err
}

func (d *stubDisc) set(err error, lists ...*metav1.APIResourceList) {
	d.mu.Lock()
	d.lists, d.err = lists, err
	d.mu.Unlock()
}

func groupFailure(gvs ...schema.GroupVersion) error {
	m := map[schema.GroupVersion]error{}
	for _, gv := range gvs {
		m[gv] = errors.New("503 service unavailable")
	}
	return &discovery.ErrGroupDiscoveryFailed{Groups: m}
}

var allServed = []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters}

// An aggregated API that is down (a metrics-server APIService) is not a group dash reads: it is
// logged at INFO and the row stays healthy.
func TestUnwantedGroupDiscoveryFailureDoesNotDegrade(t *testing.T) {
	d := &stubDisc{}
	d.set(groupFailure(schema.GroupVersion{Group: "metrics.k8s.io", Version: "v1beta1"}), served(allServed...)...)
	e := newEnv(t, envOpts{prefill: func(e *env) { e.discovery = d }})
	e.healthy()
	waitFor(t, "the INFO line", func() bool { return strings.Contains(e.logs.String(), "API groups dash does not read") })
	if got := e.info(); got.State != plugin.StateHealthy || got.Message != "" {
		t.Errorf("row = %+v", got)
	}
}

func TestWantedGroupDiscoveryFailureDegrades(t *testing.T) {
	d := &stubDisc{}
	d.set(groupFailure(kube.GVRCertificates.GroupVersion()), served(kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters)...)
	e := newEnv(t, envOpts{prefill: func(e *env) { e.discovery = d }})
	pi := e.state(plugin.StateDegraded)
	if !strings.Contains(pi.Message, "discovery failed for: cert-manager") {
		t.Errorf("message = %q", pi.Message)
	}
	// The group recovers: healthy again.
	d.set(nil, served(allServed...)...)
	e.state(plugin.StateHealthy)
}

// A dynamic informer never gates Synced, so a topic waits for its own resources: the cluster
// topic for certificates and external secrets, apps for routes, storage for CNPG.
func TestKubernetesClusterTopicWaitsForItsGVRs(t *testing.T) {
	for _, tc := range []struct {
		resource string
		topic    string
	}{
		{"certificates", "kubernetes.cluster"},
		{"externalsecrets", "kubernetes.cluster"},
		{"httproutes", "kubernetes.apps"},
		{"clusters", "kubernetes.storage"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			open := func() { once.Do(func() { close(release) }) }
			t.Cleanup(open)
			e := newEnv(t, envOpts{prefill: func(e *env) {
				e.dc.PrependReactor("list", tc.resource, func(clienttesting.Action) (bool, k8sruntime.Object, error) {
					<-release
					return false, nil, nil
				})
			}})
			e.state(plugin.StateStarting)
			// The other topics of the plugin are not held back by it.
			time.Sleep(150 * time.Millisecond)
			if _, ok := e.hub.Latest("", model.Topic(tc.topic)); ok {
				t.Fatalf("%s was published while %s was not listed", tc.topic, tc.resource)
			}
			open()
			waitFor(t, tc.topic, func() bool { _, ok := e.hub.Latest("", model.Topic(tc.topic)); return ok })
			e.state(plugin.StateHealthy)
		})
	}
}

// No Prometheus: no request is made, the sources say it is not configured, and the row does not
// wait for polls that will never run.
func TestUnconfiguredUpstreamsMakeNoRequests(t *testing.T) {
	e := newEnv(t, envOpts{noProm: true, cfg: func(c *pluginconf.Kubernetes) {
		c.BackupJobs = []pluginconf.BackupJob{{Namespace: "backup", CronJob: "nightly"}}
	}})
	e.healthy()
	for _, topic := range []string{"kubernetes.cluster", "kubernetes.storage"} {
		var raw json.RawMessage
		srcs, _ := e.payload(topic, &raw)
		found := false
		for _, s := range srcs {
			if s.Name == sourcePrometheus {
				found = true
				if s.OK || s.Error != "not_configured" {
					t.Errorf("%s: prometheus source = %+v", topic, s)
				}
			}
		}
		if !found {
			t.Errorf("%s: no prometheus source in %+v", topic, srcs)
		}
	}
	if n := e.prom.requests(); n != 0 {
		t.Errorf("%d queries reached a prometheus that is not running", n)
	}
}
