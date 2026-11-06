package mcp

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// A body is read, within 10 s, before the request takes a slot: eight requests whose bodies never
// arrive hold none of the token's eight, and are refused when the deadline passes.
func TestASlowBodyHoldsNoSlot(t *testing.T) {
	if bodyTimeout != 10*time.Second {
		t.Fatalf("the body deadline is %s, want 10s", bodyTimeout)
	}
	bodyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyTimeout = 10 * time.Second })
	url := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled()}))
	var conns []net.Conn
	for range 8 {
		c, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		_, _ = c.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{"))
		conns = append(conns, c)
	}
	time.Sleep(100 * time.Millisecond) // the eight are in the handler, reading
	if rep := rpc(t, url, "tools/list", map[string]any{}); rep.Error != nil {
		t.Errorf("a call beside eight slow bodies: %+v", rep.Error)
	}
	_ = conns[0].SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conns[0]), nil)
	if err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a body that never came: %v %v", resp, err)
	}
}

// dash's own check bounds a body at 1 MiB, before the SDK sees it: one byte over is a 413 in
// dash's words, closing the connection; exactly 1 MiB goes through.
func TestABodyOverOneMiBIsRefusedByDash(t *testing.T) {
	s := New(Options{Backend: labBackend{v: "x"}, Context: canceled()})
	p := auth.Principal{Kind: auth.KindToken, Audience: "mcp", User: "bob", TokenID: "tok", Scopes: []string{"read"}}
	post := func(size int) *httptest.ResponseRecorder {
		head, tail := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"`, `"}}`
		body := head + strings.Repeat("x", size-len(head)-len(tail)) + tail
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		s.Handler(w, r, p)
		return w
	}
	const refusal = `{"code":"invalid_input","error":"the request body is over 1 MiB"}` + "\n"
	if w := post(1<<20 + 1); w.Code != http.StatusRequestEntityTooLarge || w.Body.String() != refusal ||
		w.Header().Get("Connection") != "close" {
		t.Errorf("1 MiB and a byte: %d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := post(1 << 20); w.Code == http.StatusRequestEntityTooLarge || w.Body.String() == refusal {
		t.Errorf("exactly 1 MiB: %d %s", w.Code, w.Body)
	}
}

// The slot is chosen by the JSON-RPC method in the body: a tools/call labelled
// subscriptions/listen in its header takes a call slot, so it is refused when the token's eight
// are taken; a 429 names the limit it is over, and only a token's says "this token".
func TestTheBodyMethodChoosesTheSlot(t *testing.T) {
	s := New(Options{Backend: labBackend{v: "x"}, Context: canceled()})
	p := auth.Principal{Kind: auth.KindToken, Audience: "mcp", User: "bob", TokenID: "tok", Scopes: []string{"read"}}
	post := func(header, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		r.Header.Set("Mcp-Method", header)
		w := httptest.NewRecorder()
		s.Handler(w, r, p)
		return w
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dash_overview"}}`
	for range 8 {
		if _, refused := s.slots.acquire(tokenKey(p), "bob", false); refused != "" {
			t.Fatal(refused)
		}
	}
	w := post("subscriptions/listen", call)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "in flight for this token") {
		t.Errorf("a mislabelled call: %d %s", w.Code, w.Body)
	}

	others := New(Options{Backend: labBackend{v: "x"}, Context: canceled()})
	for i := range 64 {
		if _, refused := others.slots.acquire(tokenKey(p)+string(rune('A'+i)), string(rune('A'+i)), false); refused != "" {
			t.Fatal(i, refused)
		}
	}
	s = others
	if w := post("", call); w.Code != http.StatusTooManyRequests || strings.Contains(w.Body.String(), "token") ||
		w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), `"code":"rate_limited"`) {
		t.Errorf("over the global limit: %d %v %s", w.Code, w.Header(), w.Body)
	}
}

// The list methods count against the 120 calls a minute but are not audited.
func TestListsCountAgainstTheRateUnaudited(t *testing.T) {
	var mu sync.Mutex
	var audited []Call
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := New(Options{Backend: labBackend{v: "x"}, Context: canceled(), Now: func() time.Time { return now },
		Audit: func(_ *http.Request, c Call) { mu.Lock(); audited = append(audited, c); mu.Unlock() }})
	url := serveHTTP(t, s)
	lists := []string{"tools/list", "resources/list", "resources/templates/list", "prompts/list"}
	for i := range 120 {
		if rep := rpc(t, url, lists[i%len(lists)], map[string]any{}); rep.Error != nil {
			t.Fatalf("list %d: %+v", i+1, rep.Error)
		}
	}
	if rep := rpc(t, url, "tools/list", map[string]any{}); rep.Error == nil || rep.Error.Code != codeRateLimited {
		t.Errorf("the 121st list: %+v", rep.Error)
	}
	if res := callTool(t, url, "dash_overview", map[string]any{}); !res.IsError || res.StructuredContent["code"] != model.ErrCodeRateLimited {
		t.Errorf("a call after 121 lists: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(audited) != 1 || audited[0].Method != "tools/call" {
		t.Errorf("audited %+v, want only the refused tool call", audited)
	}
}

// dash_context reading logs counts against the 30 log reads a minute: for a token with the
// logs scope, unless its include leaves the logs out.
func TestContextWithLogsCountsAsALogRead(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	ref := map[string]any{"ref": "kubernetes:Pod/a/b"}
	url := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled(), Now: clock}), "read", "logs")
	for i := range 30 {
		args := ref
		if i%2 == 1 {
			args = map[string]any{"ref": "kubernetes:Pod/a/b", "include": []string{"events", "logs"}}
		}
		if res := callTool(t, url, "dash_context", args); res.IsError {
			t.Fatalf("context %d: %+v", i+1, res)
		}
	}
	if res := callTool(t, url, "dash_context", ref); !res.IsError || res.StructuredContent["code"] != model.ErrCodeRateLimited {
		t.Errorf("the 31st log read: %+v", res)
	}
	if res := callTool(t, url, "dash_context", map[string]any{"ref": "kubernetes:Pod/a/b", "include": []string{"events"}}); res.IsError {
		t.Errorf("a context without logs: %+v", res)
	}

	readOnly := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled(), Now: clock}))
	for i := range 31 {
		if res := callTool(t, readOnly, "dash_context", ref); res.IsError {
			t.Fatalf("context %d without the logs scope: %+v", i+1, res)
		}
	}
}

// subscriptions/listen under the legacy protocol is refused like resources/subscribe: it has no
// stream to deliver on.
func TestLegacyListenIsRefused(t *testing.T) {
	url := serveHTTP(t, New(Options{Backend: labBackend{v: "x"}, Context: canceled()}))
	rep := rpc(t, url, "subscriptions/listen", map[string]any{"notifications": map[string]any{"toolsListChanged": true}})
	if rep.Error == nil || rep.Error.Message != errLegacySubscribe.Message {
		t.Errorf("legacy listen: %+v %s", rep.Error, rep.Result)
	}
}
