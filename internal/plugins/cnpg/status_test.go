package cnpg

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// one is a cluster object and its backup age, the minimal healthy world.
func one(r *rig) {
	r.kc.SetObjects(gvr, cnpgCluster("db", "pg-main", map[string]any{"instances": int64(2), "readyInstances": int64(2), "phase": "Cluster in healthy state"}, nil))
	r.q.set(defaultQuery, sample(90, "namespace", "db", "cluster", "pg-main"))
}

// Without a running, available kubernetes plugin the row is blocked and nothing is read or
// published; the plugin starts when kubernetes does.
func TestCNPGIsBlockedWithoutKubernetes(t *testing.T) {
	for name, o := range map[string]rigOpts{
		"disabled":    {noKube: true},
		"unavailable": {kubeState: plugin.StateUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			o.prefill = one
			r := newRig(t, o)
			pi := r.waitState(model.PluginBlocked)
			if !strings.Contains(pi.Message, "kubernetes") {
				t.Errorf("message = %q", pi.Message)
			}
			if r.published() || len(r.kc.Watched()) != 0 || r.q.n.Load() != 0 {
				t.Errorf("a blocked plugin acted: published %v, watched %v, %d queries", r.published(), r.kc.Watched(), r.q.n.Load())
			}
			if name == "disabled" {
				r.switchOn("kubernetes")
			} else {
				r.kube.state.Store(plugin.StateHealthy)
			}
			r.waitState(model.PluginHealthy)
			d, _ := r.clusters()
			if len(d.Clusters) != 1 {
				t.Errorf("clusters = %+v", d.Clusters)
			}
		})
	}
}

// Kubernetes in error exports no service, but its row does not block: cnpg runs, its Start did
// not fail, the row says it waits, and nothing is published or watched. The service coming back
// is the recovery.
func TestCNPGWithKubernetesInError(t *testing.T) {
	r := newRig(t, rigOpts{noService: true, kubeState: plugin.StateError, prefill: one})
	// Start leaves the row starting with no message until the watch loop's first look.
	var pi model.PluginInfo
	eventually(t, "the row to say it waits for the kubernetes service", func() bool {
		pi = r.row()
		return pi.State == model.PluginStarting && pi.Message == "waiting for the kubernetes service"
	})
	if r.published() || len(r.kc.Watched()) != 0 {
		t.Errorf("acted without a service: published %v, watched %v", r.published(), r.kc.Watched())
	}
	if !plugin.Visible(pi.State) {
		t.Error("the waiting row is hidden")
	}

	r.kube.setCluster(r.kc)
	r.kube.state.Store(plugin.StateHealthy)
	r.waitState(model.PluginHealthy)
	if d, _ := r.clusters(); len(d.Clusters) != 1 {
		t.Errorf("clusters = %+v", d.Clusters)
	}
	if w := r.kc.Watched(); len(w) != 1 || !slices.Equal(w[0], []schema.GroupVersionResource{gvr}) {
		t.Errorf("watched = %v, want the CNPG resource once", w)
	}

	// The service goes again: the row waits, and the last payload stays (nothing is published).
	r.kube.setCluster(nil)
	pi = r.waitState(model.PluginStarting)
	if pi.Message != "waiting for the kubernetes service" {
		t.Errorf("message = %q", pi.Message)
	}
}

// Watch is asked once per service, and again when the service is another one (a kubernetes
// restart builds a new cluster that has no registrations).
func TestCNPGWatchesOncePerService(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	if w := r.kc.Watched(); len(w) != 1 || !slices.Equal(w[0], []schema.GroupVersionResource{gvr}) {
		t.Fatalf("watched = %v", w)
	}
	second := plugintest.NewKubeCluster()
	second.SetObjects(gvr, cnpgCluster("db", "pg-other", nil, nil))
	r.kube.setCluster(second)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool {
		return len(d.Clusters) == 1 && d.Clusters[0].Name == "pg-other"
	})
	if w := second.Watched(); len(w) != 1 {
		t.Errorf("the new service was asked %d times, want once", len(w))
	}
	if w := r.kc.Watched(); len(w) != 1 {
		t.Errorf("the old service was asked %d times, want once", len(w))
	}
}

// Without the CRD the row is unavailable (hidden), nothing is published (not even an empty list)
// and no query is sent: there is nothing to attach an age to.
func TestCNPGUnavailableWithoutTheCRD(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) { one(r); r.kc.SetServed(gvr, false) }})
	pi := r.waitState(model.PluginUnavailable)
	if plugin.Visible(pi.State) || !strings.Contains(pi.Message, "CRD") {
		t.Errorf("row = %+v", pi)
	}
	if w := r.kc.Watched(); len(w) != 1 {
		t.Errorf("watched = %v: the resource must be asked for, or a late CRD is never found", w)
	}
	if r.published() {
		t.Error("an empty payload was published")
	}
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("%d queries sent without a CRD", n)
	}
}

