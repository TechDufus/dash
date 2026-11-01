package topics

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/upstream"
)

// failingTransport counts requests; any request is a test failure.
type failingTransport struct{ n atomic.Int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.n.Add(1)
	return nil, errors.New("unexpected request")
}

// A production start that configures no upstream contacts none of them, and each source says
// why it is empty: "source not configured".
func TestUnconfiguredUpstreamsMakeNoRequests(t *testing.T) {
	cfg, err := config.Load([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	rt := &failingTransport{}
	m := New(Deps{
		Cfg:   cfg,
		Prom:  upstream.NewProm(cfg.VMURL, rt),
		AM:    upstream.NewAlertmanager(cfg.AlertmanagerURL, rt),
		Gatus: upstream.NewGatus(cfg.GatusURL, rt),
		PVE:   upstream.NewProxmox(cfg.ProxmoxURL, cfg.ProxmoxNode, cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, rt),
		Hub:   hub.New(),
		Links: links.New(cfg),
	})

	ctx := context.Background()
	for _, poll := range []func(context.Context){m.pollHost, m.pollVMs, m.pollUsage, m.pollStorage, m.pollAlerts, m.pollStatus} {
		poll(ctx)
	}
	if _, err := upstream.NewVLogs(cfg.VLogsURL, rt).Query(ctx, upstream.LogQuery{Namespace: "apps"}); !errors.Is(err, upstream.ErrDisabled) {
		t.Errorf("VictoriaLogs query: %v, want ErrDisabled", err)
	}

	if n := rt.n.Load(); n != 0 {
		t.Errorf("%d upstream requests with nothing configured", n)
	}
	for _, key := range []string{
		"host:victoriametrics", "cluster:victoriametrics", "storage:victoriametrics", "vms:victoriametrics",
		"alerts:alertmanager", "status:gatus", "host:proxmox", "vms:proxmox",
	} {
		s := m.sources(key)[0]
		if s.OK || s.Error != "not_configured" {
			t.Errorf("%s: ok=%v error=%q, want not ok with %q", key, s.OK, s.Error, "not_configured")
		}
	}
}
