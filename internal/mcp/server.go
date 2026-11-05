// Package mcp is dash's read-only MCP server at POST /mcp (design N, contract D36): stateless
// Streamable HTTP for protocol versions 2026-07-28 and 2025-11-25, served with the official Go
// SDK. dash's own authentication runs first (internal/api): a request reaches Handler only with
// a token principal of audience mcp, and the SDK's bearer step merely carries that principal to
// the tool handlers. One shared SDK server serves every principal; what a principal may see and
// call is decided per request.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
)

// ProtocolVersions are the MCP versions dash serves, newest first (contract D36).
var ProtocolVersions = []string{"2026-07-28", "2025-11-25"}

// maxRequestBody bounds one JSON-RPC request.
const maxRequestBody = 1 << 20

// Backend is what the tools read: the API server implements it, so the tools answer exactly
// what the REST routes would, under the same policies. r is the /mcp request, whose context
// holds the principal; reads that go through a plugin route (the logs) are made as it.
//
// A refusal the caller can act on (a bad argument, an unknown object) is an *Error; any other
// error is an upstream failure, answered with no detail.
type Backend interface {
	// Overview is the current core.overview payload.
	Overview() (model.OverviewData, bool)
	// Search asks every running plugin with objects, in registry order, for at most limit hits.
	Search(query string, limit int) []model.SearchHit
	// Get is one object by ref; Secret and ConfigMap objects are never returned.
	Get(ctx context.Context, ref string) (any, error)
	// Topic is the current envelope of topic as user reads it (their own copy of a scoped
	// topic, else the global one).
	Topic(user, topic string) (model.Envelope, bool)
	// Series reads a named series over rng with its query parameters (vmid, namespace, pod,
	// node), with the step it was read at in seconds.
	Series(ctx context.Context, name, rng string, args map[string]string) ([]model.SeriesLine, int, error)
	// Logs reads at most limit of the newest log lines, oldest first, as r's principal through
	// the victorialogs route (its scope and deny lists apply).
	Logs(r *http.Request, ref model.LogsRef, since, query string, limit int) ([]model.LogLine, error)
	// Context is the context bundle of ref, as r's principal.
	Context(r *http.Request, ref string, o model.ContextOptions) (model.ContextBundle, error)
}

// Error is a refusal a tool reports to the caller: code is a model.ErrCode*.
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Options configure a Server.
type Options struct {
	Backend Backend
	Version string
	Log     *slog.Logger
	// Scrub removes every secret dash knows from one string (secret.Registry.ScrubLogLine).
	Scrub func(string) string
}

// Server is the MCP server.
type Server struct {
	o     Options
	srv   *sdk.Server
	tools []tool
	h     http.Handler
}

// New builds the server and its tools.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Scrub == nil {
		o.Scrub = func(s string) string { return s }
	}
	s := &Server{o: o}
	s.srv = sdk.NewServer(&sdk.Implementation{Name: "dash", Title: "dash", Version: o.Version}, &sdk.ServerOptions{
		Logger:                    quiet(o.Log),
		SupportedProtocolVersions: ProtocolVersions,
		Capabilities: &sdk.ServerCapabilities{
			Tools: &sdk.ToolCapabilities{ListChanged: true},
		},
		SetCacheable: func(_ context.Context, _ sdk.Request, c *sdk.Cacheable) {
			c.CacheScope = "private"
		},
	})
	s.tools = s.toolTable()
	for _, t := range s.tools {
		s.srv.AddTool(t.sdkTool(), s.toolHandler(t))
	}
	s.srv.AddReceivingMiddleware(s.scopeFilter)
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.srv }, &sdk.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		Logger:                       quiet(o.Log),
		DisableLocalhostProtection:   true, // dash's Host allow-list covers DNS rebinding (D36)
		MaxRequestBodyBytes:          maxRequestBody,
		PropagateRequestCancellation: true,
	})
	s.h = sdkauth.RequireBearerToken(verify, &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
	return s
}

// ctxKey carries the principal and the request from Handler to the verifier.
type ctxKey struct{}

type caller struct {
	p auth.Principal
	r *http.Request
}

// quiet is l without its INFO and DEBUG records: the SDK logs every stateless request's session
// at INFO, which would be one line per tool call.
func quiet(l *slog.Logger) *slog.Logger { return slog.New(warnHandler{l.Handler()}) }

