package main

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The 121st call within a minute is rate-limited (P7.3 acceptance), and dash_logs has its own
// 30 a minute.
func TestMCPRateLimits(t *testing.T) {
	addr := startMCPDemo(t)
	ctx := context.Background()
	cs := mcpSession(t, addr, mcpReadToken, "2026-07-28")
	for i := range 120 {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_search", Arguments: map[string]any{"query": "x"}})
		if err != nil || res.IsError {
			t.Fatalf("call %d: %+v, %v", i+1, res, err)
		}
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_search", Arguments: map[string]any{"query": "x"}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
		t.Errorf("the 121st call: %+v, %v", res, err)
	}

	logs := mcpSession(t, addr, mcpLogsToken, "2026-07-28")
	args := map[string]any{"namespace": "media", "pod": "nothing-here", "limit": 1}
	for i := range 30 {
		res, err := logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_logs", Arguments: args})
		if err != nil || (res.IsError && strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited")) {
			t.Fatalf("logs call %d: %+v, %v", i+1, res, err)
		}
	}
	res, err = logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_logs", Arguments: args})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
		t.Errorf("the 31st dash_logs call: %+v, %v", res, err)
	}
	if res, err := logs.CallTool(ctx, &sdk.CallToolParams{Name: "dash_overview"}); err != nil || strings.Contains(res.Content[0].(*sdk.TextContent).Text, "rate_limited") {
		t.Errorf("other tools are held back by the dash_logs limit: %+v, %v", res, err)
	}
}
