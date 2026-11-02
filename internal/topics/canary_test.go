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
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// bearer sends a credential with every request, as an authenticated client does.
type bearer struct{ token secret.Value }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token.Reveal())
	return http.DefaultTransport.RoundTrip(r)
}

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

	// Prometheus answers 500 and echoes the Authorization header it was sent. (The Proxmox half of
	// this canary is the proxmox plugin's: internal/plugins/proxmox TestProxmoxDoesNotLeakTheTokenOrTheURL.)
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "internal error for "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(prom.Close)

	cfg := config.Defaults()
	m := New(Deps{
		Cfg:      cfg,
		Services: promServices(upstream.NewProm(prom.URL, bearer{token})),
		Secrets:  reg,
	})
	ctx := context.Background()
	m.pollUsage(ctx)

	keys := []string{"cluster:victoriametrics"}
	want := map[string]string{"victoriametrics": "http_500"}
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
	m.pollUsage(ctx)
	if after := strings.Count(logged.String(), "upstream source failed"); after != before {
		t.Errorf("a repeated failure with the same category logged again (%d -> %d)", before, after)
	}
}
