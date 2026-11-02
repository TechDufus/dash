package kubernetes

import (
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

var withIngresses = []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters, kube.GVRIngresses}

// The Ingress informer exists only when apps.ingress is on: the plugin asks discovery for the
// resource only then.
func TestIngressInformerOnlyWhenEnabled(t *testing.T) {
	off := newEnv(t, envOpts{crds: withIngresses})
	off.healthy()
	r := off.runtime()
	if r.cl.Has(kube.GVRIngresses) || r.cl.Listed(kube.GVRIngresses) {
		t.Error("the Ingress informer exists with apps.ingress off")
	}
	if _, wanted := r.wanted()[kube.GVRIngresses]; wanted {
		t.Error("Ingresses are wanted with apps.ingress off")
	}

	on := newEnv(t, envOpts{crds: withIngresses, cfg: func(c *pluginconf.Kubernetes) { c.Apps.Ingress = true }})
	on.healthy()
	r = on.runtime()
	if !r.cl.Has(kube.GVRIngresses) || !r.cl.Listed(kube.GVRIngresses) {
		t.Error("the Ingress informer does not exist with apps.ingress on")
	}
}

// An enabled key whose resource is not served is a not_configured note and a degraded row, and the
// apps still publish.
func TestIngressEnabledButNotServedIsDegraded(t *testing.T) {
	e := newEnv(t, envOpts{cfg: func(c *pluginconf.Kubernetes) { c.Apps.Ingress = true }})
	pi := e.state(plugin.StateDegraded)
	if !strings.Contains(pi.Message, "ingress") {
		t.Errorf("message = %q", pi.Message)
	}
	waitFor(t, "kubernetes.apps", func() bool { _, ok := e.hub.Latest("", "kubernetes.apps"); return ok })
	srcs, _ := e.payload("kubernetes.apps", &model.AppsData{})
	if !noteNames(srcs)["ingress"] {
		t.Errorf("apps sources = %+v", srcs)
	}
}

// Ingresses served but refused to this client never list: apps wait for them as long as the
// informers get to sync, then publish without them, with the category of the refusal.
func TestIngressRefusedPublishesAfterTheSyncTimeout(t *testing.T) {
	e := newEnv(t, envOpts{
		fakeClock: true, crds: withIngresses,
		cfg: func(c *pluginconf.Kubernetes) { c.Apps.Ingress = true },
		prefill: func(e *env) {
			e.dc.PrependReactor("list", "ingresses", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"}, "", errors.New("no"))
			})
		},
	})
	waitFor(t, "kubernetes.cluster", func() bool { _, ok := e.hub.Latest("", "kubernetes.cluster"); return ok })
	time.Sleep(100 * time.Millisecond)
	if _, ok := e.hub.Latest("", "kubernetes.apps"); ok {
		t.Fatal("apps published before the Ingresses were listed or the timeout passed")
	}
	e.clock.advance(60 * time.Second)
	waitFor(t, "kubernetes.apps", func() bool { _, ok := e.hub.Latest("", "kubernetes.apps"); return ok })
	pi := e.state(plugin.StateDegraded)
	if !strings.Contains(pi.Message, "discovery failed for: ingress") {
		t.Errorf("message = %q", pi.Message)
	}
	srcs, _ := e.payload("kubernetes.apps", &model.AppsData{})
	found := false
	for _, s := range srcs {
		if s.Name == "ingress" {
			found = true
			if s.OK || s.Error != "auth" {
				t.Errorf("ingress source = %+v, want auth", s)
			}
		}
	}
	if !found {
		t.Errorf("no ingress source in %+v", srcs)
	}
}

// The wanted set follows the settings, and the CNPG group of the transitional storage rows is in it.
func TestWantedResourcesFollowTheSettings(t *testing.T) {
	r := &runtime{cfg: pluginconf.DefaultKubernetes()}
	got := r.wanted()
	for _, g := range []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRCNPGClusters} {
		if _, ok := got[g]; !ok {
			t.Errorf("%v is not wanted", g)
		}
	}
	if len(got) != 4 {
		t.Errorf("wanted = %v", got)
	}
}
