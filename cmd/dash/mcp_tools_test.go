package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/bundle"
)

// callTool calls a tool and decodes its structured content into out (when not nil); it returns
// the text and whether the result is an error.
func callTool(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any, out any) (string, bool) {
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
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

func toolNames(t *testing.T, cs *sdk.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range res.Tools {
		out = append(out, tl.Name)
	}
	return out
}

// tools/list varies with the token's scopes: no dash_logs without logs, and calling it anyway is
// an unknown tool, not a log read.
func TestMCPToolsListVariesWithScopes(t *testing.T) {
	addr := startMCPDemo(t)
	read, logs := mcpSession(t, addr, mcpReadToken, "2026-07-28"), mcpSession(t, addr, mcpLogsToken, "2026-07-28")
	want := []string{"dash_overview", "dash_search", "dash_get", "dash_topic", "dash_series", "dash_context"}
	if got := toolNames(t, read); !slices.Equal(got, want) {
		t.Errorf("read token: %v", got)
	}
	if got := toolNames(t, logs); !slices.Equal(got, []string{"dash_overview", "dash_search", "dash_get", "dash_topic", "dash_series", "dash_logs", "dash_context"}) {
		t.Errorf("logs token: %v", got)
	}
	_, err := read.CallTool(context.Background(), &sdk.CallToolParams{Name: "dash_logs", Arguments: map[string]any{"namespace": "media", "pod": "x"}})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("dash_logs with a read token: %v", err)
	}
}

// The read tools against the demo: search finds a pod, get reads it with at most 20 events and
// never a Secret, topic follows a JSON Pointer, series sums up with at most 60 points, logs come
// only inside the untrusted block, and context is the Markdown bundle.
func TestMCPReadTools(t *testing.T) {
	addr := startMCPDemo(t)
	cs := mcpSession(t, addr, mcpLogsToken, "2026-07-28")

	var found struct {
		Hits []struct{ Ref, Kind, Title string } `json:"hits"`
	}
	callTool(t, cs, "dash_search", map[string]any{"query": "", "kinds": []string{"pod"}, "limit": 5}, &found)
	if len(found.Hits) == 0 || len(found.Hits) > 5 {
		t.Fatalf("search = %+v", found)
	}
	for _, h := range found.Hits {
		if h.Kind != "Pod" {
			t.Errorf("kinds filter let through %+v", h)
		}
	}
	pod := found.Hits[0].Ref

	var detail struct {
		Pod    struct{ Ref, Namespace, Name string } `json:"pod"`
		Events []any                                 `json:"events"`
	}
	callTool(t, cs, "dash_get", map[string]any{"ref": pod}, &detail)
	if detail.Pod.Ref != pod || len(detail.Events) > 20 {
		t.Errorf("get = %+v", detail)
	}
	if text, isErr := callTool(t, cs, "dash_get", map[string]any{"ref": "kubernetes:Secret/" + detail.Pod.Namespace + "/x"}, nil); !isErr || !strings.Contains(text, "not_found") {
		t.Errorf("a Secret ref: %s", text)
	}
	if text, isErr := callTool(t, cs, "dash_get", map[string]any{"ref": pod, "extra": 1}, nil); !isErr || !strings.Contains(text, "invalid_input") {
		t.Errorf("an unknown argument: %s", text)
	}

	var topic struct {
		Topic string         `json:"topic"`
		Data  map[string]any `json:"data"`
	}
	callTool(t, cs, "dash_topic", map[string]any{"topic": "kubernetes.cluster", "pointer": "/pods/0"}, &topic)
	if topic.Topic != "kubernetes.cluster" || topic.Data["ref"] == nil {
		t.Errorf("topic = %+v", topic)
	}
	if text, isErr := callTool(t, cs, "dash_topic", map[string]any{"topic": "kubernetes.cluster", "pointer": "/nope/9"}, nil); !isErr || !strings.Contains(text, "not_found") {
		t.Errorf("a pointer to nothing: %s", text)
	}

	var series struct {
		Lines []struct {
			Min, Max, Avg, Last float64
			Points              [][2]float64 `json:"points"`
		} `json:"lines"`
	}
	callTool(t, cs, "dash_series", map[string]any{"name": "host.cpu", "range": "7d"}, &series)
	if len(series.Lines) == 0 {
		t.Fatal("no series lines")
	}
	for _, l := range series.Lines {
		if len(l.Points) == 0 || len(l.Points) > 60 || l.Min > l.Avg || l.Avg > l.Max {
			t.Errorf("line: %d points, min %g avg %g max %g", len(l.Points), l.Min, l.Avg, l.Max)
		}
	}

	var logs struct {
		Lines []struct{ Msg string } `json:"lines"`
	}
	text, _ := callTool(t, cs, "dash_logs", map[string]any{"namespace": detail.Pod.Namespace, "pod": detail.Pod.Name, "limit": 7}, &logs)
	if len(logs.Lines) == 0 || len(logs.Lines) > 7 {
		t.Fatalf("%d log lines", len(logs.Lines))
	}
	head, block, ok := strings.Cut(text, bundle.Untrusted)
	enc, _ := json.Marshal(logs.Lines[0].Msg) // the text is the JSON, so the message is escaped in it
	if !ok || head != "" || !strings.Contains(block, string(enc[1:len(enc)-1])) {
		t.Errorf("log text outside the untrusted block:\n%.400s", text)
	}

	text, isErr := callTool(t, cs, "dash_context", map[string]any{"ref": pod}, nil)
	if isErr || !strings.HasPrefix(text, "# dash context bundle") || !strings.Contains(text, bundle.Untrusted) {
		t.Errorf("context text:\n%.400s", text)
	}
}

