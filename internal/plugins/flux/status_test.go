package flux

import (
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// The Flux API groups, for the tests that fail a discovery.
var (
	sourceGV    = kube.GVRGitRepositories.GroupVersion()
	kustomizeGV = kube.GVRKustomizations.GroupVersion()
	helmGV      = kube.GVRHelmReleases.GroupVersion()
)

func serveOnly(r *rig, served ...schema.GroupVersionResource) {
	for _, g := range gvrs {
		r.kc.SetServed(g, slices.Contains(served, g))
	}
}

func noPayload(t *testing.T, r *rig) {
	t.Helper()
	if _, _, ok := r.published(); ok {
		t.Error("the topic was published")
	}
}

func sourceNamed(srcs []model.SourceStatus, name string) (model.SourceStatus, bool) {
	for _, s := range srcs {
		if s.Name == name {
			return s, true
		}
	}
	return model.SourceStatus{}, false
}

func TestFluxIsBlockedWithoutKubernetes(t *testing.T) {
	r := newRig(t, rigOpts{noKube: true})
	row := r.waitState(model.PluginBlocked)
	if plugin.Visible(row.State) {
		t.Errorf("a blocked row is visible: %+v", row)
	}
	noPayload(t, r)
	if _, ok := r.contributor(); ok {
		t.Error("a blocked flux contributes to the overview")
	}
	if len(r.kc.Watched()) != 0 {
		t.Errorf("a blocked flux asked the cluster to watch: %v", r.kc.Watched())
	}

	// kubernetes comes up: the supervisor starts flux, which publishes.
	r.set.Set(plugin.InstanceConfig{Name: "kubernetes", Type: "kubernetes"})
	r.reload()
	r.waitState(model.PluginHealthy)
	r.waitPublished("the topic", func(model.FluxData) bool { return true })
}

// While kubernetes reports unavailable (the API server is down) flux is blocked, and it runs again
// when kubernetes does.
func TestFluxIsBlockedWhileKubernetesIsUnavailable(t *testing.T) {
	r := newRig(t, rigOpts{kubeState: plugin.StateUnavailable})
	r.waitState(model.PluginBlocked)
	r.kube.setState(plugin.StateHealthy)
	r.waitState(model.PluginHealthy)
	r.waitPublished("the topic", func(model.FluxData) bool { return true })
}

func TestFluxStartingUntilSyncedAndEveryGVRIsListedOrRefused(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kc.SetSynced(false)
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
	}})
	row := r.waitState(model.PluginStarting)
	if row.Message != "informers syncing" {
		t.Errorf("message = %q", row.Message)
	}
	if k, _ := sourceNamed(r.rowSources(), "kubernetes"); k.OK || k.Error != "informers not synced" {
		t.Errorf("kubernetes source = %+v", k)
	}

	// Synced, but one list has not completed: still not built, from no partial list.
	r.kc.SetListed(kube.GVRHelmReleases, false)
	r.kc.SetSynced(true)
	eventually(t, "the listing message", func() bool { return r.row().Message == "listing the flux resources" })
	if row := r.row(); row.State != model.PluginStarting {
		t.Errorf("state = %s", row.State)
	}
	noPayload(t, r)
	if _, ok := r.contributor(); !ok {
		t.Error("a starting flux has no contributor entry")
	} else if r.attention() != nil || r.kpis() != nil {
		t.Error("a starting flux has an opinion before it built")
	}

	r.kc.SetListed(kube.GVRHelmReleases, true)
	r.waitState(model.PluginHealthy)
	d := r.waitPublished("the objects", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 })
	if len(d.HelmReleases) != 0 {
		t.Errorf("releases = %v", d.HelmReleases)
	}
}

func TestFluxUnavailableWhenNoneOfTheFiveIsServed(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) { serveOnly(r) }})
	row := r.waitState(model.PluginUnavailable)
	if plugin.Visible(row.State) {
		t.Fatal("the row is visible")
	}
	// An empty payload on the way into unavailable.
	d := r.waitPublished("an empty payload", func(model.FluxData) bool { return true })
	if d.Sources == nil || len(d.Sources)+len(d.Kustomizations)+len(d.HelmReleases) != 0 {
		t.Errorf("payload = %+v", d)
	}
	// The contributor is dropped by the supervisor while hidden (SF6) and flux has no opinion.
	if c, ok := r.contributor(); ok && (c.Attention != nil || c.KPIs != nil) {
		t.Errorf("a hidden flux contributes: %+v", c)
	}
	if r.attention() != nil || r.kpis() != nil {
		t.Error("a hidden flux has attention or KPIs")
	}
}

