package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/model"
)

// The limits of design N.6.
const (
	callsPerMinute     = 120 // per token: tools/call, resources/read, prompts/get, subscriptions/listen
	logsPerMinute      = 30  // per token: dash_logs
	concurrentPerToken = 8
	listensPerToken    = 2
	listensGlobal      = 16
	concurrentGlobal   = 64
	// recheckEvery is how often an open listen stream asks whether its token may still listen
	// (the heartbeat of /api/v1/stream).
	recheckEvery = 15 * time.Second
	// MaxResultBytes caps the text of one result (design N.6).
	MaxResultBytes = 64 << 10
	maxLimiterKeys = 4096
)

// codeRateLimited is the JSON-RPC error code of a refused, rate-limited request (the
// implementation-defined server range).
const codeRateLimited = -32029

// Call is one audited MCP request (design N.7).
type Call struct {
	Method string // tools/call, resources/read, prompts/get, subscriptions/listen
	Tool   string // the tool, resource URI or prompt
	// ArgsSHA256 is the hex SHA-256 of the raw arguments; never the arguments themselves.
	ArgsSHA256 string
	Client     string // the client's name, when it says it
	Result     string // ok, error or rate_limited
	Code       string // the error code of a failed call
	Duration   time.Duration
}

// window is a sliding-window rate limiter keyed by token: at most n events per minute each.
type window struct {
	n   int
	now func() time.Time

	mu   sync.Mutex
	seen map[string][]time.Time
}

func newWindow(n int, now func() time.Time) *window {
	return &window{n: n, now: now, seen: map[string][]time.Time{}}
}

// allow records one event for key and reports whether it is within the limit. A new key is
// refused when the table is full of live keys.
func (w *window) allow(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	cut := now.Add(-time.Minute)
	ts := w.seen[key]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 && len(w.seen) >= maxLimiterKeys {
		for k, v := range w.seen {
			if len(v) == 0 || !v[len(v)-1].After(cut) {
				delete(w.seen, k)
			}
		}
		if len(w.seen) >= maxLimiterKeys {
			return false
		}
	}
	if len(ts) >= w.n {
		w.seen[key] = ts
		return false
	}
	w.seen[key] = append(ts, now)
	return true
}

// slots counts the requests in flight per token and in all, calls and listen streams apart.
type slots struct {
	mu                 sync.Mutex
	calls              map[string]int
	listens            map[string]int
	total, listenTotal int
}

// acquire takes a slot for key, a listen stream or a call; release gives it back.
func (s *slots) acquire(key string, listen bool) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, max, total, maxTotal := s.calls, concurrentPerToken, &s.total, concurrentGlobal
	if listen {
		m, max, total, maxTotal = s.listens, listensPerToken, &s.listenTotal, listensGlobal
	}
	if m[key] >= max || *total >= maxTotal {
		return nil, false
	}
	m[key]++
	*total++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if m[key]--; m[key] <= 0 {
			delete(m, key)
		}
		*total--
	}, true
}

// tokenKey names the credential a limit counts against: the grant of an OAuth token, else the
// token.
func tokenKey(p auth.Principal) string {
	if p.GrantID != "" {
		return "grant:" + p.GrantID
	}
	return "token:" + p.TokenID + ":" + p.User
}

// admit takes the request's concurrency slot: a listen stream (2 per token, 16 in all) or a call
// (8 per token, 64 in all). A refusal is a plain 429 with Retry-After, before the SDK reads
// anything.
func (s *Server) admit(w http.ResponseWriter, r *http.Request, p auth.Principal) (func(), bool) {
	listen := r.Header.Get("Mcp-Method") == "subscriptions/listen"
	release, ok := s.slots.acquire(tokenKey(p), listen)
	if ok {
		return release, true
	}
	method := "request"
	if listen {
		method = "subscriptions/listen"
	}
	s.audit(r, Call{Method: method, Result: "rate_limited", Code: model.ErrCodeRateLimited})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too many requests in flight for this token; try again later","code":"rate_limited"}` + "\n"))
	return nil, false
}

func (s *Server) audit(r *http.Request, c Call) {
	if s.o.Audit != nil {
		s.o.Audit(r, c)
	}
}

// counted are the methods that count against the per-token call rate and are audited.
var counted = map[string]bool{"tools/call": true, "resources/read": true, "prompts/get": true, "subscriptions/listen": true}

// limitAndAudit is the outermost middleware: the per-token rates (120 calls and 30 dash_logs a
// minute), then one audit record per counted call with its outcome and duration. A panic below
// it is answered as an internal error (guard).
func (s *Server) limitAndAudit(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		if !counted[method] {
			return s.guard(ctx, method, req, next)
		}
		c, ok := callerOf(req.GetExtra())
		if !ok {
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "no principal"}
		}
		call := describe(method, req)
		start := time.Now()
		key := tokenKey(c.p)
		limited := !s.calls.allow(key) || (method == "tools/call" && call.Tool == "dash_logs" && !s.logCalls.allow(key))
		if limited {
			call.Result, call.Code = "rate_limited", model.ErrCodeRateLimited
			s.audit(c.r, call)
			if method == "tools/call" {
				return errorResult(model.ErrCodeRateLimited, "too many calls for this token; try again in a minute"), nil
			}
			return nil, &jsonrpc.Error{Code: codeRateLimited, Message: "rate_limited: too many calls for this token; try again in a minute"}
		}
		if method == "subscriptions/listen" {
			var end context.CancelFunc
			ctx, end = s.whileAlive(ctx, c)
			defer end()
		}
		res, err := s.guard(ctx, method, req, next)
		call.Duration, call.Result = time.Since(start), "ok"
		switch {
		case err != nil:
			call.Result, call.Code = "error", "rpc_error"
		case isErrorResult(res):
			call.Result, call.Code = "error", errorCode(res)
		}
		s.audit(c.r, call)
		return res, err
	}
}

