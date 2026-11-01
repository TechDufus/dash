package topics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/secret"
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
		Gatus: upstream.NewGatus(cfg.GatusURL, rt),
		PVE:   upstream.NewProxmox(cfg.ProxmoxURL, cfg.ProxmoxNode, cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, rt),
		Links: links.New(cfg),
	})

	ctx := context.Background()
	for _, poll := range []func(context.Context){m.pollHost, m.pollVMs, m.pollUsage, m.pollStorage, m.pollStatus} {
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
		"status:gatus", "host:proxmox", "vms:proxmox",
	} {
		s := m.sources(key)[0]
		if s.OK || s.Error != "not_configured" {
			t.Errorf("%s: ok=%v error=%q, want not ok with %q", key, s.OK, s.Error, "not_configured")
		}
	}
}

// The scrub and NAS-backup queries need their conventions. With Prometheus configured but the
// conventions empty none of those series is requested; with them set, each is (P1.3).
func TestScrubAndNASQueriesNeedTheirConventions(t *testing.T) {
	run := func(c config.Conventions) string {
		var mu sync.Mutex
		var queries strings.Builder
		prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			mu.Lock()
			queries.WriteString(r.PostForm.Get("query") + "\n")
			mu.Unlock()
			io.WriteString(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		}))
		defer prom.Close()
		cfg := config.Defaults()
		cfg.HostNodename, cfg.Conventions = "pve-a", c
		m := New(Deps{
			Cfg: cfg, Services: promServices(upstream.NewProm(prom.URL, nil)), PVE: upstream.NewProxmox("", "", "", secret.Value{}, nil),
			Links: links.New(cfg),
		})
		m.pollHost(context.Background())
		m.pollStorage(context.Background())
		mu.Lock()
		defer mu.Unlock()
		return queries.String()
	}

	got := run(config.Conventions{})
	if !strings.Contains(got, `nodename="pve-a"`) {
		t.Fatalf("Prometheus was not queried at all:\n%s", got)
	}
	for _, banned := range []string{"scrub", "kube_cronjob_status_last_successful_time"} {
		if strings.Contains(got, banned) {
			t.Errorf("no convention set, yet %q was queried:\n%s", banned, got)
		}
	}

	got = run(config.Conventions{ZFSScrubMetricPrefix: "example_zfs_scrub", NASBackupNamespace: "backup", NASBackupCronJob: "nas-backup"})
	for _, want := range []string{
		"example_zfs_scrub_last_end_timestamp_seconds", "example_zfs_scrub_last_errors", "example_zfs_scrub_in_progress",
		`kube_cronjob_status_last_successful_time{namespace="backup",cronjob=~"nas-backup(-.+)?"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("conventions set, yet %q was not queried:\n%s", want, got)
		}
	}
}

// The host data says whether scrub data is tracked at all, so the SPA hides the scrub rows
// instead of showing every pool as "never scrubbed" while no prefix is configured.
func TestHostDataSaysWhetherScrubIsTracked(t *testing.T) {
	for prefix, want := range map[string]bool{"": false, "example_zfs_scrub": true} {
		cfg := config.Defaults()
		cfg.HostNodename, cfg.Conventions.ZFSScrubMetricPrefix = "pve-a", prefix
		rt := &failingTransport{} // the queries fail; the flag does not depend on their answer
		m := New(Deps{
			Cfg: cfg, Services: promServices(upstream.NewProm("http://prom.example.com", rt)), PVE: upstream.NewProxmox("", "", "", secret.Value{}, nil),
			Links: links.New(cfg),
		})
		m.pollHost(context.Background())
		if got := m.buildHost().ScrubTracked; got != want {
			t.Errorf("prefix %q: ScrubTracked = %v, want %v", prefix, got, want)
		}
	}
}
