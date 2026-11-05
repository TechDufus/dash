// Package api serves dash's REST endpoints, the SSE streams and the embedded SPA.
package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/oidcauth"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/settings"
	"github.com/TechDufus/dash/internal/upstream"
	"github.com/TechDufus/dash/web"
)

// PluginsAPI is the slice of plugin.Supervisor the API uses.
type PluginsAPI interface {
	Infos() []model.PluginInfo
	Manifests() []plugin.Manifest
	Action(instance plugin.ID, name string) (plugin.ActionRef, bool)
	// Router serves the routes of a running instance through g, the core's route policy.
	Router(instance plugin.ID, g plugin.RouteGuard) (http.Handler, bool)
	// Service is what a running instance's Provider exports, for the code that is not a plugin.
	Service(id plugin.ID) (any, bool)
	// Links resolves deep links through the running link providers.
	Links() plugin.LinkResolver
	Series(name string) (plugin.SeriesSpec, plugin.ID, bool)
	Ready() bool
}

// SettingsBackend is what the settings routes (P4.7) use of the configuration: the reloader, the
// UI settings store, the secret writer and the connection checks. cmd/dash implements it over
// the real ones (settingsBackend); settingstest.Fake is the same wiring over a temporary data
// directory, with hooks for tests.
//
// Every write runs inside WithLock and calls Reload before fn returns: the If-Match check,
// Candidate, Apply and Reload all happen under the one reloader lock, so neither another write
// nor the reloader's tick can interleave (design D14b, D15).
type SettingsBackend interface {
	// Resolved is the running configuration with the provenance of every value. It is shared:
	// never modify it.
	Resolved() *config.Resolved
	// RestartRequired lists, sorted, the settings changed since this process started that need
	// a restart (Reloader.RestartRequired).
	RestartRequired() []string
	// WithLock runs fn holding the reloader lock (Reloader.WithLock). fn must not call WithLock.
	WithLock(fn func() error) error
	// Candidate resolves the configuration with ui as the whole UI layer, strictly: a problem
	// with a UI value that differs from the store's is an error naming its path
	// (config.FieldErrors). Nothing running or stored changes. Call it inside WithLock.
	Candidate(ui map[string]any) (*config.Resolved, error)
	// Reload puts the files and the store into effect now (Reloader.Reload). Call it only
	// inside WithLock.
	Reload() error

	// Store describes the UI settings store; it does not change while dash runs.
	Store() model.SettingsStore
	// Revision is the store's revision: the If-Match of a write. Read it inside WithLock, with
	// what it is the revision of: read apart, it can be another write's.
	Revision() uint64
	// Stored is a deep copy of the store's values: the sparse, nested UI layer.
	Stored() map[string]any
	// Apply writes sets and deletes (dotted paths) as one commit and one revision; scrub lists
	// the secret paths whose old values are removed from the backups (settings.Store.Apply). A
	// non-zero prev with an error means the write landed: Revert(prev).
	Apply(sets map[string]any, deletes, scrub []string) (prev settings.Snapshot, err error)
	// Revert writes prev's values back as a new revision (settings.Store.Revert).
	Revert(prev settings.Snapshot) error

	// PluginConfig is the configuration of plugin id in res as the plugin receives it: the
	// candidate Check takes, and whether a URL of the section comes from the UI (the dial guard
	// then refuses loopback). ok is false for an id res has no section for.
	PluginConfig(res *config.Resolved, id string) (cfg any, fromUI bool, ok bool)
	// Testable reports whether Check can test plugin id (plugin.Supervisor.Testable).
	Testable(id string) bool
	// Check runs plugin id's connection check against cfg (plugin.Supervisor.Check): an error
	// is plugin.ErrNotCheckable or the plugin's refusal of cfg, whose text may hold values.
	Check(ctx context.Context, id string, cfg any, fromUI bool) (plugin.CheckReport, error)
	// Namespace is the Kubernetes namespace dash runs in (POD_NAMESPACE, else the service
	// account's namespace file); "" outside a cluster.
	Namespace() string
}

// Deps are the collaborators of a Server.
type Deps struct {
	Cfg     config.Config
	Version string
	Hub     *hub.Hub
	Plugins PluginsAPI   // nil = no plugins
	Health  HealthSource // readiness; see health.go
	Metrics *obs.Metrics // nil = metrics off; every method is nil-safe
	// Secrets scrubs upstream errors and log lines; nil only matches credential shapes.
	Secrets *secret.Registry
	Audit   *audit.Log // nil logs to slog only
	// Auth resolves the principal of every /api request. It is required: New panics without one.
	Auth auth.Authenticator
	// Tokens resolves bearer credentials ahead of Auth (contract D2); nil in auth.mode=none,
	// where a Bearer header is ignored like every other credential.
	Tokens TokenResolver
	// OAuth is the authorization server (contract D21): auth.oauth.enabled in local, oidc or
	// proxy mode with a writable data dir; nil otherwise, and its routes answer 404.
	OAuth *oauth.Server

	// The session modes (auth.mode local and oidc); every field is nil in the other modes.
	// Sessions issues and clears the session cookie (login, setup, the OIDC callback, logout).
	Sessions *session.Manager
	// Credentials holds the personal access tokens in every mode but none (contract D1), and in
	// the session modes the session epochs (logout bumps them) and the local account.
	Credentials *credentials.Store
	// Local is the local mode's login machinery; nil unless auth.mode=local.
	Local *localauth.Local
	// OIDC is the configured identity provider; nil unless auth.mode=oidc.
	OIDC *oidcauth.Provider
	// Settings backs /api/v1/settings/**; nil (demo mode) makes every settings route 404.
	Settings SettingsBackend
}

