package topics

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

const canarySecret = "CANARYSECRET123456"

// lockedBuf is a log sink that the test's HTTP servers (which log through the default logger
// from their own goroutines) and the test can use together.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Failing upstreams show a viewer one category and nothing of the upstream: not its URL, its
// host name or port, and not what it answered. An upstream that echoes the credential back
// (some proxies do, in error pages) leaks it into neither the published sources nor the log
// (P1.4 canary tests).
func TestViewerStreamNeverCarriesUpstreamDetail(t *testing.T) {
	logged := &lockedBuf{}
	reg := secret.NewRegistry()
	old := slog.Default()
	slog.SetDefault(slog.New(secret.NewHandler(slog.NewTextHandler(logged, nil), reg)))
	t.Cleanup(func() { slog.SetDefault(old) })

	token := secret.New(canarySecret, "")
	reg.Add(token)

	// Proxmox answers 500 and echoes the Authorization header it was sent.
	pve := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error for "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(pve.Close)
	// Prometheus is behind a certificate the client does not trust.
	prom := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(prom.Close)
	// Alertmanager is gone.
	am := httptest.NewServer(http.NotFoundHandler())
	amURL := am.URL
	am.Close()
	// Gatus wants credentials.
	gatus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "login at gatus", 401) }))
	t.Cleanup(gatus.Close)

	cfg := config.Defaults()
	cfg.ProxmoxNode, cfg.HostNodename = "pve", "pve"
	m := New(Deps{
		Cfg:      cfg,
		Services: promServices(upstream.NewProm(prom.URL, nil)),
		AM:       upstream.NewAlertmanager(amURL, nil),
		Gatus:    upstream.NewGatus(gatus.URL, nil),
		PVE:      upstream.NewProxmox(pve.URL, "pve", "u@pve!t", token, nil),
		Links:    links.New(cfg),
		Secrets:  reg,
	})
	ctx := context.Background()
	m.pollHost(ctx)
	m.pollAlerts(ctx)
	m.pollStatus(ctx)

	keys := []string{"host:proxmox", "host:victoriametrics", "alerts:alertmanager", "status:gatus"}
	want := map[string]string{"proxmox": "http_500", "victoriametrics": "tls", "alertmanager": "unreachable", "gatus": "auth"}
	sources := m.sources(keys...)
	for _, s := range sources {
		if s.OK || s.Error != want[s.Name] {
			t.Errorf("%s: ok=%v error=%q, want the category %q", s.Name, s.OK, s.Error, want[s.Name])
		}
	}

	// What a viewer's stream carries is these sources and nothing more; internal/api
	// TestStreamEndpointNeverCarriesUpstreamDetail checks it through /api/stream.

	// The detail is in the log, once, with the credential scrubbed out of it.
	out := logged.String()
	if strings.Contains(out, canarySecret) {
		t.Errorf("the log carries the secret:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") || !strings.Contains(out, "category=http_500") {
		t.Errorf("want the scrubbed detail and the category in the log:\n%s", out)
	}
	before := strings.Count(out, "upstream source failed")
	m.pollHost(ctx)
	if after := strings.Count(logged.String(), "upstream source failed"); after != before {
		t.Errorf("a repeated failure with the same category logged again (%d -> %d)", before, after)
	}
}
