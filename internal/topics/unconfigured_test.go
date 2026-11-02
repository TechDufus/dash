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
	// No prometheus plugin runs and nothing is configured: the pollers have no upstream to ask.
	m := New(Deps{Cfg: cfg})

	ctx := context.Background()
	for _, poll := range []func(context.Context){m.pollUsage, m.pollStorage} {
		poll(ctx)
	}
	for _, key := range []string{
		"cluster:victoriametrics", "storage:victoriametrics",
	} {
		s := m.sources(key)[0]
		if s.OK || s.Error != "not_configured" {
			t.Errorf("%s: ok=%v error=%q, want not ok with %q", key, s.OK, s.Error, "not_configured")
		}
	}
	// With the gatus plugin not running the Manager's stand-in keeps the chip, not configured.
	if g := m.endpoints().SourceStatuses(); len(g) != 1 || g[0].Name != "gatus" || g[0].OK || g[0].Error != "not_configured" {
		t.Errorf("gatus source = %+v, want one not_configured chip", g)
	}
}

// The NAS-backup query needs its convention. With Prometheus configured but the convention empty
// that series is not requested; with it set, it is (P1.3). The ZFS scrub series are the hardware
// plugin's, which has the same rule (internal/plugins/hardware TestEmptyScrubPrefixQueriesNoScrubData).
func TestNASQueryNeedsItsConvention(t *testing.T) {
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
		cfg.Conventions = c
		m := New(Deps{
			Cfg: cfg, Services: promServices(upstream.NewProm(prom.URL, nil)),
		})
		m.pollStorage(context.Background())
		mu.Lock()
		defer mu.Unlock()
		return queries.String()
	}

	got := run(config.Conventions{})
	if !strings.Contains(got, "kubelet_volume_stats") {
		t.Fatalf("Prometheus was not queried at all:\n%s", got)
	}
	if strings.Contains(got, "kube_cronjob_status_last_successful_time") {
		t.Errorf("no convention set, yet the NAS backup was queried:\n%s", got)
	}

	got = run(config.Conventions{NASBackupNamespace: "backup", NASBackupCronJob: "nas-backup"})
	if want := `kube_cronjob_status_last_successful_time{namespace="backup",cronjob=~"nas-backup(-.+)?"}`; !strings.Contains(got, want) {
		t.Errorf("convention set, yet %q was not queried:\n%s", want, got)
	}
}