// Server holds the handlers' state.
type Server struct {
	d           Deps
	cspMode     cspMode // server.csp; "" enforces (a Config that never went through the resolver)
	iconSrc     string  // the img-src source of ui.iconBaseURL, computed once ("" = none)
	static      fs.FS
	pwa         map[string]pwaFile // headers of sw.js and offline.html in static, computed once
	now         func() time.Time
	heartbeat   time.Duration
	events      *streamLimiter   // open /api/v1/stream connections
	tails       *streamLimiter   // open plugin streams (a log tail)
	tailMax     time.Duration    // longest a plugin stream stays open
	sseWrite    time.Duration    // deadline of each SSE write
	bodyTimeout time.Duration    // longest a request body may take to arrive
	actionLimit *rateLimiter     // framework actions per principal
	tokenLimit  *tokenBucket     // requests per api token (design M.5)
	createLimit *rateLimiter     // personal access tokens created per user
	oauthLimit  *evictingLimiter // /oauth/token and /oauth/revoke per grant, or client_id and peer
	testLimit   *rateLimiter     // settings connection tests per principal
	openTarget  *rateLimiter     // stream-open audit records per user and target
	openUser    *rateLimiter     // stream-open audit records per user
	hostWarn    atomic.Int64     // unix nanos of the last unknown-host warning
	serving     atomic.Bool      // the listeners are bound (MarkServing)
	probe       principalProbe   // first sighting of each user name, for the log
	schemas     sync.Map         // reflect.Type -> json.RawMessage: action input schemas
}

// New builds a Server serving the embedded SPA.
func New(d Deps) *Server {
	if d.Auth == nil {
		panic("api: Deps.Auth is required")
	}
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil { // only if the embed root is renamed
		panic(err)
	}
	l := withDefaultLimits(d.Cfg.Limits)
	return &Server{
		d: d, static: dist, pwa: pwaFiles(dist), now: time.Now, heartbeat: 15 * time.Second, cspMode: cspMode(d.Cfg.CSP),
		iconSrc: iconSourceOrWarn(d.Cfg.IconBaseURL, cspMode(d.Cfg.CSP), slog.Default()),
		events:  newStreamLimiter(l.StreamsPerUser, l.Streams),
		tails:   newStreamLimiter(l.TailsPerUser, l.Tails),
		tailMax: l.TailMaxDuration, sseWrite: l.SSEWriteTimeout, bodyTimeout: l.BodyReadTimeout,
		actionLimit: newRateLimiter(actionRateLimit, actionRateWindow, maxRateKeys),
		tokenLimit:  newTokenBucket(tokenRate, tokenBurst, maxRateKeys),
		createLimit: newRateLimiter(tokenCreateLimit, tokenCreateWindow, maxRateKeys),
		oauthLimit:  newEvictingLimiter(oauthTokenLimit, oauthTokenWindow, maxRateKeys),
		testLimit:   newRateLimiter(settingsTestRateLimit, settingsTestRateWindow, maxRateKeys),
		openTarget:  newRateLimiter(1, openRecordWindow, maxRateKeys),
		openUser:    newRateLimiter(openRecordPerUser, openRecordWindow, maxRateKeys),
	}
}

// Handler is the API plus the SPA, for the loopback listener. The chain is request id, security
// headers, host check, CORS, authentication, CSRF, then the mux the route table builds (whose
// guard enforces each route's role). Authentication runs before CSRF, so a refusal for a missing
// header names the user and an unauthenticated write is 401. The security headers sit just
// inside the request id, so every answer carries them, including a refused host; CORS sits
// inside the host check, so a host dash does not serve gets no CORS answer either.
func (s *Server) Handler() http.Handler {
	return s.d.Metrics.Instrument(s.requestID(securityHeaders(s.cspMode)(s.hosts(s.cors(s.authn(s.csrf(s.mux())))))))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers the L.2 error envelope: msg is text for a person, code one of model.ErrCode*.
// The request id is the one requestID put on the response.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeErrDetails(w, status, code, msg, nil)
}

// writeErrDetails is writeErr with the envelope's details (omitted when nil).
func writeErrDetails(w http.ResponseWriter, status int, code, msg string, details any) {
	writeJSON(w, status, model.APIError{Error: msg, Code: code, RequestID: w.Header().Get(requestIDHeader), Details: details})
}

