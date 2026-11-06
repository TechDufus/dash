package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/model"
)

// The P7.5 acceptance end to end (contract G7-R6): dash runs as main does, in demo mode with MCP
// on. The demo person creates a personal access token with the mcp audience and
// actions:kubernetes through the API; a go-sdk client with it calls the kubernetes_restart tool,
// which answers pending_approval and runs nothing; the same person approves the request through
// the API; the restart runs exactly once (one audit outcome naming the approval); and
// dash_approval returns the outcome. A protected namespace needs the workload's name typed: 400
// without it, approved with it.
func TestApprovalEndToEnd(t *testing.T) {
	var logs bytes.Buffer
	var logMu sync.Mutex
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(lockedWriter{&logMu, &logs}, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	addr := startMCPDemo(t)
	client := &http.Client{Timeout: 30 * time.Second}
	api := func(method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, "http://"+addr+path, rd)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if method != http.MethodGet {
			req.Header.Set("Origin", "https://dash.example.com")
			req.Header.Set("X-Dash-Csrf", "1")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if out != nil && resp.StatusCode < 300 {
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatalf("%s %s: %v in %s", method, path, err, b)
			}
		}
		return resp.StatusCode
	}

	var me model.Me
	if code := api(http.MethodGet, "/api/v1/me", nil, &me); code != http.StatusOK || !me.Capabilities.Approvals {
		t.Fatalf("/me = %d %+v, want capabilities.approvals", code, me.Capabilities)
	}
	var created model.TokenCreated
	if code := api(http.MethodPost, "/api/v1/tokens", model.TokenCreateRequest{
		Name: "agent", Audience: model.TokenAudienceMCP, Scopes: []string{"read", "actions:kubernetes"},
	}, &created); code != http.StatusCreated {
		t.Fatalf("token = %d", code)
	}

	cs := mcpSession(t, addr, created.Token, "2025-11-25")
	waitFor(t, func() bool {
		tools, err := cs.ListTools(context.Background(), nil)
		return err == nil && strings.Contains(toolList(tools), "kubernetes_restart")
	})
	call := func(name string, args map[string]any, out any) *sdk.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out != nil && !res.IsError {
			raw, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatalf("%s: %v in %s", name, err, raw)
			}
		}
		return res
	}
	runs := func(id string) int {
		logMu.Lock()
		defer logMu.Unlock()
		n := 0
		for line := range strings.SplitSeq(logs.String(), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "audit" && rec["action"] == "restart" && rec["approval"] == id && rec["result"] == "ok" {
				n++
			}
		}
		return n
	}

	var pending model.ApprovalPending
	if res := call("kubernetes_restart", map[string]any{"kind": "Deployment", "namespace": "gatus", "name": "gatus"}, &pending); res.IsError ||
		pending.Status != model.ApprovalStatusPending || pending.ApproveURL != "https://dash.example.com/approve/"+pending.ApprovalID {
		t.Fatalf("call = %+v %+v", res.Content, pending)
	}
	if n := runs(pending.ApprovalID); n != 0 {
		t.Fatalf("the restart ran %d times before the approval", n)
	}
	var info model.ApprovalInfo
	if code := api(http.MethodGet, "/api/v1/approvals/"+pending.ApprovalID, nil, &info); code != http.StatusOK || info.State != model.ApprovalStatePending ||
		info.User != me.User || info.CredentialName != "agent" || info.Confirm != model.ConfirmDialog {
		t.Fatalf("GET = %d %+v", code, info)
	}
	if code := api(http.MethodPost, "/api/v1/approvals/"+pending.ApprovalID+"/decision", model.ApprovalDecision{Approve: true}, &info); code != http.StatusOK ||
		info.State != model.ApprovalStateApproved || info.Outcome == nil || !info.Outcome.OK {
		t.Fatalf("decision = %d %+v", code, info)
	}
	if code := api(http.MethodPost, "/api/v1/approvals/"+pending.ApprovalID+"/decision", model.ApprovalDecision{Approve: true}, nil); code != http.StatusConflict {
		t.Errorf("a second decision = %d, want 409", code)
	}
	if n := runs(pending.ApprovalID); n != 1 {
		t.Errorf("the restart ran %d times, want once", n)
	}
	var polled struct {
		State   string `json:"state"`
		Outcome *struct {
			OK     bool              `json:"ok"`
			Result map[string]string `json:"result"`
		} `json:"outcome"`
	}
	if res := call("dash_approval", map[string]any{"id": pending.ApprovalID}, &polled); res.IsError || polled.State != model.ApprovalStateApproved ||
		polled.Outcome == nil || !polled.Outcome.OK || polled.Outcome.Result["status"] != "accepted" {
		t.Fatalf("dash_approval = %+v", polled)
	}

	// A protected namespace: the typed name is the person's to give.
	var guarded model.ApprovalPending
	call("kubernetes_restart", map[string]any{"kind": "Deployment", "namespace": "cert-manager", "name": "cert-manager", "confirm": "cert-manager"}, &guarded)
	if code := api(http.MethodGet, "/api/v1/approvals/"+guarded.ApprovalID, nil, &info); code != http.StatusOK || info.Confirm != model.ConfirmTypeName {
		t.Fatalf("protected request = %d %+v", code, info)
	}
	if code := api(http.MethodPost, "/api/v1/approvals/"+guarded.ApprovalID+"/decision", model.ApprovalDecision{Approve: true}, nil); code != http.StatusBadRequest {
		t.Errorf("without the typed name = %d, want 400", code)
	}
	if code := api(http.MethodPost, "/api/v1/approvals/"+guarded.ApprovalID+"/decision", model.ApprovalDecision{Approve: true, Confirm: "cert-manager"}, &info); code != http.StatusOK ||
		info.State != model.ApprovalStateApproved || info.Outcome == nil || !info.Outcome.OK {
		t.Errorf("with the typed name = %d %+v", code, info)
	}
	if n := runs(guarded.ApprovalID); n != 1 {
		t.Errorf("the protected restart ran %d times, want once", n)
	}
}

func toolList(res *sdk.ListToolsResult) string {
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return strings.Join(names, " ")
}
