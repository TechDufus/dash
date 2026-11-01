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
	"k8s.io/client-go/kubernetes/fake"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/kube"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// Failing upstreams show a viewer's /api/stream one category and nothing of the upstream: the
// real topic manager polls them, publishes into the hub, and the SSE handler serves what a
// browser would read. Not their URLs, host names or ports, not what they answered, and not the
// credential an upstream echoes back (P1.4 canary test, end to end).
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
	// Prometheus is behind a certificate the client does not trust.
	prom := httptest.NewUnstartedServer(http.NotFoundHandler())
	prom.Config.ErrorLog = log.New(io.Discard, "", 0) // the client's refused handshakes
	prom.StartTLS()
	t.Cleanup(prom.Close)

	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range []schema.GroupVersionResource{
		kube.GVRKustomizations, kube.GVRHelmReleases, kube.GVRGitRepositories, kube.GVROCIRepositories, kube.GVRHelmRepositories,
		kube.GVRCNPGClusters, kube.GVRCertificates, kube.GVRExternalSecrets, kube.GVRHTTPRoutes,
	} {
		listKinds[gvr] = gvr.Resource + "List"
	}
	cluster := kube.NewFromClients(fake.NewClientset(), dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds))

	cfg := config.Defaults()
	cfg.PublicURL, cfg.ProxmoxNode, cfg.HostNodename = testOrigin+"/", "pve", "pve"
	promClient := upstream.NewProm(prom.URL, nil)
	gatus := &authGatus{}
	s := newServer(func(d *Deps) {
		d.Cfg, d.Secrets = cfg, reg
		m := topics.New(topics.Deps{
			Cfg: cfg, Cluster: cluster, Hosts: plugintest.AllRunning(d.Hub, topics.LegacyManifests()...), Links: links.New(cfg), Secrets: reg,
			// The prometheus and gatus plugins' services: a plain client and a Gatus that wants
			// credentials, read by the Manager through Services.
			Services: func(id plugin.ID) (any, bool) {
				switch id {
				case "prometheus":
					return promClient, true
				case "gatus":
					return gatus, true
				}
				return nil, false
			},
			PVE: upstream.NewProxmox(pve.URL, "pve", "u@pve!t", token, nil),
		})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		m.Run(ctx)
		d.Data = m
	})
	s.heartbeat = 20 * time.Millisecond

	_, lines := sseClient(t, s.Handler(), "/api/v1/stream")
	var stream strings.Builder
	want := []string{`"http_500"`, `"tls"`, `"auth"`} // proxmox, victoriametrics, gatus (on apps and vms)
	deadline := time.After(15 * time.Second)
	for missing := true; missing; {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("the stream ended:\n%s", stream.String())
			}
			stream.WriteString(l + "\n")
		case <-deadline:
			t.Fatalf("the stream never showed every source category %v:\n%s", want, stream.String())
		}
		missing = false
		for _, w := range want {
			missing = missing || !strings.Contains(stream.String(), w)
		}
	}

	for _, srv := range []string{pve.URL, prom.URL} {
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

// authGatus is the gatus plugin's service while Gatus asks for credentials: it has polled once
// (Revision above 0, which opens the apps gate) and its source reads "auth".
type authGatus struct{}

func (*authGatus) ByHost(string) (model.Health, bool) { return "", false }
func (*authGatus) ByName(string) (up, known bool)     { return false, false }
func (*authGatus) Revision() uint64                   { return 1 }
func (*authGatus) SourceStatuses() []model.SourceStatus {
	return []model.SourceStatus{{Name: "gatus", Error: upstream.CategoryAuth, CheckedAt: time.Now().UTC()}}
}

var _ svc.EndpointHealth = (*authGatus)(nil)