type warnHandler struct{ slog.Handler }

func (h warnHandler) Enabled(ctx context.Context, lv slog.Level) bool {
	return lv >= slog.LevelWarn && h.Handler.Enabled(ctx, lv)
}
func (h warnHandler) WithAttrs(a []slog.Attr) slog.Handler { return warnHandler{h.Handler.WithAttrs(a)} }
func (h warnHandler) WithGroup(n string) slog.Handler      { return warnHandler{h.Handler.WithGroup(n)} }

// Handler serves POST /mcp for p, the token principal dash resolved for r (audience mcp).
func (s *Server) Handler(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	s.h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, caller{p: p, r: r})))
}

// verify is the SDK's bearer step: it only maps the principal dash already resolved. A request
// without one never reaches here (internal/api answers it 401 first), so it fails closed.
func verify(ctx context.Context, _ string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	c, ok := ctx.Value(ctxKey{}).(caller)
	if !ok || c.p.Kind != auth.KindToken || c.p.Audience != "mcp" {
		return nil, sdkauth.ErrInvalidToken
	}
	return &sdkauth.TokenInfo{Scopes: c.p.Scopes, UserID: c.p.User, Extra: map[string]any{"caller": c}}, nil
}

// callerOf is the principal and request of a tool call.
func callerOf(extra *sdk.RequestExtra) (caller, bool) {
	if extra == nil || extra.TokenInfo == nil {
		return caller{}, false
	}
	c, ok := extra.TokenInfo.Extra["caller"].(caller)
	return c, ok
}

// tool is one of dash's tools: its MCP description, the scope it needs, and what it runs. in is
// a pointer to a zero input, decoded strictly; the result is any JSON value plus, optionally, its
// text rendering (otherwise the JSON, framed as untrusted lab data).
type tool struct {
	name, title, desc string
	scope             string
	input             any
	run               func(ctx context.Context, c caller, in any) (result any, text string, err error)
}

func (t tool) sdkTool() *sdk.Tool {
	schema := jsonschema.New().Type(reflect.TypeOf(t.input).Elem())
	return &sdk.Tool{
		Name: t.name, Title: t.title, Description: t.desc, InputSchema: schema,
		Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: new(false), Title: t.title},
	}
}

// refuse is a refusal a tool answers as an error result: the caller's mistake, never a failure
// of dash.
func refuse(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// toolHandler decodes the input strictly, runs the tool and renders its result: the scrubbed
// JSON as structured content, and as text either the tool's own rendering or the JSON inside the
// untrusted-data block.
func (s *Server) toolHandler(t tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		c, ok := callerOf(req.Extra)
		if !ok {
			return nil, errors.New("no principal")
		}
		in := reflect.New(reflect.TypeOf(t.input).Elem()).Interface()
		if raw := req.Params.Arguments; len(raw) > 0 && string(raw) != "null" {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.DisallowUnknownFields()
			if err := dec.Decode(in); err != nil {
				return errorResult(model.ErrCodeInvalidInput, "invalid arguments: "+s.o.Scrub(err.Error())), nil
			}
		}
		out, text, err := t.run(ctx, c, in)
		if te, ok := errors.AsType[*Error](err); ok {
			return errorResult(te.Code, s.o.Scrub(te.Message)), nil
		}
		if err != nil {
			s.o.Log.Warn("mcp tool failed", "tool", t.name, "err", s.o.Scrub(err.Error()))
			return errorResult(model.ErrCodeUpstreamUnavailable, "the data is not available right now"), nil
		}
		tree, err := bundle.Scrub(out, s.o.Scrub)
		if err != nil {
			return nil, err
		}
		if text == "" {
			raw, err := json.MarshalIndent(tree, "", "  ")
			if err != nil {
				return nil, err
			}
			text = bundle.UntrustedBlock(string(raw))
		} else {
			text = s.o.Scrub(text)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, StructuredContent: tree}, nil
	}
}

func errorResult(code, msg string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError:           true,
		Content:           []sdk.Content{&sdk.TextContent{Text: code + ": " + msg}},
		StructuredContent: map[string]any{"error": msg, "code": code},
	}
}
