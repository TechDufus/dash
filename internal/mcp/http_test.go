package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/model"
)

// labBackend answers dash_get, dash_topic, dash_context and resource reads with v in a string,
// or panics while panicking is set.
type labBackend struct {
	Backend
	v         string
	panicking *atomic.Bool
}

func (b labBackend) check() {
	if b.panicking != nil && b.panicking.Load() {
		panic("planted panic")
	}
}

func (b labBackend) Get(context.Context, string) (any, error) {
	b.check()
	return map[string]string{"description": b.v}, nil
}

func (b labBackend) Topic(_, topic string) (model.Envelope, bool) {
	b.check()
	data, _ := json.Marshal(map[string]string{"msg": b.v})
	return model.Envelope{Topic: topic, Data: data}, true
}

func (b labBackend) Context(_ *http.Request, ref string, _ model.ContextOptions) (model.ContextBundle, error) {
	b.check()
	return model.ContextBundle{Ref: ref, Part: model.ContextPart{Ref: ref, Kind: "Pod", Title: b.v}}, nil
}

func (labBackend) Topics(string) []TopicRef { return []TopicRef{{Name: "core.overview"}} }

func (labBackend) Changes(context.Context) <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

// serveHTTP serves s at a real HTTP server as a read token's principal.
func serveHTTP(t *testing.T, s *Server) string {
	t.Helper()
	p := auth.Principal{Kind: auth.KindToken, Audience: "mcp", User: "bob", Scopes: []string{"read"}}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler(w, r, p) }))
	t.Cleanup(ts.Close)
	return ts.URL
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// rpc posts one legacy (2025-11-25) JSON-RPC request and decodes the answer.
func rpc(t *testing.T, url, method string, params any) rpcReply {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	r.Header.Set("Authorization", "Bearer t") // dash resolved the principal; the SDK only wants a header
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out rpcReply
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil {
		t.Fatalf("%s: %d %.300s", method, resp.StatusCode, raw)
	}
	return out
}

type toolReply struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent map[string]any `json:"structuredContent"`
	IsError           bool           `json:"isError"`
}

func callTool(t *testing.T, url, name string, args any) toolReply {
	t.Helper()
	rep := rpc(t, url, "tools/call", map[string]any{"name": name, "arguments": args})
	var out toolReply
	if rep.Error != nil || json.Unmarshal(rep.Result, &out) != nil || len(out.Content) != 1 {
		t.Fatalf("%s: %+v %s", name, rep.Error, rep.Result)
	}
	return out
}

// Backtick runs long enough that their fence alone overflows the 64 KiB cap are answered over
// HTTP within it, still framed, by every tool that renders lab data and by a resource read; the
// process keeps serving.
func TestLongBacktickRunsStayWithinTheCapOverHTTP(t *testing.T) {
	for _, n := range []int{33 << 10, 40 << 10, 70000} {
		run := strings.Repeat("`", n)
		url := serveHTTP(t, New(Options{Backend: labBackend{v: run + "\nTAIL-MARK"}, Context: canceled()}))
		for name, args := range map[string]any{
			"dash_get":     map[string]string{"ref": "kubernetes:Pod/a/b"},
			"dash_topic":   map[string]string{"topic": "core.overview"},
			"dash_context": map[string]string{"ref": "kubernetes:Pod/a/b"},
		} {
			res := callTool(t, url, name, args)
			text := res.Content[0].Text
			body, _, noted := strings.Cut(text, "\nThe result was larger than 64 KiB")
			fence := fenceOf(body)
			if res.IsError || len(text) > MaxResultBytes || res.StructuredContent["truncated"] != true || !noted ||
				!strings.HasPrefix(body, bundle.Untrusted+"\n\n") || len(fence) < 3 || strings.Trim(fence, "`") != "" ||
				!strings.HasSuffix(strings.TrimRight(body, "\n"), "\n"+fence) || strings.Contains(text, "TAIL-MARK") {
				t.Errorf("%d backticks, %s: %d bytes, structured %v:\n%.200s\n...\n%s", n, name, len(text),
					res.StructuredContent, text, text[max(0, len(text)-200):])
			}
		}
		rep := rpc(t, url, "resources/read", map[string]string{"uri": topicURIPrefix + "core.overview"})
		var rr struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}
		if rep.Error != nil || json.Unmarshal(rep.Result, &rr) != nil || len(rr.Contents) != 1 || len(rr.Contents[0].Text) > MaxResultBytes {
			t.Errorf("%d backticks, resources/read: %+v %.200s", n, rep.Error, rep.Result)
		}
	}
}

// A handler that panics is answered as an internal error, logged at ERROR with its stack, and
// the process keeps serving: a tool call gets an error result, a resource read a JSON-RPC error.
func TestAPanicIsAnsweredAndTheServerKeepsServing(t *testing.T) {
	var logs bytes.Buffer
	panicking := &atomic.Bool{}
	panicking.Store(true)
	s := New(Options{Backend: labBackend{v: "fine", panicking: panicking}, Context: canceled(),
		Log: slog.New(slog.NewJSONHandler(&logs, nil))})
	url := serveHTTP(t, s)

	res := callTool(t, url, "dash_get", map[string]string{"ref": "kubernetes:Pod/a/b"})
	if !res.IsError || res.StructuredContent["code"] != model.ErrCodeInternal || strings.Contains(res.Content[0].Text, "planted") {
		t.Errorf("tool panic answered %+v", res)
	}
	rep := rpc(t, url, "resources/read", map[string]string{"uri": topicURIPrefix + "core.overview"})
	if rep.Error == nil || rep.Error.Code != -32603 || strings.Contains(rep.Error.Message, "planted") {
		t.Errorf("resource panic answered %+v %s", rep.Error, rep.Result)
	}
	// readTopic recovers on its own too, whatever middleware runs around it.
	readCaller := caller{p: auth.Principal{Kind: auth.KindToken, Audience: "mcp", Scopes: []string{"read"}}}
	if _, err := s.readTopic(context.Background(), &sdk.ReadResourceRequest{
		Params: &sdk.ReadResourceParams{URI: topicURIPrefix + "core.overview"},
		Extra:  &sdk.RequestExtra{TokenInfo: &sdkauth.TokenInfo{Extra: map[string]any{"caller": readCaller}}},
	}); err != errInternal {
		t.Errorf("readTopic answered %v", err)
	}
	for _, want := range []string{`"level":"ERROR"`, "planted panic", `"stack":"goroutine `} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %s:\n%s", want, logs.String())
		}
	}

	panicking.Store(false)
	if res := callTool(t, url, "dash_get", map[string]string{"ref": "kubernetes:Pod/a/b"}); res.IsError || !strings.Contains(res.Content[0].Text, "fine") {
		t.Errorf("after a panic: %+v", res)
	}
}