// whileAlive is the context of c's listen stream: it ends with the request, or at the first
// re-check (every Recheck) after which Alive says c may no longer listen: a revoked or expired
// token, a lost scope, MCP switched off.
func (s *Server) whileAlive(ctx context.Context, c caller) (context.Context, context.CancelFunc) {
	ctx, end := context.WithCancel(ctx)
	if s.o.Alive == nil {
		return ctx, end
	}
	go func() {
		t := time.NewTicker(s.o.Recheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !s.o.Alive(c.r, c.p) {
					end()
					return
				}
			}
		}
	}()
	return ctx, end
}

// guard runs next and answers a panic as an internal error, logged with its stack. The SDK runs
// a handler in a goroutine of its own, outside net/http's recovery, so an unrecovered panic
// would end the process.
func (s *Server) guard(ctx context.Context, method string, req sdk.Request, next sdk.MethodHandler) (res sdk.Result, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			s.recovered(method, rec)
			if method == "tools/call" {
				res, err = errorResult(model.ErrCodeInternal, "internal error"), nil
			} else {
				res, err = nil, errInternal
			}
		}
	}()
	return next(ctx, method, req)
}

// errInternal answers a request whose handler panicked.
var errInternal = &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "internal error"}

// recovered logs a recovered panic with the stack it unwound.
func (s *Server) recovered(where string, rec any) {
	s.o.Log.Error("panic in mcp handler", "method", where, "panic", s.o.Scrub(fmt.Sprint(rec)), "stack", string(debug.Stack()))
}

// describe is the audit view of a request: the tool, resource or prompt it names, the hash of
// its arguments and the client's name.
func describe(method string, req sdk.Request) Call {
	c := Call{Method: method}
	var args []byte
	switch p := req.GetParams().(type) {
	case *sdk.CallToolParamsRaw:
		c.Tool, args = p.Name, p.Arguments
	case *sdk.ReadResourceParams:
		c.Tool = p.URI
	case *sdk.GetPromptParams:
		c.Tool = p.Name
		args, _ = json.Marshal(p.Arguments)
	case *sdk.SubscriptionsListenParams:
		args, _ = json.Marshal(p.Notifications)
	}
	if len(args) > 0 {
		sum := sha256.Sum256(args)
		c.ArgsSHA256 = hex.EncodeToString(sum[:])
	}
	if ci, ok := req.(interface{ ClientInfo() *sdk.Implementation }); ok {
		if impl := ci.ClientInfo(); impl != nil {
			c.Client = impl.Name
		}
	}
	return c
}

func isErrorResult(res sdk.Result) bool {
	r, ok := res.(*sdk.CallToolResult)
	return ok && r.IsError
}

func errorCode(res sdk.Result) string {
	if r, ok := res.(*sdk.CallToolResult); ok {
		if m, ok := r.StructuredContent.(map[string]any); ok {
			if code, ok := m["code"].(string); ok {
				return code
			}
		}
	}
	return "error"
}

// truncatedNote follows, outside the block, a result cut to MaxResultBytes.
const truncatedNote = "\nThe result was larger than 64 KiB and is truncated."

// capResult keeps a tool result within MaxResultBytes of text (design N.6). An answer that is
// larger is body (its JSON, or its own rendering) cut within the untrusted framing, flagged
// truncated in its structured content and its _meta, and says so outside the block.
func (s *Server) capResult(res *sdk.CallToolResult, tree any, body string) *sdk.CallToolResult {
	text := res.Content[0].(*sdk.TextContent).Text
	raw, _ := json.Marshal(tree)
	if len(text) <= MaxResultBytes && len(raw) <= MaxResultBytes {
		return res
	}
	text = s.frameCut(body, truncatedNote)
	return &sdk.CallToolResult{
		Meta:              sdk.Meta{"truncated": true},
		Content:           []sdk.Content{&sdk.TextContent{Text: text}},
		StructuredContent: map[string]any{"truncated": true, "bytes": max(len(raw), len(res.Content[0].(*sdk.TextContent).Text)), "limit": MaxResultBytes},
	}
}

// frameCut is text cut to fit, with note, within MaxResultBytes as an untrusted block. The fence
// grows with the longest backtick run in what is kept, so a cut that leaves the fences too long
// is cut again with the room they leave; when the fences alone do not fit, nothing is kept.
func (s *Server) frameCut(text, note string) string {
	frame := func(t string) string { return bundle.UntrustedBlock(t) + note }
	kept := s.cut(text, MaxResultBytes-len(frame("")))
	for {
		out := frame(kept)
		if len(out) <= MaxResultBytes {
			return out
		}
		// The framing kept needs; each round keeps strictly less, and "" always fits.
		kept = s.cut(kept, MaxResultBytes-(len(out)-len(kept)))
	}
}

// cut shortens text to at most n bytes without splitting a character or leaving part of a
// secret (Options.Cut, secret.Registry.Cut); n <= 0 is "".
func (s *Server) cut(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if s.o.Cut != nil {
		out, _ := s.o.Cut(text, n)
		if len(out) <= n {
			return out
		}
		text = out
	}
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