// The CRD goes: the row is hidden and the payload emptied (a hidden row is never the last word
// with data in it). The CRD returns: the row turns visible only after the rebuilt payload is
// out, so the supervisor's resend of the latest event shows the fresh payload, never the empty
// one.
func TestCNPGRowStaysHiddenUntilBuiltForTheReturningCRD(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 1 })

	r.kc.SetServed(gvr, false)
	r.waitState(model.PluginUnavailable)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return d.Clusters != nil && len(d.Clusters) == 0 })

	r.kc.SetServed(gvr, true)
	eventually(t, "the row to turn visible", func() bool { return plugin.Visible(r.row().State) })
	// At the first look that shows the row the payload must already hold the cluster.
	d, _, ok := stored[model.CNPGClustersData](t, r.hub, "cnpg.clusters")
	if !ok || len(d.Clusters) != 1 {
		t.Fatalf("the row is visible and the latest payload is %+v (published %v)", d, ok)
	}
}

// The invariant on an open stream: once the core.plugins event shows the row, the cnpg.clusters
// events that follow (the supervisor's resend of the latest one) carry the rebuilt clusters,
// never the empty list published while the row was hidden.
func TestCNPGOpenStreamNeverSeesTheEmptyPayloadAfterTheRowIsVisible(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	r.kc.SetServed(gvr, false)
	r.waitState(model.PluginUnavailable)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return d.Clusters != nil && len(d.Clusters) == 0 })
	// The supervisor must have published the hidden row: a flap faster than its status tick is
	// invisible to it (the plugin must not flap that fast, and a test must not make it).
	waitTopic(t, r.hub, string(model.TopicPlugins), func(d model.PluginsData) bool {
		for _, pi := range d.Plugins {
			if pi.ID == string(ID) {
				return !plugin.Visible(pi.State)
			}
		}
		return false
	})

	_, ch, cancel := r.hub.Subscribe("", nil)
	defer cancel()
	r.kc.SetServed(gvr, true)

	visible, after := false, 0
	deadline := time.After(5 * time.Second)
	for after == 0 {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("the subscriber was dropped")
			}
			switch ev.Topic {
			case string(model.TopicPlugins):
				var env model.Envelope
				var data model.PluginsData
				if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &data) != nil {
					t.Fatalf("core.plugins: %v", err)
				}
				for _, pi := range data.Plugins {
					if pi.ID == string(ID) && plugin.Visible(pi.State) {
						visible = true
					}
				}
			case "cnpg.clusters":
				if !visible {
					continue
				}
				var env model.Envelope
				var d model.CNPGClustersData
				if err := json.Unmarshal(ev.Data, &env); err != nil || json.Unmarshal(env.Data, &d) != nil {
					t.Fatalf("cnpg.clusters: %v", err)
				}
				if len(d.Clusters) == 0 {
					t.Fatal("the empty payload reached the stream after the row turned visible")
				}
				after++
			}
		case <-deadline:
			t.Fatalf("no cnpg.clusters event after the row turned visible (visible %v)", visible)
		}
	}
}

// The row is starting until the informers synced, the CRD is listed (or refused) and the first
// age poll ended, and nothing is published before that.
func TestCNPGStartingUntilSyncedListedAndTheFirstAgePollEnded(t *testing.T) {
	r := newRig(t, rigOpts{gated: true, prefill: func(r *rig) {
		one(r)
		r.kc.SetSynced(false)
		r.kc.SetListed(gvr, false)
	}})
	stage := func(want string) {
		t.Helper()
		pi := r.waitState(model.PluginStarting)
		if want != "" {
			eventually(t, "the message "+want, func() bool { return r.row().Message == want })
		}
		if !plugin.Visible(pi.State) {
			t.Errorf("a starting row is hidden")
		}
		if r.published() {
			t.Fatalf("published while %s", want)
		}
	}
	stage("listing the CloudNativePG clusters") // not synced

	r.kc.SetSynced(true)
	stage("listing the CloudNativePG clusters") // synced, not listed
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("%d queries before the CRD was listed", n)
	}

	r.kc.SetListed(gvr, true)
	eventually(t, "the poll to start", func() bool { return r.q.n.Load() > 0 })
	stage("waiting for the first backup-age poll")

	close(r.q.gate)
	r.waitState(model.PluginHealthy)
	d, srcs := r.clusters()
	if len(d.Clusters) != 1 || d.Clusters[0].LastBackupAgeSeconds == nil {
		t.Errorf("clusters = %+v", d.Clusters)
	}
	if !hasSource(srcs, "kubernetes", true, "") || !hasSource(srcs, "prometheus", true, "") {
		t.Errorf("sources = %+v", srcs)
	}
}

