package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/secret"
)

// k8sCanary holds a quote and an ampersand: a leak through any escaping (JSON, URL, shell) still
// shows the CANARY-4f1c part.
const k8sCanary = `s3cr&t"CANARY-4f1c-0123456789`

const (
	pveRef  = "k8s:dash/pve-token#secret"
	promRef = "k8s:monitoring/prom-creds#bearer"
)

// k8sConfig is a configuration whose Proxmox token and Prometheus bearer token are read from
// Kubernetes Secrets. Prometheus has no url: only the Proxmox row can start.
func k8sConfig(pveURL string) string {
	return "plugins:\n" +
		"  proxmox:\n    url: " + pveURL + "\n    tokenID: u@pve!t\n    tokenSecret: " + pveRef + "\n" +
		"  prometheus:\n    auth:\n      bearerToken: " + promRef + "\n"
}

// k8sProcess is run's wiring of the references: one K8sRefs, the registry that scrubs, the
// framework and the reloader's loop, with the real plugins of registry.
type k8sProcess struct {
	reg    *secret.Registry
	refs   *secret.K8sRefs
	fw     *framework
	srv    *api.Server
	cfg    config.Config
	logs   *lockedBuf
	cancel context.CancelFunc
}

func startK8sProcess(t *testing.T, cfgBody string, env ...string) *k8sProcess {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	writeTestFile(t, cfgPath, cfgBody)
	startEnv(t, dir, append([]string{"DASH_CONFIG=" + cfgPath}, env...)...)

	p := &k8sProcess{logs: &lockedBuf{}}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(p.logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	p.reg = secret.NewRegistry()
	p.refs = secret.NewK8sRefs(p.reg)
	res, store, err := loadConfig(p.reg, p.refs)
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
	}
	p.cfg = res.Config
	p.fw = newFramework(res, store, config.ReloadOptions{Interval: time.Millisecond}, p.reg, p.refs, nil, p.cfg.Demo)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	ov := overview.New(overview.Deps{Hosts: p.fw.sup, Contributors: p.fw.sup.Contributors})
	if err := p.fw.start(ctx, registry(p.refs), func() { ov.Run(ctx) }); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); p.fw.reloader.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		p.fw.sup.Shutdown(context.Background())
	})
	p.srv = api.New(api.Deps{Cfg: p.cfg, Hub: p.fw.hub, Secrets: p.reg, Health: p.fw.sup, Plugins: p.fw.sup, Auth: authtest.Proxy("alice")})
	p.srv.MarkServing()
	return p
}

func (p *k8sProcess) row(t *testing.T, id string) model.PluginInfo {
	t.Helper()
	for _, in := range p.fw.sup.Infos() {
		if in.ID == id {
			return in
		}
	}
	t.Fatalf("no row for %s", id)
	return model.PluginInfo{}
}

// everythingViewersSee is the plugin rows and every published topic as JSON, as a viewer's
// stream would carry them.
func (p *k8sProcess) everythingViewersSee(t *testing.T) string {
	t.Helper()
	rows, err := json.Marshal(p.fw.sup.Infos())
	if err != nil {
		t.Fatal(err)
	}
	snap, _, cancel := p.fw.hub.Subscribe("", nil)
	cancel()
	var sb strings.Builder
	sb.Write(rows)
	for _, ev := range snap {
		sb.WriteString("\n" + ev.Topic + " ")
		sb.Write(ev.Data)
	}
	return sb.String()
}

// A k8s: reference in a secret field loads; the plugin that needs it is not configured and its
// row says only that it waits for a Kubernetes Secret, naming the setting and nothing of the
// Secret.
func TestK8sRefInConfigLoadsAsPendingRow(t *testing.T) {
	pve := newFakePVE(t)
	p := startK8sProcess(t, k8sConfig(pve.URL))

	for _, id := range []string{"proxmox", "prometheus"} {
		waitFor(t, func() bool { return strings.Contains(p.row(t, id).Message, "waiting for a Kubernetes Secret") })
	}
	px := p.row(t, "proxmox")
	if px.State != model.PluginDisabled && px.State != model.PluginUnconfigured {
		t.Errorf("proxmox is %s, want disabled or unconfigured", px.State)
	}
	if !strings.Contains(px.Message, "(see plugins.proxmox.tokenSecret)") {
		t.Errorf("proxmox row = %q", px.Message)
	}
	if got := p.row(t, "prometheus").Message; !strings.Contains(got, "(see plugins.prometheus.auth.bearerToken)") {
		t.Errorf("prometheus row = %q", got)
	}
	seen := p.everythingViewersSee(t)
	for _, leak := range []string{"pve-token", "prom-creds", "k8s:", "#secret", "#bearer", "monitoring/"} {
		if strings.Contains(seen, leak) {
			t.Errorf("what viewers see names %q:\n%s", leak, seen)
		}
	}
	if p.fw.sup.Running("proxmox") || pve.lastAuth() != "" {
		t.Error("proxmox started without its token")
	}
}

