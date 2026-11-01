package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

const testToken = "dash@pve!ci=00000000-0000-0000-0000-000000000000"

var testTokenID, testTokenSecret, _ = strings.Cut(testToken, "=")

// pveEnv is the environment of a configured Proxmox token plus the given name/value pairs.
func pveEnv(kv ...string) map[string]string {
	env := map[string]string{"DASH_PLUGINS_PROXMOX_TOKEN_ID": testTokenID, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET": testTokenSecret}
	for i := 0; i+1 < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}

// fakePVE serves GET /api2/json/nodes, counting requests and recording Authorization headers.
// reply gets the 1-based call number and returns the status and body.
type fakePVE struct {
	*httptest.Server
	mu    sync.Mutex
	calls int
	auth  []string
}

func newFakePVE(t *testing.T, reply func(call int) (int, string)) *fakePVE {
	t.Helper()
	f := &fakePVE{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		call := f.calls
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if r.URL.Path != "/api2/json/nodes" {
			http.NotFound(w, r)
			return
		}
		code, body := reply(call)
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePVE) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func nodesBody(names ...string) string {
	items := make([]string, len(names))
	for i, n := range names {
		items[i] = fmt.Sprintf(`{"node":%q,"status":"online"}`, n)
	}
	return `{"data":[` + strings.Join(items, ",") + `]}`
}

func always(code int, body string) func(int) (int, string) {
	return func(int) (int, string) { return code, body }
}

func TestResolveProxmoxNode(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string // DASH_PLUGINS_PROXMOX_URL is the fake's URL unless noURL
		noURL     bool
		reply     func(call int) (int, string)
		budget    time.Duration
		wantErr   string // substring; "" means success
		wantCalls int
		wantNode  string
		wantHost  string
	}{
		{
			name:      "one node is discovered for node and nodename",
			env:       pveEnv(),
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 1, wantNode: "pve-a", wantHost: "pve-a",
		},
		{
			name:      "explicit nodename is kept",
			env:       pveEnv("DASH_PLUGINS_HARDWARE_HOSTS", `[{"nodename":"box"}]`),
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 1, wantNode: "pve-a", wantHost: "box",
		},
		{
			name:      "explicit node skips discovery",
			env:       pveEnv("DASH_PLUGINS_PROXMOX_NODE", "pve-b"),
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 0, wantNode: "pve-b", wantHost: "pve-b",
		},
		{
			name:      "explicit node and nodename skip discovery",
			env:       pveEnv("DASH_PLUGINS_PROXMOX_NODE", "pve-b", "DASH_PLUGINS_HARDWARE_HOSTS", `[{"nodename":"box"}]`),
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 0, wantNode: "pve-b", wantHost: "box",
		},
		{
			name:      "no token is a no-op",
			env:       map[string]string{},
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 0, wantNode: "", wantHost: "",
		},
		{
			name:      "no url is a no-op",
			env:       pveEnv(),
			noURL:     true,
			reply:     always(200, nodesBody("pve-a")),
			budget:    5 * time.Second,
			wantCalls: 0, wantNode: "", wantHost: "",
		},
		{
			name:      "several nodes fail without retry",
			env:       pveEnv(),
			reply:     always(200, nodesBody("pve-b", "pve-a")),
			budget:    5 * time.Second,
			wantErr:   "plugins.proxmox.node",
			wantCalls: 1,
		},
		{
			name:      "zero nodes fail without retry",
			env:       pveEnv(),
			reply:     always(200, `{"data":[]}`),
			budget:    5 * time.Second,
			wantErr:   "plugins.proxmox.node",
			wantCalls: 1,
		},
		{
			name: "transient error is retried",
			env:  pveEnv(),
			reply: func(call int) (int, string) {
				if call == 1 {
					return 500, `{"data":null}`
				}
				return 200, nodesBody("pve-a")
			},
			budget:    5 * time.Second,
			wantCalls: 2, wantNode: "pve-a", wantHost: "pve-a",
		},
		{
			name:    "persistent error gives up after the budget",
			env:     pveEnv(),
			reply:   always(500, `{"data":null}`),
			budget:  200 * time.Millisecond,
			wantErr: "plugins.proxmox.node",
			// attempt 1 fails, the wait is capped at the budget, attempt 2 fails and ends it.
			wantCalls: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePVE(t, tc.reply)
			// A token without a URL is not a valid configuration (Load refuses it), but the
			// no-op must hold for a Config built by hand.
			cfg := config.Config{ProxmoxTokenID: testTokenID, ProxmoxTokenSecret: secret.New(testTokenSecret, "")}
			if !tc.noURL {
				env := []string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_PLUGINS_PROXMOX_URL=" + f.URL}
				for k, v := range tc.env {
					env = append(env, k+"="+v)
				}
				var err error
				if cfg, err = config.Load(env); err != nil {
					t.Fatal(err)
				}
			}

			start := time.Now()
			err := resolveProxmoxNode(context.Background(), &cfg, upstream.NewProxmox(cfg.ProxmoxURL, "", cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, nil).Nodes, tc.budget)
			elapsed := time.Since(start)

			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v, want it to mention %q", err, tc.wantErr)
			}
			if got := f.count(); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
			if cfg.ProxmoxNode != tc.wantNode || cfg.HostNodename != tc.wantHost {
				t.Errorf("node = %q, nodename = %q; want %q, %q", cfg.ProxmoxNode, cfg.HostNodename, tc.wantNode, tc.wantHost)
			}
			for _, a := range f.auth {
				if a != "PVEAPIToken="+testToken {
					t.Errorf("Authorization = %q, want the PVEAPIToken header", a)
				}
			}
			if tc.budget < time.Second && elapsed > tc.budget+time.Second {
				t.Errorf("took %v, want about the %v budget", elapsed, tc.budget)
			}
		})
	}
}