// Some of the five served and some not is healthy (O-4): the lists of a missing kind are empty and
// nothing marks the gap.
func TestFluxPartlyServedIsHealthy(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		serveOnly(r, kube.GVRKustomizations, kube.GVRHelmReleases)
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
	}})
	row := r.waitState(model.PluginHealthy)
	if row.Message != "" {
		t.Errorf("message = %q", row.Message)
	}
	d := r.waitPublished("the kustomization", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 })
	if len(d.Sources) != 0 {
		t.Errorf("sources = %v", d.Sources)
	}
	_, srcs, _ := r.published()
	if len(srcs) != 1 || srcs[0].Name != "kubernetes" || !srcs[0].OK {
		t.Errorf("sources = %+v, want the kubernetes one alone", srcs)
	}
	eventually(t, "a contribution", func() bool { return r.attention() != nil })
}

// A list the plugin may not make is refused after the kubernetes plugin's own bounded wait: the
// topic is then published without it.
func TestFluxRefusedListPublishesAfterTheSyncTimeout(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
		r.kc.SetListed(kube.GVRGitRepositories, false)
	}})
	r.waitState(model.PluginStarting)
	noPayload(t, r)
	r.kc.SetRefused(kube.GVRGitRepositories, "auth")
	r.waitPublished("the topic without the refused list", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 })
	r.waitState(model.PluginDegraded)
}

func TestFluxRefusedListIsDegradedWithTheCategory(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
		r.kc.SetListed(kube.GVRGitRepositories, false)
		r.kc.SetListed(kube.GVRHelmReleases, false)
		r.kc.SetRefused(kube.GVRGitRepositories, "auth")
		r.kc.SetRefused(kube.GVRHelmReleases, "timeout")
	}})
	row := r.waitState(model.PluginDegraded)
	if row.Message != "cannot list: gitrepositories (auth), helmreleases (timeout)" {
		t.Errorf("message = %q", row.Message)
	}
	for _, name := range []string{"gitrepositories", "helmreleases"} {
		if s, ok := sourceNamed(r.rowSources(), name); !ok || s.OK || s.Error == "" {
			t.Errorf("row source %s = %+v (present %v)", name, s, ok)
		}
	}
	_, srcs, _ := r.published()
	if s, ok := sourceNamed(srcs, "gitrepositories"); !ok || s.OK || s.Error != "auth" {
		t.Errorf("topic source gitrepositories = %+v", s)
	}
	if s, ok := sourceNamed(srcs, "helmreleases"); !ok || s.Error != "timeout" {
		t.Errorf("topic source helmreleases = %+v", s)
	}
	if k, _ := sourceNamed(srcs, "kubernetes"); !k.OK {
		t.Errorf("kubernetes source = %+v", k)
	}
	// The objects it could read are published and contribute.
	eventually(t, "a contribution", func() bool { return r.attention() != nil })

	// The list is allowed again: healthy, the note gone.
	r.kc.ClearRefused(kube.GVRGitRepositories)
	r.kc.ClearRefused(kube.GVRHelmReleases)
	r.kc.SetListed(kube.GVRGitRepositories, true)
	r.kc.SetListed(kube.GVRHelmReleases, true)
	r.waitState(model.PluginHealthy)
	eventually(t, "the notes to go", func() bool {
		_, srcs, _ := r.published()
		return len(srcs) == 1
	})
}

// flux has no clock of its own: the 60 s of API-server up-time are counted by the kubernetes
// plugin, which pauses them while the API server does not answer. A list that is neither listed nor
// refused stays unpublished however much time the host clock reports.
func TestFluxRefusedClockPausesDuringAnOutage(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) { r.kc.SetListed(kube.GVRHelmRepositories, false) }})
	r.waitState(model.PluginStarting)
	r.clk.advance(30 * time.Minute)
	r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
	// Wait for rounds, not for a duration: each one has run through the whole decision.
	before := r.kube.calls.Load()
	eventually(t, "more rounds after the jump", func() bool { return r.kube.calls.Load() >= before+20 })
	noPayload(t, r)
	if row := r.row(); row.State != model.PluginStarting || row.Message != "listing the flux resources" {
		t.Errorf("row = %+v", row)
	}
	r.kc.SetRefused(kube.GVRHelmRepositories, "unreachable")
	r.waitState(model.PluginDegraded)
}