// Filling the handle (what the kubernetes plugin's watcher does) starts the consumer through one
// Reload, and the value reaches the plugin's requests and nothing a viewer or a log carries.
func TestK8sRefFillStartsTheConsumer(t *testing.T) {
	pve := newFakePVE(t)
	p := startK8sProcess(t, k8sConfig(pve.URL))
	var fired atomic.Int32
	p.refs.OnChange(func() { fired.Add(1) })
	waitFor(t, func() bool { return strings.Contains(p.row(t, "proxmox").Message, "waiting for a Kubernetes Secret") })

	p.refs.Fill(pveRef, k8sCanary)
	waitFor(t, func() bool { return p.fw.sup.Running("proxmox") })
	if n := fired.Load(); n != 1 {
		t.Errorf("a fill must make exactly one change, got %d", n)
	}
	if n := strings.Count(p.logs.String(), `plugin started" plugin=proxmox`); n != 1 {
		t.Errorf("proxmox started %d times:\n%s", n, p.logs.String())
	}

	detail := func() int {
		r := httptest.NewRequest(http.MethodGet, p.cfg.PublicURL+"/api/v1/plugins/proxmox/vms/101", nil)
		r.RemoteAddr = "127.0.0.1:41000" // the proxy next to dash, the peer whose identity header dash believes
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		rec := httptest.NewRecorder()
		p.srv.Handler().ServeHTTP(rec, r)
		return rec.Code
	}
	waitFor(t, func() bool { return detail() == http.StatusOK })
	if got := pve.lastAuth(); got != "PVEAPIToken=u@pve!t="+k8sCanary {
		t.Errorf("the plugin's request carries %d bytes, want the Secret's value", len(got))
	}

	// A rotation reaches the next request with no restart and no change event.
	p.refs.Fill(pveRef, k8sCanary+"-rotated")
	waitFor(t, func() bool { return detail() == http.StatusOK && strings.HasSuffix(pve.lastAuth(), "-rotated") })
	if n := fired.Load(); n != 1 {
		t.Errorf("a rotation changed %d times in all, want 1", n)
	}
	if n := strings.Count(p.logs.String(), `plugin started" plugin=proxmox`); n != 1 {
		t.Errorf("a rotation restarted proxmox: %d starts", n)
	}

	// The canary is nowhere a viewer or an operator reads: not the plugin rows, nor any topic
	// (core.plugins among them), nor the log.
	seen := p.everythingViewersSee(t)
	if !strings.Contains(seen, "core.plugins") {
		t.Fatalf("no core.plugins topic:\n%s", seen)
	}
	for name, text := range map[string]string{"stream": seen, "log": p.logs.String()} {
		if strings.Contains(text, "CANARY-4f1c") {
			t.Errorf("the %s shows the secret:\n%s", name, text)
		}
	}
	if got := p.reg.ScrubLogLine("failed: " + k8sCanary); strings.Contains(got, "CANARY-4f1c") {
		t.Errorf("the registry does not scrub the handle: %q", got)
	}
}

// Demo mode needs no reference read: a k8s: reference in its configuration loads, and the
// plugins run as their twins.
func TestDemoModeAcceptsK8sRefs(t *testing.T) {
	p := startK8sProcess(t, k8sConfig("https://pve.example.com"), "DASH_DEMO=true")
	waitFor(t, func() bool { return p.fw.sup.Running("proxmox") })
	if got := p.row(t, "proxmox").Message; strings.Contains(got, "k8s:") || strings.Contains(got, "pve-token") {
		t.Errorf("proxmox row = %q", got)
	}
}

// dash config validate checks the syntax of a k8s: reference and reads nothing.
func TestValidateChecksK8sRefSyntaxOnly(t *testing.T) {
	cleanEnv(t)
	for _, args := range [][]string{nil, {"--resolve-secrets"}} {
		p := writeFile(t, k8sConfig("https://pve.example.com"))
		out, err := validate(t, append([]string{p}, args...)...)
		if err != nil || !strings.Contains(out, "configuration valid") {
			t.Errorf("%v: %v\n%s", args, err, out)
		}
	}

	for name, bad := range map[string]string{
		"no key":        "k8s:media/not-a-ref-SECRET",
		"bad namespace": "k8s:Media_NS/secretname-xyz#key",
		"bad key":       "k8s:media/secretname-xyz#k e y",
	} {
		p := writeFile(t, "plugins:\n  proxmox:\n    url: https://pve.example.com\n    tokenID: u@pve!t\n    tokenSecret: "+bad+"\n")
		out, err := validate(t, p)
		if err == nil || exitCode(err) != 1 {
			t.Errorf("%s: err = %v\n%s", name, err, out)
		}
		if !strings.Contains(out, "plugins.proxmox.tokenSecret") {
			t.Errorf("%s: the report must name the field:\n%s", name, out)
		}
		for _, echoed := range []string{"not-a-ref-SECRET", "Media_NS", "secretname-xyz", "k e y"} {
			if strings.Contains(out, echoed) {
				t.Errorf("%s: the report echoes the reference (%q):\n%s", name, echoed, out)
			}
		}
	}
}