func TestResolveProxmoxNodeSeveralListsNames(t *testing.T) {
	f := newFakePVE(t, always(200, nodesBody("pve-b", "pve-a")))
	cfg, err := config.Load([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_PLUGINS_PROXMOX_TOKEN_ID=" + testTokenID, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + testTokenSecret, "DASH_PLUGINS_PROXMOX_URL=" + f.URL})
	if err != nil {
		t.Fatal(err)
	}
	err = resolveProxmoxNode(context.Background(), &cfg, upstream.NewProxmox(cfg.ProxmoxURL, "", cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, nil).Nodes, time.Second)
	if err == nil || !strings.Contains(err.Error(), "pve-a, pve-b") {
		t.Fatalf("error %v, want the sorted node names", err)
	}
}

func TestResolveProxmoxNodeWrapsLastError(t *testing.T) {
	boom := errors.New("boom")
	cfg := config.Config{ProxmoxTokenID: testTokenID, ProxmoxTokenSecret: secret.New(testTokenSecret, ""), ProxmoxURL: "https://pve.example.com:8006"}
	err := resolveProxmoxNode(context.Background(), &cfg, func(context.Context) ([]string, error) { return nil, boom }, time.Millisecond)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "plugins.proxmox.node") {
		t.Fatalf("error %v, want it to wrap boom and name plugins.proxmox.node", err)
	}
}

func TestResolveProxmoxNodeCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := config.Config{ProxmoxTokenID: testTokenID, ProxmoxTokenSecret: secret.New(testTokenSecret, ""), ProxmoxURL: "https://pve.example.com:8006"}
	calls := 0
	list := func(context.Context) ([]string, error) {
		calls++
		cancel() // SIGTERM arrives while dash waits to retry
		return nil, errors.New("unavailable")
	}
	start := time.Now()
	err := resolveProxmoxNode(ctx, &cfg, list, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error %v, want context.Canceled", err)
	}
	if calls != 1 || time.Since(start) > time.Second {
		t.Errorf("calls = %d after %v, want one call and a prompt return", calls, time.Since(start))
	}
}