// Without the logs scope a context bundle has no log lines and says why.
func TestMCPContextWithoutTheLogsScope(t *testing.T) {
	addr := startMCPDemo(t)
	cs := mcpSession(t, addr, mcpReadToken, "2025-11-25")
	var found struct {
		Hits []struct{ Ref string } `json:"hits"`
	}
	callTool(t, cs, "dash_search", map[string]any{"kinds": []string{"Pod"}, "limit": 1}, &found)
	if len(found.Hits) != 1 {
		t.Fatalf("search = %+v", found)
	}
	var b struct {
		Logs     []any  `json:"logs"`
		LogsNote string `json:"logsNote"`
	}
	callTool(t, cs, "dash_context", map[string]any{"ref": found.Hits[0].Ref}, &b)
	if len(b.Logs) != 0 || b.LogsNote != "logs_scope_required" {
		t.Errorf("bundle logs %d, note %q", len(b.Logs), b.LogsNote)
	}
}

// dash_search's description names every kind the demo's plugins answer with, dash_series
// answers an unknown name with the names it serves, and a bad namespace in dash_logs is the
// caller's mistake, not an outage.
func TestMCPToolsDescribeAndRefuseWithTheRealNames(t *testing.T) {
	fw, _ := startDemo(t)
	awaitDemoTopics(t, fw)
	kinds := map[string]bool{}
	for _, id := range fw.sup.ObjectInstances() {
		o, _ := fw.sup.Objects(id)
		for _, h := range o.Search("", nil, 100000) {
			kinds[h.Kind] = true
		}
	}
	series := fw.sup.SeriesNames()

	addr := startMCPDemo(t)
	cs := mcpSession(t, addr, mcpLogsToken, "2026-07-28")
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(res.Tools, func(tl *sdk.Tool) bool { return tl.Name == "dash_search" })
	desc := res.Tools[i].Description
	for k := range kinds {
		if !strings.Contains(desc, " "+k+",") && !strings.Contains(desc, " "+k+" (") {
			t.Errorf("the dash_search description lacks the kind %s: %s", k, desc)
		}
	}
	text, isErr := callTool(t, cs, "dash_series", map[string]any{"name": "nope.cpu", "range": "1h"}, nil)
	if !isErr || !strings.Contains(text, "invalid_input") || !strings.Contains(text, strings.Join(series, ", ")) {
		t.Errorf("unknown series: %s (want the names %v)", text, series)
	}
	text, isErr = callTool(t, cs, "dash_logs", map[string]any{"namespace": "Not_A_Namespace", "pod": "x"}, nil)
	if !isErr || !strings.HasPrefix(text, "invalid_input: invalid namespace") {
		t.Errorf("a bad namespace: %s", text)
	}
}