// A refused list (the category says why) is not stuck starting: the row is degraded with the
// category, the topic is published with no clusters and a cnpg note; it recovers when the list
// does.
func TestCNPGRefusedListIsDegradedWithTheCategory(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		one(r)
		r.kc.SetListed(gvr, false)
		r.kc.SetRefused(gvr, "auth")
	}})
	pi := r.waitState(model.PluginDegraded)
	if pi.Message != "cannot list: cnpg (auth)" {
		t.Errorf("message = %q", pi.Message)
	}
	d, srcs := r.clusters()
	if d.Clusters == nil || len(d.Clusters) != 0 {
		t.Errorf("clusters = %#v, want an empty list", d.Clusters)
	}
	if !hasSource(srcs, "cnpg", false, "auth") {
		t.Errorf("sources = %+v", srcs)
	}

	r.kc.ClearRefused(gvr)
	r.kc.SetListed(gvr, true)
	r.waitState(model.PluginHealthy)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 1 })
}

// A group that failed discovery is neither present nor absent: the row is degraded, not hidden,
// and says so; the topic holds no clusters and a note.
func TestCNPGFailedGroupDegrades(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		one(r)
		r.kc.SetServed(gvr, false)
		r.kc.SetFailedGroups(gvr.GroupVersion())
	}})
	pi := r.waitState(model.PluginDegraded)
	if pi.Message != "discovery failed for: cnpg" {
		t.Errorf("message = %q", pi.Message)
	}
	d, srcs := r.clusters()
	if d.Clusters == nil || len(d.Clusters) != 0 || !hasSource(srcs, "cnpg", false, "unreachable") {
		t.Errorf("clusters %#v sources %+v", d.Clusters, srcs)
	}

	r.kc.SetFailedGroups()
	r.kc.SetServed(gvr, true)
	r.waitState(model.PluginHealthy)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 1 })
}

// A group whose discovery failed after its clusters were listed keeps its cached answer in the
// real cluster (Has stays true, List still returns them), so the topic keeps the clusters while
// the row is degraded and carries the note.
func TestCNPGFailedGroupKeepsTheCachedClusters(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 1 })

	r.kc.SetFailedGroups(gvr.GroupVersion()) // still served: only discovery of the group failed
	pi := r.waitState(model.PluginDegraded)
	if pi.Message != "discovery failed for: cnpg" {
		t.Errorf("message = %q", pi.Message)
	}
	d, srcs := waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool {
		return len(d.Clusters) == 1
	})
	if d.Clusters[0].Name != "pg-main" || !hasSource(srcs, "cnpg", false, "unreachable") {
		t.Errorf("clusters %+v sources %+v", d.Clusters, srcs)
	}

	r.kc.SetFailedGroups()
	r.waitState(model.PluginHealthy)
}

// Only the Prometheus query failing degrades the row: the clusters are still published without
// an age, the source says why, and the last good age survives a later failure.
func TestCNPGDegradedWhenOnlyTheAgeQueryFails(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		one(r)
		r.q.setErr(defaultQuery, errors.New("dial tcp 192.0.2.7:9090: refused"))
	}})
	pi := r.waitState(model.PluginDegraded)
	if pi.Message != "prometheus: unreachable" {
		t.Errorf("message = %q", pi.Message)
	}
	d, srcs := r.clusters()
	if len(d.Clusters) != 1 || d.Clusters[0].LastBackupAgeSeconds != nil {
		t.Errorf("clusters = %+v, want the cluster with no age", d.Clusters)
	}
	if !hasSource(srcs, "prometheus", false, "unreachable") || !hasSource(srcs, "kubernetes", true, "") {
		t.Errorf("sources = %+v", srcs)
	}

	r.q.set(defaultQuery, sample(90, "namespace", "db", "cluster", "pg-main"))
	r.waitState(model.PluginHealthy)
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool {
		return len(d.Clusters) == 1 && d.Clusters[0].LastBackupAgeSeconds != nil && *d.Clusters[0].LastBackupAgeSeconds == 90
	})

	// A failure keeps the last good age.
	r.q.setErr(defaultQuery, errors.New("boom"))
	r.waitState(model.PluginDegraded)
	d, _ = r.clusters()
	if d.Clusters[0].LastBackupAgeSeconds == nil || *d.Clusters[0].LastBackupAgeSeconds != 90 {
		t.Errorf("the failed poll lost the age: %+v", d.Clusters[0])
	}
}

