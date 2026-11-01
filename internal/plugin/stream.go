package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

// StreamSpec documents a stream route (Routes.Stream).
type StreamSpec struct {
	Summary string
	// Scope is the token scope of the route ("logs"). Documented now: the core checks the role
	// only, and enforces scopes for bearer tokens from phase 7.
	Scope string
	// Query documents the query parameters of the route in OpenAPI.
	Query []Param
	// Validate checks the request before the core takes a stream slot or writes an audit record: a
	// request that can never succeed (a missing namespace or pod) is refused without costing
	// either. Its error is answered like a handler's error before Begin: an *ActionError as it
	// is, an error wrapping upstream.ErrInvalid as 400, anything else as an upstream failure.
	// nil accepts every request.
	Validate func(r *http.Request) error
	// Target names the object a client opens a stream on, for the audit record the core writes
	// at every open (design F.5). Nil records no target.
	Target func(r *http.Request) (kind, namespace, name string)
	// Namespace is RouteSpec.Namespace for the route: the namespace the open targets.
	Namespace func(r *http.Request) string
}

// Stream is one open server-sent-events response, as a StreamHandler sees it.
type Stream interface {
	// Begin writes the SSE headers and the retry hint. Before Begin a handler may return an
	// error and the client gets the standard error envelope; after it the stream just closes.
	Begin() error
	// Event JSON-encodes data and writes it as one event under the core's write deadline. id is
	// the SSE id the browser echoes as Last-Event-ID when it reconnects, "" for none. Event does
	// not scrub: the plugin scrubs text before it builds data (Host.ScrubLog).
	Event(event, id string, data any) error
}

// StreamHandler serves one stream. ctx ends when the client leaves, the server shuts down or the
// stream's maximum duration passes (the browser's EventSource then reconnects by itself, with
// Last-Event-ID, which is in r). p is the principal that opened the stream. Returning ends the
// stream: an error before Stream.Begin is answered as an action error (an *ActionError as it is,
// anything else through Fail).
type StreamHandler func(ctx context.Context, r *http.Request, p auth.Principal, s Stream) error

// MountedRoute is one route of a running instance, as the core's guard sees it.
type MountedRoute struct {
	Instance ID
	Info     RouteInfo
	// ReadDenyNamespaces is the instance's InstanceConfig.ReadDenyNamespaces now.
	ReadDenyNamespaces []string
	// Context ends when the instance that mounted the route stops (a reconfiguration, a disable, a
	// shutdown): a stream the core serves for it must end then too, not run on for its maximum
	// duration against a plugin that has stopped.
	Context context.Context
}

// RouteGuard is how the core serves the routes of a plugin without the plugin package knowing
// the API (the supervisor cannot import it). The API passes one to Supervisor.Router with every
// request; the zero value serves plain GET routes unguarded and refuses stream routes.
type RouteGuard struct {
	// Serve runs one route inside the instance's mux, after ServeMux has set the path values. It
	// applies the core's policy and calls next, the plugin's handler, only when the request may
	// proceed. next is nil for a stream route: Serve then runs mr.Info.StreamHandler itself.
	Serve func(w http.ResponseWriter, r *http.Request, mr MountedRoute, next http.HandlerFunc)
	// Fail answers an error of a route handler of instance id: the category-only upstream error
	// of the core (never a URL or host name; the detail goes to the log), 400 for invalid input.
	Fail func(w http.ResponseWriter, r *http.Request, id ID, err error)
}

type guardKey struct{}

type guardValue struct {
	id    ID
	guard RouteGuard
}

func withGuard(ctx context.Context, id ID, g RouteGuard) context.Context {
	return context.WithValue(ctx, guardKey{}, guardValue{id, g})
}

func guardFrom(ctx context.Context) guardValue {
	v, _ := ctx.Value(guardKey{}).(guardValue)
	return v
}

// WriteError writes the error envelope (design L.2): status, one of the model.ErrCode* codes,
// and text for a person. The request id is the one the core put on the response.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.APIError{Error: msg, Code: code, RequestID: w.Header().Get("X-Request-Id")})
}

// Fail answers err from a route handler. Inside the API the core answers: an *ActionError as the
// plugin wrote it, an input error as 400, anything else as an upstream failure with a category
// (unreachable, timeout, tls, auth, http_<code>, …) while the detail goes to the log scrubbed, so
// a viewer never sees a URL or a host name; the handler passes the error through unexamined.
// Outside the API (a bare supervisor) an *ActionError is written as it is and anything else is a
// plain 502.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	if g := guardFrom(r.Context()); g.guard.Fail != nil {
		g.guard.Fail(w, r, g.id, err)
		return
	}
	var ae *ActionError
	if errors.As(err, &ae) {
		status, code := ae.Status, ae.Code
		if status < 400 || status > 599 {
			status, code = http.StatusInternalServerError, model.ErrCodeInternal
		}
		WriteError(w, status, code, ae.Message)
		return
	}
	WriteError(w, http.StatusBadGateway, model.ErrCodeUpstreamUnavailable, "upstream unavailable")
}