// A resource that appears later (a CRD installed under a running flux) is waited for like the
// first five were: until it is listed or refused, and the row says so without losing the topic.
func TestFluxLateCRDGetsItsOwnTimeout(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		serveOnly(r, kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVRHelmRepositories)
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
	}})
	r.waitState(model.PluginHealthy)
	r.waitPublished("the kustomization", func(d model.FluxData) bool { return len(d.Kustomizations) == 1 })

	r.kc.SetListed(kube.GVROCIRepositories, false)
	r.kc.SetServed(kube.GVROCIRepositories, true) // the CRD appeared, its first list has not completed
	eventually(t, "the row to wait for the new resource", func() bool {
		row := r.row()
		return row.State == model.PluginStarting && row.Message == "listing the flux resources"
	})
	// The payload it had stays: the page does not lose its data while the new list runs.
	if d, _, _ := r.published(); len(d.Kustomizations) != 1 {
		t.Errorf("payload = %+v", d)
	}
	// It is refused: published without it, degraded.
	r.kc.SetRefused(kube.GVROCIRepositories, "auth")
	row := r.waitState(model.PluginDegraded)
	if !strings.Contains(row.Message, "ocirepositories (auth)") {
		t.Errorf("message = %q", row.Message)
	}
}

// A discovery that fails for a Flux group degrades the row and adds a flux source that is
// unreachable, because the objects of the group are missing although the cluster has them; the
// groups of other resources do not count, and recovery clears both.
func TestFluxFailedGroupDegradesTheRowAndNotesTheTopic(t *testing.T) {
	other := schema.GroupVersion{Group: "cert-manager.io", Version: "v1"}
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kc.SetFailedGroups(other)
		r.kc.SetObjects(kube.GVRKustomizations, obj(kst, "g", "a", ready("ok")))
	}})
	r.waitState(model.PluginHealthy) // a failed group that is not Flux's is none of flux's business
	_, srcs, _ := r.published()
	if _, ok := sourceNamed(srcs, "flux"); ok {
		t.Errorf("a flux note for a foreign group: %+v", srcs)
	}

	r.kc.SetFailedGroups(sourceGV, other, kustomizeGV, helmGV)
	for _, g := range []schema.GroupVersionResource{kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories} {
		r.kc.SetServed(g, false) // Has is false for a group whose discovery never answered
	}
	row := r.waitState(model.PluginDegraded)
	const want = "discovery failed for: helm.toolkit.fluxcd.io, kustomize.toolkit.fluxcd.io, source.toolkit.fluxcd.io"
	if row.Message != want {
		t.Errorf("message = %q, want %q", row.Message, want)
	}
	if s, ok := sourceNamed(r.rowSources(), "flux"); !ok || s.OK || s.Error != "unreachable" {
		t.Errorf("row flux source = %+v", s)
	}
	eventually(t, "the topic's flux note", func() bool {
		_, srcs, _ := r.published()
		s, ok := sourceNamed(srcs, "flux")
		return ok && !s.OK && s.Error == "unreachable"
	})

	r.kc.SetFailedGroups(other)
	for _, g := range gvrs {
		r.kc.SetServed(g, true)
	}
	r.waitState(model.PluginHealthy)
	eventually(t, "the note to go", func() bool {
		_, srcs, _ := r.published()
		_, ok := sourceNamed(srcs, "flux")
		return !ok
	})
}

// A group that failed discovery is not the same as no Flux: the row is degraded, not hidden, even
// though every resource reads as not served.
func TestFluxAllFailedGroupsAreNotUnavailable(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		serveOnly(r)
		r.kc.SetFailedGroups(sourceGV, kustomizeGV, helmGV)
	}})
	row := r.waitState(model.PluginDegraded)
	if !plugin.Visible(row.State) {
		t.Errorf("row = %+v", row)
	}
}

// A list that completes empty sends no informer event: the plugin must notice the flags alone.
func TestFluxRebuildsWhenListedOrRefusedFlipsWithoutAnInformerEvent(t *testing.T) {
	r := newRig(t, rigOpts{prefill: func(r *rig) {
		r.kube.setService(quiet{r.kc})
		r.kc.SetListed(kube.GVRHelmReleases, false)
	}})
	r.waitState(model.PluginStarting)
	noPayload(t, r)
	// The list completes with no object: no informer event, so the revision is the same.
	r.kc.SetListed(kube.GVRHelmReleases, true)
	r.waitState(model.PluginHealthy)
	r.waitPublished("the first payload", func(model.FluxData) bool { return true })

	// The same for refused: it flips with no event.
	r.kc.SetListed(kube.GVRGitRepositories, false)
	r.kc.SetRefused(kube.GVRGitRepositories, "auth")
	r.waitState(model.PluginDegraded)
	r.kc.ClearRefused(kube.GVRGitRepositories)
	r.kc.SetListed(kube.GVRGitRepositories, true)
	r.waitState(model.PluginHealthy)
}

// quiet is a cluster whose informers never report a change: only the flags move.
type quiet struct{ *plugintest.KubeCluster }

func (quiet) GVRRevision(...schema.GroupVersionResource) uint64 { return 0 }