// With no Prometheus plugin the age is optional: the clusters are listed, the row is healthy and
// the source says not_configured. No query is sent.
func TestCNPGWithoutPrometheusListsTheClusters(t *testing.T) {
	r := newRig(t, rigOpts{noProm: true, prefill: one})
	r.waitState(model.PluginHealthy)
	d, srcs := r.clusters()
	if len(d.Clusters) != 1 || d.Clusters[0].LastBackupAgeSeconds != nil {
		t.Errorf("clusters = %+v", d.Clusters)
	}
	if !hasSource(srcs, "prometheus", false, "not_configured") {
		t.Errorf("sources = %+v", srcs)
	}
	if n := r.q.n.Load(); n != 0 {
		t.Errorf("%d queries reached a prometheus that is not running", n)
	}
}

// The cluster list follows the informer's events, not the polls: a change reaches the topic.
func TestCNPGFollowsInformerEvents(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	r.kc.SetObjects(gvr,
		cnpgCluster("db", "pg-main", map[string]any{"instances": int64(3), "readyInstances": int64(1)}, nil),
		cnpgCluster("app", "pg-new", nil, nil))
	d, _ := waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 2 })
	if d.Clusters[0].Name != "pg-new" || d.Clusters[1].Instances != 3 || d.Clusters[1].ReadyInstances != 1 {
		t.Errorf("clusters = %+v", d.Clusters)
	}
}

// The age is read again every poll interval, so a new backup moves the row.
func TestCNPGPollsTheAgeEveryInterval(t *testing.T) {
	r := newRig(t, rigOpts{prefill: one})
	r.waitState(model.PluginHealthy)
	r.q.set(defaultQuery, sample(30, "namespace", "db", "cluster", "pg-main"))
	waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool {
		return len(d.Clusters) == 1 && d.Clusters[0].LastBackupAgeSeconds != nil && *d.Clusters[0].LastBackupAgeSeconds == 30
	})
	if n := len(r.q.asked()); n < 2 {
		t.Errorf("%d queries, want the poll repeated", n)
	}
}

// The upstream's detail goes to the log scrubbed, the category to the viewers.
func TestCNPGFailingPrometheusLeaksNothing(t *testing.T) {
	scrub := func(s string) string { return strings.ReplaceAll(s, canary, "[scrubbed]") }
	r := newRig(t, rigOpts{scrub: scrub, prefill: func(r *rig) {
		one(r)
		r.q.setErr(defaultQuery, errors.New("get https://prom.internal:9090/api?token="+canary+": connection refused"))
	}})
	r.waitState(model.PluginDegraded)
	eventually(t, "the failure to be logged", func() bool { return strings.Contains(r.logs.String(), "upstream source failed") })

	var bodies []string
	for _, topic := range []string{"cnpg.clusters", string(model.TopicPlugins)} {
		ev, ok := r.hub.Latest("", model.Topic(topic))
		if !ok {
			t.Fatalf("%s has no snapshot", topic)
		}
		bodies = append(bodies, string(ev.Data))
	}
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

// Refs: every cluster has one of the plugin, unique across namespaces for one name.
func TestCNPGRefs(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kc.SetObjects(gvr, cnpgCluster("a", "pg", nil, nil), cnpgCluster("b", "pg", nil, nil))
	}})
	d, _ := waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 2 })
	plugintest.AssertRefs(t, r.sup, ID, d, model.CNPGCluster{})
	if d.Clusters[0].Ref != "cnpg:Cluster/a/pg" || d.Clusters[1].Ref != "cnpg:Cluster/b/pg" {
		t.Errorf("refs = %q %q", d.Clusters[0].Ref, d.Clusters[1].Ref)
	}
}

// The links come from the running link providers, for kind Cluster.
func TestCNPGLinksComeFromTheProviders(t *testing.T) {
	r := newRig(t, rigOpts{
		prefill: one,
		link: func(req plugin.LinkRequest) []model.Link {
			if req.Kind != "Cluster" {
				return nil
			}
			return []model.Link{{Label: "Cluster:" + req.Namespace + "/" + req.Name, Href: "https://headlamp.example.com/"}}
		},
	})
	d, _ := waitTopic(t, r.hub, "cnpg.clusters", func(d model.CNPGClustersData) bool { return len(d.Clusters) == 1 })
	if l := d.Clusters[0].Links; len(l) != 1 || l[0].Label != "Cluster:db/pg-main" {
		t.Errorf("links = %+v", l)
	}
}

// The config reaches the plugin the way the supervisor hands it over.
func TestCNPGSettingsArePointerOrValue(t *testing.T) {
	for _, cfg := range []any{&pluginconf.CNPG{BackupMetric: "m"}, pluginconf.CNPG{BackupMetric: "m"}} {
		c, err := settings(cfg)
		if err != nil || c.BackupMetric != "m" {
			t.Errorf("%T: %+v %v", cfg, c, err)
		}
	}
	if _, err := settings(3); err == nil {
		t.Error("settings accepted an int")
	}
}
