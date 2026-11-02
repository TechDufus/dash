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

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/secret"
)

// Failing upstreams show a viewer's /api/stream one category and nothing of the upstream: real
// plugins poll them, publish into the hub, and the SSE handler serves what a browser would
// read. Not their URLs, host names or ports, not what they answered, and not the credential an
// upstream echoes back (P1.4 canary test, end to end).
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
	// topic, read through it, is what shows it.
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
	s := newServer(func(d *Deps) {
		d.Cfg, d.Secrets = cfg, reg
		// The plugins themselves poll the failing upstreams and publish into the hub the stream
		// serves.
		set := &plugintest.Settings{}
		set.Set(
			plugin.InstanceConfig{Name: "proxmox", Type: "proxmox", Config: &pveCfg},
			plugin.InstanceConfig{Name: "prometheus", Type: "prometheus", Config: &promCfg},
			plugin.InstanceConfig{Name: "gatus", Type: "gatus", Config: &gatCfg},
		)
		sup := plugin.NewSupervisor(plugin.Options{
			Publisher: d.Hub, Settings: set, StatusEvery: 20 * time.Millisecond, StopTimeout: time.Second, ScrubLog: reg.Scrub,
		})
		if err := sup.Add(proxmox.New, prometheus.New, gatus.New); err != nil {
			t.Fatal(err)
		}
		sctx, scancel := context.WithCancel(context.Background())
		if err := sup.Run(sctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { scancel(); sup.Shutdown(context.Background()) })
		d.Plugins = sup
	})
	s.heartbeat = 20 * time.Millisecond

	_, lines := sseClient(t, s.Handler(), "/api/v1/stream")
	var stream strings.Builder
	want := []string{`"http_500"`, `"tls"`, `"auth"`} // proxmox, prometheus (on proxmox.storage), gatus
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
