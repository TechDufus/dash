package api

import (
	"net/http"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Route is one entry of the route table: the single place a handler is registered and the
// source of the OpenAPI document (design L.3). Handler() builds its mux from routeTable() and
// nothing else, so no route can exist without its description.
type Route struct {
	ID      string // operationId in the OpenAPI document
	Method  string // "" matches every method (the catch-alls)
	Path    string // the ServeMux path, e.g. "/api/v1/topics/{topic}"
	Summary string
	// Description is the operation's detail in the document (statuses, limits); empty for none.
	Description string
	Tag         string
	Role        auth.Role // the lowest role that may call it; empty = public (the catch-alls and the SPA)
	// Interactive marks a route for a person: a principal that is not interactive (a token) is
	// refused with 403 forbidden, whatever its role. Settings, token and consent routes set it.
	Interactive bool
	// Scope is the token scope the route needs: "read", "logs" or "actions:<plugin>". The guard
	// refuses a token without it (403 insufficient_scope), and every token on a row without one;
	// the plugin rows are checked per route and per action instead.
	Scope      string
	PathParams []plugin.Param
	Query      []plugin.Param
	Request    any // zero value of the JSON body; nil = none
	// RequestDoc describes the request body in the document; empty is a generic sentence.
	RequestDoc string
	Response   any // zero value of the success body; nil = no body
	// Text is the media type of a success body that is not JSON (the settings export,
	// text/yaml): a string in the document.
	Text   string
	Status int // success status; 0 = 200
	SSE    bool
	// SSEDoc replaces the generic description of a stream's 200 response (the streams of plugins).
	SSEDoc string
	// TopicEnvelope marks the topic snapshot: its response is the envelope of whichever topic
	// was asked for, a oneOf of the per-topic envelopes in the document.
	TopicEnvelope bool
	// Plugin marks a route every plugin instance shares. The document lists it once per
	// registered manifest (their actions and routes), not under this generic path.
	Plugin bool
	// Infra marks the catch-all /api/ answer and the SPA, which are not API operations.
	Infra   bool
	Handler http.HandlerFunc
}

// pattern is the ServeMux pattern of r.
func (r Route) pattern() string {
	if r.Method == "" {
		return r.Path
	}
	return r.Method + " " + r.Path
}

// routeTable lists every route the server registers: the /api/v1 contract, the generic plugin
// routes, and the two catch-alls.
func (s *Server) routeTable() []Route {
	viewer := auth.RoleViewer
	rows := []Route{
		{
			ID: "getMe", Method: "GET", Path: "/api/v1/me", Tag: "Session", Role: viewer, Scope: "read",
			Summary: "Who the request acts as, and what the API offers it", Response: model.Me{}, Handler: s.me,
		},
		{
			ID: "streamEvents", Method: "GET", Path: "/api/v1/stream", Tag: "Topics", Role: viewer, Scope: "read",
			Summary: "Server-sent events: the current envelope of every topic, then each change",
			Query: []plugin.Param{{
				Name: "topics", Doc: "Comma-separated topic names (at most 32); only these are sent. Empty or absent means all.",
			}},
			SSE: true, Handler: s.stream,
		},
		{
			ID: "getSeries", Method: "GET", Path: "/api/v1/series", Tag: "Series", Role: viewer, Scope: "read",
			Summary: "A named metric series over a range",
			Query: []plugin.Param{
				{Name: "name", Doc: "Series name, e.g. host.cpu or vm.mem.", Required: true},
				{Name: "range", Doc: "15m, 1h, 6h, 24h or 7d.", Required: true},
				{Name: "vmid", Doc: "The VM, for vm.* series."},
				{Name: "namespace", Doc: "The namespace, for pod.* series (ns is an alias)."},
				{Name: "ns", Doc: "Alias of namespace."},
				{Name: "pod", Doc: "The pod, for pod.* series."},
				{Name: "node", Doc: "The node name, for node.* series."},
			},
			Response: model.Series{}, Handler: s.series,
		},
		{
			ID: "getTopic", Method: "GET", Path: "/api/v1/topics/{topic}", Tag: "Topics", Role: viewer, Scope: "read",
			Summary:       "The current envelope of one topic; 304 when If-None-Match matches its ETag",
			PathParams:    []plugin.Param{{Name: "topic", Doc: "Topic name, e.g. core.plugins or vms.", Required: true}},
			TopicEnvelope: true, Handler: s.topic,
		},
		{
			ID: "listPlugins", Method: "GET", Path: "/api/v1/plugins", Tag: "Plugins", Role: viewer, Scope: "read",
			Summary:  "Every plugin instance with its state, topics, mounted actions and navigation",
			Response: []model.PluginInfo{}, Handler: s.plugins,
		},
		{
			Method: "GET", Path: "/api/v1/plugins/{id}", Plugin: true, Handler: s.pluginRoute,
			Summary: "The bare plugin path; always 404", Tag: "Plugins", Role: viewer,
		},
		{
			Method: "GET", Path: "/api/v1/plugins/{id}/{rest...}", Plugin: true, Handler: s.pluginRoute,
			Summary: "A read route of a running plugin", Tag: "Plugins", Role: viewer, Scope: "read",
		},
		{
			Method: "POST", Path: "/api/v1/plugins/{id}/actions/{name}", Plugin: true, Handler: s.runAction,
			Summary: "Run an action of a running plugin", Tag: "Plugins", Role: auth.RoleAdmin,
		},
	}
	rows = append(rows, s.settingsRoutes()...)
	rows = append(rows, s.tokenRoutes()...)
	rows = append(rows, s.oauthRoutes()...)
	return append(rows, []Route{
		// The sign-in routes of the session modes (docs/auth.md). They are outside /api, so authn
		// does not run (logout reads the session itself) and the guard has no role to enforce;
		// csrf, the host check and the security headers (no-store) apply as everywhere.
		{Method: "POST", Path: "/login", Infra: true, Handler: s.login},
		{Method: "POST", Path: "/setup", Infra: true, Handler: s.setup},
		{Method: "POST", Path: "/auth/logout", Infra: true, Handler: s.logout},
		{Method: "GET", Path: "/auth/oidc/start", Infra: true, Handler: s.oidcStart},
		{Method: "GET", Path: "/auth/callback", Infra: true, Handler: s.oidcCallback},

		{Path: "/api/", Infra: true, Handler: func(w http.ResponseWriter, _ *http.Request) {
			writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
		}},
		// Every other /.well-known/ path answers a JSON 404, never the SPA: a client probing for
		// a document (openid-configuration, a path-inserted metadata URL) learns it is absent.
		{Path: "/.well-known/", Infra: true, Handler: notFound},
		{Path: "/", Infra: true, Handler: s.serveStatic},
	}...)
}

// RoutePatterns lists the ServeMux pattern of every route the API registers, for the metrics'
// route label set (internal/obs).
func RoutePatterns() []string {
	table := (&Server{}).routeTable()
	out := make([]string, len(table))
	for i, r := range table {
		out[i] = r.pattern()
	}
	return out
}

// mux registers every route of the table. guard enforces the row's Role ahead of the handler;
// Mark is the layer outside it, the one that sees the pattern the mux matched; recovered is
// outside both.
func (s *Server) mux() *http.ServeMux { return s.muxOf(s.routeTable()) }

// muxOf is mux for the given table (the tests wrap its handlers).
func (s *Server) muxOf(table []Route) *http.ServeMux {
	mux := http.NewServeMux()
	for _, r := range table {
		mux.HandleFunc(r.pattern(), s.recovered(s.d.Metrics.Mark(s.guard(r, r.Handler))))
	}
	return mux
}
