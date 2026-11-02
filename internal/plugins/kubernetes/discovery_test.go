package kubernetes

import (
	"encoding/json"
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

var withIngresses = []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes, kube.GVRIngresses}

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
	if !strings.Contains(pi.Message, "cannot list: ingress (auth)") {
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

// Every dynamic resource gets the Ingress treatment: one that is served but never lists (a list
// the client may not make) holds its topic back until the sync timeout, then the topic is
// published without it and the row is degraded with the category of the watch error.
func TestServedResourceThatNeverListsPublishesAfterTheSyncTimeout(t *testing.T) {
	for _, tc := range []struct {
		resource, topic, name string
		others                []string // the topics that do not wait for it
	}{
		{"certificates", "kubernetes.cluster", "cert-manager", []string{"kubernetes.storage", "kubernetes.apps"}},
		{"externalsecrets", "kubernetes.cluster", "external-secrets", []string{"kubernetes.storage", "kubernetes.apps"}},
		{"httproutes", "kubernetes.apps", "gateway-api", []string{"kubernetes.cluster", "kubernetes.storage"}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			e := newEnv(t, envOpts{
				fakeClock: true,
				prefill: func(e *env) {
					e.dc.PrependReactor("list", tc.resource, func(clienttesting.Action) (bool, k8sruntime.Object, error) {
						return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: tc.resource}, "", errors.New("no"))
					})
				},
			})
			published := func(topic string) func() bool {
				return func() bool { _, ok := e.hub.Latest("", model.Topic(topic)); return ok }
			}
			// The other topics are not held back. A gate that let the held topic through would
			// publish it in the round that publishes the others, or earlier.
			for _, o := range tc.others {
				waitFor(t, o, published(o))
			}
			if published(tc.topic)() {
				t.Fatalf("%s was published before %s was listed or the timeout passed", tc.topic, tc.resource)
			}

			e.clock.advance(60 * time.Second)
			waitFor(t, tc.topic, published(tc.topic))
			pi := e.state(plugin.StateDegraded)
			if want := "cannot list: " + tc.name + " (auth)"; !strings.Contains(pi.Message, want) {
				t.Errorf("message = %q, want %q", pi.Message, want)
			}
			var raw json.RawMessage
			srcs, _ := e.payload(tc.topic, &raw)
			found := false
			for _, s := range srcs {
				if s.Name == tc.name {
					found = true
					if s.OK || s.Error != "auth" {
						t.Errorf("%s source = %+v, want auth", tc.name, s)
					}
				}
			}
			if !found {
				t.Errorf("no %s source in %+v", tc.name, srcs)
			}
		})
	}
}

// A resource the settings mark optional is not a degraded row when its list is refused, though
// the topic still waits for it and then publishes without it.
func TestOptionalResourceThatNeverListsDoesNotDegrade(t *testing.T) {
	e := newEnv(t, envOpts{
		fakeClock: true,
		cfg:       func(c *pluginconf.Kubernetes) { c.Optional.Certificates = true },
		prefill: func(e *env) {
			e.dc.PrependReactor("list", "certificates", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "certificates"}, "", errors.New("no"))
			})
		},
	})
	waitFor(t, "kubernetes.apps", func() bool { _, ok := e.hub.Latest("", "kubernetes.apps"); return ok })
	e.clock.advance(60 * time.Second)
	waitFor(t, "kubernetes.cluster", func() bool { _, ok := e.hub.Latest("", "kubernetes.cluster"); return ok })
	if pi := e.state(plugin.StateHealthy); pi.Message != "" {
		t.Errorf("message = %q", pi.Message)
	}
}

// The wanted set follows the settings: the three optional groups, and no CNPG group (the cnpg
// plugin reads that one).
func TestWantedResourcesFollowTheSettings(t *testing.T) {
	r := &runtime{cfg: pluginconf.DefaultKubernetes()}
	got := r.wanted()
	for _, g := range []schema.GroupVersionResource{kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes} {
		if _, ok := got[g]; !ok {
			t.Errorf("%v is not wanted", g)
		}
	}
	if _, ok := got[kube.GVRCNPGClusters]; ok || len(got) != 3 {
		t.Errorf("wanted = %v", got)
	}
}
