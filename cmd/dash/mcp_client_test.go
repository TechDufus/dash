package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/secret"
)

// Static tokens of the MCP tests: audience mcp, the first read only, the second read and logs.
var (
	mcpReadToken = "dash_st_" + strings.Repeat("r", 43)
	mcpLogsToken = "dash_st_" + strings.Repeat("l", 43)
	mcpAPIToken  = "dash_st_" + strings.Repeat("a", 43)
)

// startMCPDemo runs dash as main does, in demo mode with MCP on and the static tokens above;
// env adds to the environment. It returns the API address.
func startMCPDemo(t *testing.T, env ...string) string {
	t.Helper()
	cleanEnv(t)
	tokens, _ := json.Marshal([]map[string]any{
		{"name": "agent", "role": "viewer", "audience": "mcp", "scopes": []string{"read"}, "hash": credentials.HashToken(mcpReadToken)},
		{"name": "logs", "role": "viewer", "audience": "mcp", "scopes": []string{"read", "logs"}, "hash": credentials.HashToken(mcpLogsToken)},
		{"name": "api", "role": "viewer", "audience": "api", "scopes": []string{"read"}, "hash": credentials.HashToken(mcpAPIToken)},
	})
	startEnv(t, filepath.Join(t.TempDir(), "data"), append([]string{
		"DASH_DEMO=true", "DASH_MCP_ENABLED=true", "DASH_AUTH_TOKENS=" + string(tokens),
		"KUBECONFIG=", "KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=",
		"DASH_SERVER_LISTEN=" + anyPort, "DASH_SERVER_HEALTH_LISTEN=" + anyPort,
	}, env...)...)
	type addrs struct{ api, health string }
	bound := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(secret.NewRegistry(), func(a, h net.Addr) { bound <- addrs{a.String(), h.String()} })
	}()
	var a addrs
	select {
	case a = <-bound:
	case err := <-done:
		t.Fatalf("run returned before binding: %v", err)
	}
	t.Cleanup(func() {
		waitFor(t, func() bool { c, _ := fetch(a.health, "/healthz", false); return c == http.StatusOK })
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run returned %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after SIGTERM")
		}
	})
	waitFor(t, func() bool { c, _ := fetch(a.health, "/readyz", false); return c == http.StatusOK })
	return a.api
}

// bearerTransport sends every request with a bearer token.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession connects the official Go SDK client to addr with token at protocol version.
func mcpSession(t *testing.T, addr, token, version string) *sdk.ClientSession {
	t.Helper()
	c := sdk.NewClient(&sdk.Implementation{Name: "dash-test", Version: "1"}, nil)
	tr := &sdk.StreamableClientTransport{
		Endpoint: "http://" + addr + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}, DisableStandaloneSSE: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, tr, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect at %s: %v", version, err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// The P7.3 acceptance: a go-sdk client against demo mode with a static mcp token lists the tools
// and calls dash_overview, on both protocol versions dash serves.
func TestMCPGoSDKClientAgainstDemoMode(t *testing.T) {
	addr := startMCPDemo(t)
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			cs := mcpSession(t, addr, mcpReadToken, version)
			ctx := context.Background()
			tools, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(tools.Tools) == 0 || tools.Tools[0].Name != "dash_overview" {
				t.Fatalf("tools = %+v", tools.Tools)
			}
			var res *sdk.CallToolResult
			waitFor(t, func() bool {
				res, err = cs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_overview"})
				return err != nil || !res.IsError // the overview is composed shortly after readiness
			})
			if err != nil || res.IsError {
				t.Fatalf("dash_overview = %+v, %v", res, err)
			}
			var ov struct {
				Status    string           `json:"status"`
				KPIs      []map[string]any `json:"kpis"`
				Attention []map[string]any `json:"attention"`
			}
			raw, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(raw, &ov); err != nil || ov.Status == "" || len(ov.KPIs) == 0 || len(ov.Attention) > 50 {
				t.Errorf("overview = %s (%v)", raw, err)
			}
			text := res.Content[0].(*sdk.TextContent).Text
			if !strings.HasPrefix(text, "Untrusted lab data follows") {
				t.Errorf("text = %.200s", text)
			}
		})
	}
}
