// Package plugin is the contract between dash's core and its integrations (design A): what a
// plugin declares (Manifest), what it implements (Plugin and the optional interfaces), what the
// core gives it (Host), and the Supervisor that registers, orders, starts, reconfigures and
// stops instances.
//
// This package and every plugin package import neither internal/config nor internal/api: the
// supervisor reads the configuration through Settings, which cmd/dash implements.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/queries"
)

// ID names a plugin type and, in v1, its single instance: [a-z][a-z0-9]{1,31}. No '_' or '-',
// so DASH_PLUGINS_<ID>_<FIELD> parses unambiguously (design A.2).
type ID string

// Core is reserved for the core.* topics; no plugin may use it.
const Core ID = "core"

var (
	idRE     = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)
	topicRE  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	actionRE = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)
)

// Valid reports whether id can name a plugin.
func (id ID) Valid() bool { return id != Core && idRE.MatchString(string(id)) }

// Factory makes a fresh, unstarted Plugin. The supervisor calls it once at Add (for the
// manifest) and again for every restart of the instance.
type Factory func() Plugin

type (
	State   = model.PluginState
	Confirm = model.Confirm
)

const (
	StateDisabled     = model.PluginDisabled
	StateUnavailable  = model.PluginUnavailable
	StateUnconfigured = model.PluginUnconfigured
	StateBlocked      = model.PluginBlocked
	StateStarting     = model.PluginStarting
	StateHealthy      = model.PluginHealthy
	StateDegraded     = model.PluginDegraded
	StateError        = model.PluginError

	ConfirmNone     = model.ConfirmNone
	ConfirmDialog   = model.ConfirmDialog
	ConfirmTypeName = model.ConfirmTypeName
)

// Visible reports whether a plugin in state s shows its pages: every state except disabled,
// unavailable, unconfigured and blocked. An unknown state is visible, so a newer server never
// hides a page an older client had. It mirrors isVisible in web/src/plugins/visibility.ts.
func Visible(s State) bool {
	switch s {
	case StateDisabled, StateUnavailable, StateUnconfigured, StateBlocked:
		return false
	}
	return true
}

// Manifest is everything a plugin declares about itself, readable without starting it.
type Manifest struct {
	ID          ID     // the plugin type; the default instance name
	Name        string // "Proxmox VE"
	Description string
	Version     string     // semver of this plugin's topic/API contract (not the dash version)
	Requires    []ID       // hard: without it the instance is "blocked"
	Uses        []ID       // soft: ordering and Host.Service only
	Config      func() any // pointer to a zero config struct; the schema is reflected from it
	Topics      []TopicSpec
	Actions     []ActionSpec
	Series      []SeriesSpec
	UI          UISpec
}

// TopicSpec declares one topic.
type TopicSpec struct {
	Name string // "vms" is published as SSE event "proxmox.vms"; [a-z][a-z0-9_]{0,31}
	Doc  string
	Type any // zero value of the payload; Host.Publish rejects another type (pointers are dereferenced)
}

// ActionSpec declares one action. Destructive, Idempotent and Input also define the MCP tool
// (design N.2).
type ActionSpec struct {
	Name        string // "restart" -> POST /api/v1/plugins/kubernetes/actions/restart
	Title       string
	MinRole     auth.Role // auth.RoleAdmin unless stated
	Confirm     Confirm
	Destructive bool
	Idempotent  bool       // repeating with the same input is safe; the MCP idempotentHint
	Input       func() any // zero request struct; the schema is reflected; the body is capped like decodeBody
	Summary     func(input any) string
	Target      func(input any) (kind, namespace, name string)
}

// SeriesArgs are the validated arguments a series expression is built from.
type SeriesArgs = queries.Args

// SeriesSpec is a named query a plugin offers on /api/v1/series.
type SeriesSpec struct {
	Name string // "vm.cpu"
	Expr func(a SeriesArgs) (string, error)
}

// ErrNotFound is what a SeriesSpec.Expr reports, through NotFound, for a resource that does not
// exist. The series route answers it 404 not_found with the error's text; any other Expr error
// is 400 invalid_input. Argument validation still comes first: a malformed argument is a 400
// before an Expr runs.
var ErrNotFound = errors.New("not found")

// missingError is an error whose text is the client's message and that is ErrNotFound.
type missingError string

func (e missingError) Error() string        { return string(e) }
func (e missingError) Is(target error) bool { return target == ErrNotFound }

// NotFound is the error of a missing resource: Error() is msg alone (it is the 404 body, so it
// carries no "not found:" prefix) and errors.Is(err, ErrNotFound) holds.
func NotFound(msg string) error { return missingError(msg) }

// UISpec is what a plugin adds to the SPA.
type UISpec struct {
	Nav []model.NavItem
	// Generic are views the SPA renders from the plugin's topics when it has no module of its
	// own. A plugin that declares them and no Nav gets one nav entry, /x/<instance>.
	Generic []model.GenericView
	Icon    string // lucide name of that synthesised nav entry; "Puzzle" when empty
}

