package victorialogs

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/upstream"
)

// maxTailResume is how far back a reconnecting log tail replays.
const maxTailResume = time.Minute

// logParams documents the query parameters both routes read.
var logParams = []plugin.Param{
	{Name: "ns", Doc: "Namespace of the pod (required). namespace is accepted as well."},
	{Name: "pod", Doc: "Pod name. Empty selects every pod of the namespace."},
	{Name: "container", Doc: "Container name. Empty selects every container."},
	{Name: "q", Doc: "Text a line must contain."},
	{Name: "since", Doc: "How far back a query looks: 5m, 15m (default), 1h, 6h or 24h. A tail ignores it."},
}

// Routes implements plugin.Router.
func (p *Plugin) Routes(r plugin.Routes) {
	r.Get("/logs", p.logs, plugin.RouteSpec{
		Summary:   "Log lines of a pod",
		Scope:     "logs",
		Query:     logParams,
		Namespace: namespaceOf,
		Response:  []model.LogLine{},
	})
	r.Stream("/logs/tail", p.tail, plugin.StreamSpec{
		Summary:   "Live log lines of a pod",
		Scope:     "logs",
		Query:     logParams,
		Validate:  p.validateTail,
		Target:    podTarget,
		Namespace: namespaceOf,
	})
}

// validateTail answers a bad query with the 400 envelope before the core takes a stream slot
// or writes the audit record. A plugin that is not running passes: the handler answers 503.
func (p *Plugin) validateTail(r *http.Request) error {
	st := p.st.Load()
	if st == nil {
		return nil
	}
	_, err := upstream.BuildQuery(queryFrom(r), st.fields, true)
	return err
}

// namespaceOf is the namespace a request targets, which the core checks against denyNamespaces
// before the handler runs (and, for the tail, before it takes a stream slot).
func namespaceOf(r *http.Request) string {
	q := r.URL.Query()
	if ns := q.Get("ns"); ns != "" {
		return ns
	}
	return q.Get("namespace")
}

// podTarget is the audit target of a tail open.
func podTarget(r *http.Request) (kind, namespace, name string) {
	return "Pod", namespaceOf(r), r.URL.Query().Get("pod")
}

func queryFrom(r *http.Request) upstream.LogQuery {
	q := r.URL.Query()
	return upstream.LogQuery{
		Namespace: namespaceOf(r),
		Pod:       q.Get("pod"),
		Container: q.Get("container"),
		Text:      q.Get("q"),
		Since:     q.Get("since"),
	}
}

// errNotRunning is what a request that races Stop or a reload gets.
var errNotRunning = &plugin.ActionError{
	Status: http.StatusServiceUnavailable, Code: model.ErrCodeUpstreamUnavailable, Message: "logs are not available",
}

// logs serves GET logs: up to the configured limit of the newest matching lines, oldest first.
// Every Msg is scrubbed before the payload is encoded.
func (p *Plugin) logs(w http.ResponseWriter, r *http.Request) {
	st := p.st.Load()
	if st == nil {
		plugin.Fail(w, r, errNotRunning)
		return
	}
	lines, err := st.src.Query(r.Context(), queryFrom(r))
	if err != nil {
		plugin.Fail(w, r, err)
		return
	}
	out := make([]model.LogLine, len(lines)) // an array, never null
	for i, l := range lines {
		l.Msg = st.scrub(l.Msg)
		out[i] = l
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// tail serves GET logs/tail. The core has checked the role and the namespace policy, taken the
// slot, audited the open and set the maximum duration, the write deadline and the heartbeat;
// the handler ends when ctx does.
//
// A reconnecting EventSource sends the id of the last line it saw: one resume point per
// container. The tail replays from just before the oldest, never further back than
// maxTailResume, and drops only what the client already has from each container. A malformed id
// means a plain tail.
func (p *Plugin) tail(ctx context.Context, r *http.Request, _ auth.Principal, s plugin.Stream) error {
	st := p.st.Load()
	if st == nil {
		return errNotRunning
	}
	q := queryFrom(r)
	// The core ran validateTail before the slot; BuildQuery is checked again here because the
	// instance may have been replaced in between.
	if _, err := upstream.BuildQuery(q, st.fields, true); err != nil {
		return err
	}
	resume, seen := resumePoints{}, resumePoints{}
	if pts, ok := parseResumePoints(r.Header.Get("Last-Event-ID")); ok {
		resume, seen = pts, pts.clone() // seen starts from the points so quiet containers survive the next reconnect
		q.StartOffset = min(max(st.now().Sub(pts.oldest())+5*time.Second, 0), maxTailResume)
	}
	if err := s.Begin(); err != nil {
		return err
	}
	err := st.src.Tail(ctx, q, func(l model.LogLine) error {
		if at, ok := resume[l.Container]; ok && !l.Time.After(at) { // already delivered before the reconnect
			return nil
		}
		l.Msg = st.scrub(l.Msg)
		if l.Time.IsZero() {
			return s.Event("line", "", l)
		}
		seen.note(l.Container, l.Time)
		return s.Event("line", seen.String(), l)
	})
	if ctx.Err() != nil { // the client left or the maximum duration passed: the normal end
		return nil
	}
	return err
}