// maxBody caps JSON request bodies.
const maxBody = 64 << 10

// decodeStrict reads a JSON body with a declared shape into v: a field v does not have is an
// error (the action framework's input structs). It answers 400 itself and reports false on
// failure.
//
// The read deadline bounds the whole body. On failure it is left set, so net/http's drain of
// the unread remainder fails at once and closes the connection instead of waiting for a
// client that stalled mid-body. On success the rest of the body is read while the deadline
// still applies, then the deadline is cleared. On Go 1.27 net/http clears it itself when it
// starts the background read after the body is drained; the explicit clear keeps a deadline
// from outliving the body (and, once it fired, cancelling the request context of a handler
// that outlasts the timeout) independent of the Go version.
func (s *Server) decodeStrict(w http.ResponseWriter, r *http.Request, v any) bool {
	rc := http.NewResponseController(w)
	armed := rc.SetReadDeadline(time.Now().Add(s.bodyTimeout)) == nil
	body := http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid request body")
		return false
	}
	if _, err := io.Copy(io.Discard, body); err != nil {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "invalid request body")
		return false
	}
	if armed {
		_ = rc.SetReadDeadline(time.Time{})
	}
	return true
}

// firstOf returns the first non-empty query value among keys.
func firstOf(r *http.Request, keys ...string) string {
	q := r.URL.Query()
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// me answers GET /api/v1/me (design L.4). For a person, scopes are what they may put on a token
// (Settings -> Tokens offers exactly these): read and logs (only read for the anonymous viewer,
// which every log route refuses), and for an administrator actions:<plugin> for each instance
// with mounted actions. For a token they are its own scopes. capabilities.actions says whether
// the principal can run some action, capabilities.tokens whether it may create personal access
// tokens, capabilities.oauth whether dash serves its OAuth authorization server (Settings ->
// Connected apps). mcp and push are later releases.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r.Context())
	scopes := []string{"read", "logs"}
	if p.Kind == auth.KindAnonymous {
		scopes = []string{"read"}
	}
	me := model.Me{
		User: p.User, Role: string(p.Role), AuthMode: s.authMode(p), Audience: cmp.Or(p.Audience, "api"), Version: s.d.Version,
		Scopes: scopes,
		// Every principal reads /me: a URL's userinfo (credentials) is never in it.
		UI:           model.MeUI{IconBaseURL: config.RedactURLs(s.d.Cfg.IconBaseURL).(string)},
		Warnings:     []string{},
		Capabilities: model.MeCapabilities{Tokens: p.Interactive() && s.tokensAvailable(), OAuth: s.d.OAuth != nil},
	}
	// Standing warnings of the authenticator, for the SPA's banners. admin_wildcard and auth_none
	// go to everyone, as they always did; anonymous_viewer and oidc_open are for administrators,
	// who can act on them.
	if w, ok := s.d.Auth.(auth.Warner); ok {
		for _, code := range w.Warnings() {
			if adminOnlyWarning(code) && !p.Role.AtLeast(auth.RoleAdmin) {
				continue
			}
			me.Warnings = append(me.Warnings, code)
		}
	}
	if s.d.Plugins != nil {
		for _, pi := range s.d.Plugins.Infos() {
			// An action the principal's role cannot run grants no capability: a viewer is not offered
			// controls the server would refuse (design D5).
			if !p.HasScope("actions:"+pi.ID) || !slices.ContainsFunc(pi.Actions, func(a model.ActionInfo) bool { return p.Role.AtLeast(auth.Role(a.MinRole)) }) {
				continue
			}
			me.Capabilities.Actions = true
			// The actions scope is an administrator's (design M.3).
			if p.Role.AtLeast(auth.RoleAdmin) {
				me.Scopes = append(me.Scopes, "actions:"+pi.ID)
			}
		}
	}
	if p.Kind == auth.KindToken {
		me.Scopes = append([]string{}, p.Scopes...) // a token's own scopes, as it was created
	}
	writeJSON(w, http.StatusOK, me)
}

// tokensAvailable reports whether this dash keeps a personal access token it creates (contract
// D1): a credential store that outlives the process, or the in-memory one of dev and demo,
// whose tokens are documented as lost at restart. Without a store (auth.mode=none) or a
// writable data dir in local, oidc and proxy mode, it does not.
func (s *Server) tokensAvailable() bool {
	return s.d.Tokens != nil && s.d.Credentials != nil && (s.d.Credentials.Persistent() || s.d.Cfg.Dev || s.d.Cfg.Demo)
}

// adminOnlyWarning reports whether Me carries the warning to administrators only.
func adminOnlyWarning(code string) bool {
	return code == auth.WarningAnonymousViewer || code == auth.WarningOIDCOpen
}

// errInvalid reports whether err is an upstream input validation failure.
func errInvalid(err error) bool { return errors.Is(err, upstream.ErrInvalid) }