// Status is what a running plugin reports about itself.
type Status struct {
	State   State // only starting, healthy, degraded, error or unavailable; the supervisor adds the others
	Message string
	Sources []model.SourceStatus
}

// Plugin is one integration.
type Plugin interface {
	Manifest() Manifest
	// Start receives a config already validated. It builds clients and starts pollers (with
	// Host.Go), and must not block. An error means the instance cannot run at all.
	Start(ctx context.Context, h Host, cfg any) error
	// Stop ends everything Start launched, within ctx's deadline.
	Stop(ctx context.Context) error
	// Status must be cheap and non-blocking (a read of cached state): the supervisor calls it
	// every Options.StatusEvery.
	Status() Status
}

// Optional interfaces, detected with type assertions. The supervisor and the API call these and
// nothing else.

// Validator checks cross-field rules of a config the schema already accepted. A failure puts
// the instance in "unconfigured".
type Validator interface{ Validate(cfg any) error }

// Configured reports whether the config has what the plugin needs. With enabled unset, false
// means "disabled" (auto-enable); with enabled true, "unconfigured". A secret that is a k8s:
// reference the kubernetes plugin has not read yet is not set (secret.Value.IsSet is false), so
// a plugin that needs it is not configured until then; the row says it waits for a Kubernetes
// Secret and the supervisor starts the plugin when the first Reload after the fill finds it set.
type Configured interface{ Configured(cfg any) bool }

// ConfigHinter names what to set to configure the plugin ("plugins.gatus.url"), so that its
// "not configured" row says what is missing: "not configured: set plugins.gatus.url". Without it
// the row says only "not configured". The hint is shown to every viewer: a setting name only.
type ConfigHinter interface{ ConfigHint() string }

// Provider exports what Host.Service(id) returns to a plugin that declares it in Requires or Uses.
type Provider interface{ Service() any }

// Actor runs the plugin's actions.
type Actor interface {
	Act(ctx context.Context, action string, p auth.Principal, input any) (any, error)
}

// ActionError is a deliberate refusal from Act: Status and Code reach the client as written.
// Any other error is a 502 with the upstream category.
type ActionError struct {
	Status  int
	Code    string // a model.ErrCode*
	Message string
	Details any
}

func (e *ActionError) Error() string { return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code) }

// Routes is where a Router registers its routes.
type Routes interface {
	// Get registers GET and HEAD under /api/v1/plugins/{instance}/. pattern is relative
	// ("/vms/{vmid}"); the spec documents it in OpenAPI.
	Get(pattern string, h http.HandlerFunc, spec RouteSpec)
	// Stream registers a server-sent-events GET route. The core owns the connection: it checks
	// the role and the namespace policy, takes a slot of the tail limiter, audits the open,
	// applies the maximum duration, the write deadline and the heartbeat, and then calls h.
	Stream(pattern string, h StreamHandler, spec StreamSpec)
}

// RouteSpec documents one route.
type RouteSpec struct {
	Summary string
	Scope   string // "read" or "logs" (M.3; documented now, enforced for tokens from P7.1)
	Query   []Param
	// Namespace names the namespace the request targets, "" for none. The core refuses the
	// request with 403 when it is in the instance's ReadDenyNamespaces, before the handler runs
	// and (for a stream) before a slot is taken. It is called with the path values set.
	Namespace func(r *http.Request) string
	Response  any // zero value of the 200 body; nil = none
}

// Param documents one query parameter.
type Param struct {
	Name     string
	Doc      string
	Required bool
}

// Router is a plugin with HTTP routes.
type Router interface{ Routes(r Routes) }

// RouteInfo is one route a Router registered. A stream route has Stream and StreamHandler and
// no Handler; the core serves it.
type RouteInfo struct {
	Pattern       string // relative, as registered
	Spec          RouteSpec
	Handler       http.HandlerFunc
	Stream        *StreamSpec
	StreamHandler StreamHandler
}

// CollectRoutes returns what r registers, in registration order. The OpenAPI generator reads
// the routes of a plugin that is not running through it.
func CollectRoutes(r Router) []RouteInfo {
	var c routeCollector
	r.Routes(&c)
	return c.routes
}

type routeCollector struct{ routes []RouteInfo }

func (c *routeCollector) Get(pattern string, h http.HandlerFunc, spec RouteSpec) {
	c.routes = append(c.routes, RouteInfo{Pattern: pattern, Spec: spec, Handler: h})
}

func (c *routeCollector) Stream(pattern string, h StreamHandler, spec StreamSpec) {
	c.routes = append(c.routes, RouteInfo{
		Pattern: pattern, Handler: nil, Stream: &spec, StreamHandler: h,
		Spec: RouteSpec{Summary: spec.Summary, Scope: spec.Scope, Query: spec.Query, Namespace: spec.Namespace},
	})
}

// Demo is implemented by a plugin that has a twin for demo mode (DASH_DEMO=true): a Plugin
// with the same manifest that publishes synthetic data and fakes its actions, and contacts
// nothing. With Options.Demo the supervisor runs the twin instead of the plugin, enabled and
// configured, with its actions mounted.
type Demo interface{ Demo() Plugin }

