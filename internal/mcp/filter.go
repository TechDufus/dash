package mcp

import (
	"context"
	"slices"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolNamed is the tool called name, if dash has one.
func (s *Server) toolNamed(name string) (tool, bool) {
	i := slices.IndexFunc(s.tools, func(t tool) bool { return t.name == name })
	if i < 0 {
		return tool{}, false
	}
	return s.tools[i], true
}

// visible reports whether c may call t: its token holds the tool's scope.
func visible(c caller, t tool) bool { return c.p.HasScope(t.scope) }

// scopeFilter makes tools/list show only the tools the calling principal may call, in the
// table's order, and answers tools/call of any other tool as the SDK answers an unknown one
// (design N.2): one shared server, decided per request.
func (s *Server) scopeFilter(next sdk.MethodHandler) sdk.MethodHandler {
	return func(ctx context.Context, method string, req sdk.Request) (sdk.Result, error) {
		c, ok := callerOf(req.GetExtra())
		switch method {
		case "tools/call":
			p, _ := req.GetParams().(*sdk.CallToolParamsRaw)
			if p == nil {
				break
			}
			if t, known := s.toolNamed(p.Name); !ok || (known && !visible(c, t)) {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown tool " + strconv.Quote(p.Name)}
			}
		case "tools/list":
			res, err := next(ctx, method, req)
			lr, isList := res.(*sdk.ListToolsResult)
			if err != nil || !isList {
				return res, err
			}
			out := *lr
			out.Tools = []*sdk.Tool{}
			for _, t := range s.tools {
				if !ok || !visible(c, t) {
					continue
				}
				if i := slices.IndexFunc(lr.Tools, func(x *sdk.Tool) bool { return x.Name == t.name }); i >= 0 {
					out.Tools = append(out.Tools, lr.Tools[i])
				}
			}
			return &out, nil
		case "resources/list":
			if !ok {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "no principal"}
			}
			res := s.listTopics(c)
			res.Cacheable = sdk.Cacheable{CacheScope: "private", TTLMs: 60000}
			return res, nil
		case "subscriptions/listen":
			p, _ := req.GetParams().(*sdk.SubscriptionsListenParams)
			if !ok {
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: "no principal"}
			}
			if p == nil || p.Notifications == nil {
				break
			}
			if err := s.checkSubscriptions(c, p.Notifications.ResourceSubscriptions); err != nil {
				return nil, err
			}
		case "resources/subscribe", "resources/unsubscribe":
			return nil, errLegacySubscribe
		}
		return next(ctx, method, req)
	}
}
