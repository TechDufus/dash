package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/secret"
)

// Every plugin with an upstream client hands it Host.CutLog (upstream.Cut): an upstream that
// answers an error page with a registered secret across the 200-rune cut of the error body has
// that secret redacted whole in the plugin's log line, as dash logs it (the redacting handler).
// Without the option the client cuts the secret in half and no scrubber matches the half.
func TestUpstreamErrorBodyKeepsNoPartOfASecretInEveryPlugin(t *testing.T) {
	pad := strings.Repeat("x", 200-8)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, pad+cutSecret+" in the error page")
	}))
	t.Cleanup(up.Close)
	for _, tc := range []struct {
		name, logged string
		ics          func() []plugin.InstanceConfig
		factories    []factoryOf
	}{
		{"prometheus", "upstream source failed", func() []plugin.InstanceConfig {
			p, k := pluginconf.DefaultPrometheus(), pluginconf.DefaultKubernetes()
			p.URL = up.URL
			return []plugin.InstanceConfig{{Name: "prometheus", Type: "prometheus", Config: &p}, {Name: "kubernetes", Type: "kubernetes", Config: &k}}
		}, []factoryOf{factoryFunc(prometheus.New), factoryFunc(kubernetes.FactoryWithConnect(nil, fakeConnection))}},
		{"alertmanager", "alertmanager poll failed", func() []plugin.InstanceConfig {
			c := pluginconf.DefaultAlertmanager()
			c.URL = up.URL
			return []plugin.InstanceConfig{{Name: "alertmanager", Type: "alertmanager", Config: &c}}
		}, []factoryOf{factoryFunc(alertmanager.New)}},
		{"gatus", "upstream source failed", func() []plugin.InstanceConfig {
			c := pluginconf.DefaultGatus()
			c.URL = up.URL
			return []plugin.InstanceConfig{{Name: "gatus", Type: "gatus", Config: &c}}
		}, []factoryOf{factoryFunc(gatus.New)}},
		{"proxmox", "proxmox node discovery failed", func() []plugin.InstanceConfig {
			c := pluginconf.DefaultProxmox()
			c.URL, c.TokenID, c.TokenSecret = up.URL, "dash@pve!t", secret.New("token-secret-value", "")
			return []plugin.InstanceConfig{{Name: "proxmox", Type: "proxmox", Config: &c}}
		}, []factoryOf{factoryFunc(proxmox.New)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := cutRegistry()
			logs := &syncBuf{}
			e := newPluginEnvOpt(t, func(o *plugin.Options) {
				o.ScrubLog, o.CutLog = reg.ScrubLogLine, reg.CutRunes
				o.Log = slog.New(secret.NewHandler(slog.NewTextHandler(logs, nil), reg))
			}, tc.ics(), tc.factories...)
			e.eventually(t, "the failure to be logged", func() bool { return strings.Contains(logs.String(), tc.logged) })
			out := logs.String()
			if strings.Contains(out, cutSecret[:8]) || !strings.Contains(out, pad+"[redacted]") {
				t.Errorf("the log keeps the start of the secret or misses the cut:\n%s", out)
			}
		})
	}
}
