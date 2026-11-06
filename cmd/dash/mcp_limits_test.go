package main

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Every request but initialize counts against the 120 a minute (P7.3 acceptance): the client's
// own discovery as well as its calls. dash_logs has its own 30 a minute.
func TestMCPRateLimits(t *testing.T) {
	addr := startMCPDemo(t)
	ctx := context.Background()
	cs := mcpSession(t, addr, mcpReadToken, "2026-07-28")
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for ; calls < 121; calls++ {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_search", Arguments: map[string]any{"query": "x"}})
		if err != nil {
			t.Fatalf("call %d: %v", calls+1, err)
		}
		if res.IsError {
			if !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
				t.Fatalf("call %d: %+v", calls+1, res)
			}
			break
		}
	}
	// The session's own requests count too: its discovery and the tools/list above.
	const want = 120 - 2
	if calls != want {
		t.Errorf("%d calls passed after the session's own requests, want %d", calls, want)
	}

	logs := mcpSession(t, addr, mcpLogsToken, "2026-07-28")
	args := map[string]any{"namespace": "media", "pod": "nothing-here", "limit": 1}
	for i := range 30 {
		res, err := logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_logs", Arguments: args})
		if err != nil || (res.IsError && strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited")) {
			t.Fatalf("logs call %d: %+v, %v", i+1, res, err)
		}
	}
	res, err := logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_logs", Arguments: args})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
		t.Errorf("the 31st dash_logs call: %+v, %v", res, err)
	}
	if res, err := logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_overview"}); err != nil || strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
		t.Errorf("other tools are held back by the dash_logs limit: %+v, %v", res, err)
	}
}