// Contributors to core.overview (P2.5).
type (
	// AttentionSource lists what needs the operator's attention. A nil list means "no opinion yet"
	// (the plugin is hidden or has not built its data), and the supervisor leaves the first
	// sightings of its items alone; a non-nil empty list means "nothing to raise" and prunes
	// them. A plugin that has polled therefore returns a non-nil empty list when it has nothing
	// to raise. Items without a Since of their own need no help: the supervisor stamps them
	// with the time they were first seen, and that clock survives restarts of the plugin.
	AttentionSource interface {
		Attention(now time.Time) []model.AttentionItem
	}
	KPISource      interface{ KPIs() []model.KPI }
	TopologySource interface{ Topology() model.Topology }
)

// Host is what the core gives a running instance.
type Host interface {
	// Publish publishes the declared topic under "<instance>.<topic>". An
	// undeclared topic, a payload of the wrong type, or a call after Stop is logged and dropped.
	Publish(topic string, data any, sources []model.SourceStatus)
	// Logger is slog.Default().With("plugin", instance): already behind the redacting handler.
	Logger() *slog.Logger
	// HTTPClient builds the guarded client for the plugin's upstream: TLS and CA, credentials per
	// request, the dial guard, redirects limited to the same host, and Options.Transport on top.
	HTTPClient(c HTTPConfig) (*http.Client, error)
	// ScrubLog removes every secret the core knows from a log line. A plugin scrubs a line's text
	// before it builds the payload: scrubbing the encoded JSON would miss a secret that contains
	// a character json.Marshal escapes.
	ScrubLog(s string) string
	// Links resolves the deep links the running link providers offer for an object (design A.9:
	// a plugin never imports another). The providers are read when Links is called.
	Links() LinkResolver
	// IconBaseURL is ui.iconBaseURL as the process started with it (Options.IconBase): the
	// origin and path launcher icon names resolve against and the only place an absolute icon
	// URL may come from. Empty means no icon base is configured. Like the setting, it is fixed
	// for the life of the process.
	IconBaseURL() string
	// Service returns what a Provider exports, only for ids in Requires/Uses whose instance is
	// running.
	Service(id ID) (any, bool)
	Now() time.Time
	// Go runs fn in a goroutine that recovers panics: the instance becomes "error"
	// ("panic in <name>") and the process survives. Pollers MUST use it.
	Go(name string, fn func())
}

// InstanceConfig is what the supervisor needs from the configuration for one instance.
type InstanceConfig struct {
	Name    ID    // plugins.<Name>
	Type    ID    // plugins.<Name>.type; defaults to Name
	Enabled *bool // nil = auto
	// Config is a pointer to a copy of the struct Manifest.Config() returns for Type. A
	// different type puts the instance in "error" ("configuration type mismatch").
	Config any
	// ActionsEnabled and DenyNamespaces are plugins.<Name>.actions.enabled / .denyNamespaces: a
	// section value that implements ActionsConfig (cmd/dash) feeds them, one without leaves them
	// false and empty. Either changing restarts the instance.
	ActionsEnabled bool
	DenyNamespaces []string
	// ReadDenyNamespaces are the namespaces the instance's read routes refuse (a plugin's
	// denyNamespaces, e.g. plugins.victorialogs.denyNamespaces): the core answers 403 for a
	// route whose RouteSpec.Namespace names one of them.
	ReadDenyNamespaces []string
	// FromUI is set when a URL of the section came from the UI layer: every client the instance
	// builds with Host.HTTPClient then refuses loopback too, as HTTPConfig.FromUI does (design
	// A.3, K.22). Like any field, a change restarts the instance, so its clients are rebuilt.
	FromUI bool
}

// Settings is the configuration as the supervisor sees it.
type Settings interface {
	// Instances is the current plugins.* configuration, any order. Unknown names (no factory)
	// are logged once and ignored; a factory without an entry runs with a zero config, auto.
	Instances() []InstanceConfig
}

// Publisher is the hub as the supervisor sees it. *hub.Hub satisfies it.
type Publisher interface {
	Publish(topic model.Topic, data any, sources []model.SourceStatus) (bool, error)
	Remove(topic model.Topic)
	// Resend delivers the stored latest event of topic to the current subscribers again, with
	// the same data and ETag, and reports whether there was one (design A.9, rule (a)).
	Resend(topic model.Topic) bool
}

// HostProvider hands out the Host of an instance to code that publishes on behalf of plugins
// it hosts, and the core's Host to the overview publisher.
type HostProvider interface {
	// HostFor never returns nil. For an instance that is not running (or unknown) every call is
	// a no-op; HostFor(plugin.Core) is always live (core.* topics).
	HostFor(id ID) Host
	Running(id ID) bool
}

// ActionRef is a mounted action, as the API runs it.
type ActionRef struct {
	Instance       ID
	Spec           ActionSpec
	DenyNamespaces []string
	Run            func(ctx context.Context, p auth.Principal, input any) (any, error) // recovers panics
}
