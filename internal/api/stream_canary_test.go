package api

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/topics"
)

// fakeConnection is how the kubernetes plugin reaches the cluster in this test: an empty one whose
// informers sync at once.
func fakeConnection(pluginconf.Kubernetes) (*kubernetes.Connection, error) {
	return connectionWith(kubefake.NewClientset()), nil
}

// connectionWith is a cluster over client, with no custom resource served.
func connectionWith(client *kubefake.Clientset) *kubernetes.Connection {
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	return &kubernetes.Connection{Cluster: kube.NewFromClients(client,
		dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))}
}

// topicLineHas reports whether some line of the SSE stream carries the envelope of topic and the
// text want.
func topicLineHas(stream, topic, want string) bool {
	for line := range strings.SplitSeq(stream, "\n") {
		if strings.Contains(line, `"topic":"`+topic+`"`) && strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// Failing upstreams show a viewer's /api/stream one category and nothing of the upstream: real
// plugins poll them, publish into the hub, and the SSE handler serves what a browser would
// read. Not their URLs, host names or ports, not what they answered, and not the credential an
// upstream echoes back (P1.4 canary test, end to end). The kubernetes plugin runs next to them,
// over a fake cluster, with the Manager for flux reading it: its three topics carry the
// categories of the prometheus and gatus it reads and nothing more.
func TestStreamEndpointNeverCarriesUpstreamDetail(t *testing.T) {
	const credential = "CANARYSECRET123456"
	reg := secret.NewRegistry()
	captureLog(t, reg) // the detail goes to the log; keep it out of the test output
	token := secret.New(credential, "")
	reg.Add(token)

	// Proxmox answers 500 and echoes the Authorization header it was sent.
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error for "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(pve.Close)
	// Prometheus is behind a certificate the client does not trust; the proxmox plugin's storage
	// topic and the kubernetes plugin's usage and storage queries, read through it, show it.
	prom := httptest.NewUnstartedServer(http.NotFoundHandler())
	prom.Config.ErrorLog = log.New(io.Discard, "", 0) // the client's refused handshakes
	prom.StartTLS()
	t.Cleanup(prom.Close)
	// Gatus asks for credentials.
	gat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, "login at gatus")
	}))
	t.Cleanup(gat.Close)

	cfg := config.Defaults()
	cfg.PublicURL = testOrigin + "/"
	pveCfg := pluginconf.DefaultProxmox()
	pveCfg.URL, pveCfg.Node, pveCfg.TokenID, pveCfg.TokenSecret = pve.URL, "pve", "u@pve!t", token
	pveCfg.Storage.Source = pluginconf.ProxmoxStoragePrometheus
	promCfg := pluginconf.DefaultPrometheus()
	promCfg.URL = prom.URL
	gatCfg := pluginconf.DefaultGatus()
	gatCfg.URL = gat.URL
	kubeCfg := pluginconf.DefaultKubernetes()
	s := newServer(func(d *Deps) {
		d.Cfg, d.Secrets = cfg, reg
		// The plugins themselves poll the failing upstreams and publish into the hub the stream
		// serves.
		set := &plugintest.Settings{}
		set.Set(
			plugin.InstanceConfig{Name: "proxmox", Type: "proxmox", Config: &pveCfg},
			plugin.InstanceConfig{Name: "prometheus", Type: "prometheus", Config: &promCfg},
			plugin.InstanceConfig{Name: "gatus", Type: "gatus", Config: &gatCfg},
			plugin.InstanceConfig{Name: "kubernetes", Type: "kubernetes", Config: &kubeCfg},
		)
		sup := plugin.NewSupervisor(plugin.Options{
			Publisher: d.Hub, Settings: set, StatusEvery: 20 * time.Millisecond, StopTimeout: time.Second, ScrubLog: reg.Scrub,
		})
		// The Manager publishes flux from the kubernetes plugin's service, as cmd/dash wires it.
		cluster := func() (svc.Cluster, bool) {
			v, ok := sup.Service("kubernetes")
			c, isCluster := v.(svc.Cluster)
			return c, ok && isCluster
		}
		mgr := topics.New(topics.Deps{
			Cfg: cfg, Cluster: cluster, Hosts: sup, Contributors: sup.Contributors, LinkProviders: sup.Links,
		})
		if err := sup.Add(append(mgr.Plugins(), proxmox.New, prometheus.New, gatus.New, kubernetes.FactoryWithConnect(nil, fakeConnection))...); err != nil {
			t.Fatal(err)
		}
		sctx, scancel := context.WithCancel(context.Background())
		if err := sup.Run(sctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { scancel(); sup.Shutdown(context.Background()) })
		mgr.Run(sctx)
		d.Plugins = sup
		d.Cluster = cluster
	})
	s.heartbeat = 20 * time.Millisecond

	_, lines := sseClient(t, s.Handler(), "/api/v1/stream")
	var stream strings.Builder
	// proxmox, prometheus (on proxmox.storage), gatus, then the topics of the kubernetes plugin and flux
	want := []string{`"http_500"`, `"tls"`, `"auth"`, `"topic":"kubernetes.cluster"`, `"topic":"kubernetes.storage"`, `"topic":"kubernetes.apps"`, `"topic":"flux"`}
	deadline := time.After(15 * time.Second)
	for missing := true; missing; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended:\n%s", stream.String())
			}
			stream.WriteString(l + "\n")
		case <-deadline:
			t.Fatalf("the stream never showed every source category and topic %v:\n%s", want, stream.String())
		}
		missing = false
		for _, w := range want {
			missing = missing || !strings.Contains(stream.String(), w)
		}
	}

	// The kubernetes topics name the category of what failed under them, and only that.
	eventually := time.After(15 * time.Second)
	for _, c := range []struct{ topic, category string }{
		{"kubernetes.cluster", `"tls"`}, {"kubernetes.storage", `"tls"`}, {"kubernetes.apps", `"auth"`},
	} {
		for !topicLineHas(stream.String(), c.topic, c.category) {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the stream ended before %s showed %s:\n%s", c.topic, c.category, stream.String())
				}
				stream.WriteString(l + "\n")
			case <-eventually:
				t.Fatalf("%s never carried the category %s:\n%s", c.topic, c.category, stream.String())
			}
		}
	}

	for _, srv := range []string{pve.URL, prom.URL, gat.URL} {
		u, _ := url.Parse(srv)
		for _, leak := range []string{srv, u.Host} {
			if strings.Contains(stream.String(), leak) {
				t.Errorf("the stream carries %q:\n%s", leak, stream.String())
			}
		}
	}
	for _, leak := range []string{credential, "login at gatus", "internal error", "127.0.0.1"} {
		if strings.Contains(stream.String(), leak) {
			t.Errorf("the stream carries upstream text %q:\n%s", leak, stream.String())
		}
	}
}
