package mcp

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/model"
)

// The cap is 64 KiB, as a number: a text of 65536 bytes passes untouched, one of 65537 is cut;
// so is a small text whose structured content is over 65536 bytes.
func TestTheCapIs64KiBOfTextOrStructuredContent(t *testing.T) {
	s := New(Options{Backend: labBackend{v: "x"}, Context: canceled()})
	res := func(text string, tree any) *sdk.CallToolResult {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, StructuredContent: tree}
	}
	small := map[string]string{"a": "b"}
	at := strings.Repeat("y", 65536)
	if got := s.capResult(res(at, small), small, "", at); got.Meta["truncated"] != nil || got.Content[0].(*sdk.TextContent).Text != at {
		t.Errorf("65536 bytes were cut")
	}
	over := at + "y"
	if got := s.capResult(res(over, small), small, "", over); got.Meta["truncated"] != true || len(got.Content[0].(*sdk.TextContent).Text) > 65536 {
		t.Errorf("65537 bytes were not cut to 65536: %v", got.Meta)
	}
	// JSON escapes every quote: 40000 of them are 80000 bytes of structured content.
	quotes := strings.Repeat(`"`, 40000)
	big := map[string]string{"q": quotes}
	got := s.capResult(res("short text", big), big, "", quotes)
	if sc, _ := got.StructuredContent.(map[string]any); got.Meta["truncated"] != true || sc["truncated"] != true || sc["limit"] != 65536 {
		t.Errorf("a structured content over 64 KiB was not flagged: %+v", got.StructuredContent)
	}
}

// The debounce of design N.3 is 5 s unless set.
func TestTheDefaultDebounceIs5s(t *testing.T) {
	if d := New(Options{Backend: labBackend{v: "x"}, Context: canceled()}).o.Debounce; d != 5*time.Second {
		t.Errorf("default debounce %s, want 5s", d)
	}
}

// A token without the read scope lists no resource and reads none, though the topic exists.
func TestResourcesNeedTheReadScope(t *testing.T) {
	url := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled()}), "logs")
	if rep := rpc(t, url, "resources/list", map[string]any{}); rep.Error != nil || strings.Contains(string(rep.Result), "core.overview") {
		t.Errorf("list for a logs token: %+v %s", rep.Error, rep.Result)
	}
	if rep := rpc(t, url, "resources/read", map[string]string{"uri": topicURIPrefix + "core.overview"}); rep.Error == nil {
		t.Errorf("read for a logs token: %s", rep.Result)
	}
	reader := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled()}))
	if rep := rpc(t, reader, "resources/list", map[string]any{}); !strings.Contains(string(rep.Result), "core.overview") {
		t.Errorf("list for a read token: %s", rep.Result)
	}
}

// logsBackend answers dash_logs with one line.
type logsBackend struct{ labBackend }

func (logsBackend) Logs(*http.Request, model.LogsRef, string, string, int) ([]model.LogLine, error) {
	return []model.LogLine{{Msg: "hello"}}, nil
}

// dash_logs reads at most the last hour and at most 200 lines.
func TestLogsAreBoundedTo1hAnd200Lines(t *testing.T) {
	url := serveHTTP(t, New(Options{Backend: logsBackend{labBackend{v: "x"}}, Context: canceled()}), "logs")
	for _, tc := range []struct {
		args map[string]any
		ok   bool
	}{
		{map[string]any{"since": "1h", "limit": 200}, true},
		{map[string]any{"since": "2h"}, false},
		{map[string]any{"since": "6h"}, false},
		{map[string]any{"limit": 201}, false},
		{map[string]any{"limit": -1}, false},
	} {
		tc.args["namespace"], tc.args["pod"] = "a", "b"
		res := callTool(t, url, "dash_logs", tc.args)
		if res.IsError == tc.ok || (!tc.ok && res.StructuredContent["code"] != model.ErrCodeInvalidInput) {
			t.Errorf("%v: %+v", tc.args, res.StructuredContent)
		}
	}
}

// A call over the rate is audited as rate_limited, with its tool, and is not run.
func TestARateLimitedCallIsAudited(t *testing.T) {
	var mu sync.Mutex
	var audited []Call
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := New(Options{Backend: labBackend{v: "x"}, Context: canceled(), Now: func() time.Time { return now },
		Audit: func(_ *http.Request, c Call) { mu.Lock(); audited = append(audited, c); mu.Unlock() }})
	for range 120 {
		s.calls.allow(tokenKey(serveHTTPPrincipal("read")))
	}
	url := serveHTTP(t, s)
	res := callTool(t, url, "dash_get", map[string]string{"ref": "kubernetes:Pod/a/b"})
	mu.Lock()
	defer mu.Unlock()
	if !res.IsError || res.StructuredContent["code"] != model.ErrCodeRateLimited || len(audited) != 1 ||
		audited[0].Method != "tools/call" || audited[0].Tool != "dash_get" || audited[0].Result != "rate_limited" ||
		audited[0].Code != model.ErrCodeRateLimited || audited[0].ArgsSHA256 == "" {
		t.Errorf("result %+v, audited %+v", res.StructuredContent, audited)
	}
}
