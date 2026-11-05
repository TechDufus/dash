package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/bundle"
)

// topicURIPrefix starts the URI of a topic resource: dash://topics/<topic> (design N.3).
const topicURIPrefix = "dash://topics/"

// topicOf is the topic a resource URI names, if it names one.
func topicOf(uri string) (string, bool) {
	t, ok := strings.CutPrefix(uri, topicURIPrefix)
	return t, ok && topicRE.MatchString(t)
}

// listTopics answers resources/list: the topics c may read, global ones and c's own copies.
func (s *Server) listTopics(c caller) *sdk.ListResourcesResult {
	out := &sdk.ListResourcesResult{Resources: []*sdk.Resource{}}
	if !c.p.HasScope("read") {
		return out
	}
	for _, t := range s.o.Backend.Topics(c.p.User) {
		out.Resources = append(out.Resources, &sdk.Resource{
			URI: topicURIPrefix + t.Name, Name: t.Name, Title: t.Name, MIMEType: "application/json",
			Description: "The current snapshot of the " + t.Name + " topic.",
		})
	}
	return out
}

// readTopic answers resources/read of dash://topics/<topic>: the envelope as the caller reads it
// (its own copy of a user-scoped topic), every string scrubbed. It needs the read scope.
func (s *Server) readTopic(_ context.Context, req *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
	c, ok := callerOf(req.Extra)
	topic, named := topicOf(req.Params.URI)
	if !ok || !named || !c.p.HasScope("read") {
		return nil, sdk.ResourceNotFoundError(req.Params.URI)
	}
	env, found := s.o.Backend.Topic(c.p.User, topic)
	if !found {
		return nil, sdk.ResourceNotFoundError(req.Params.URI)
	}
	tree, err := bundle.Scrub(env, s.o.Scrub)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(tree)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxResultBytes { // design N.6: cut, flagged; the JSON then does not parse
		return &sdk.ReadResourceResult{Meta: sdk.Meta{"truncated": true}, Contents: []*sdk.ResourceContents{{
			URI: req.Params.URI, MIMEType: "text/plain", Text: s.cut(string(raw), MaxResultBytes),
			Meta: sdk.Meta{"truncated": true},
		}}}, nil
	}
	return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{
		URI: req.Params.URI, MIMEType: "application/json", Text: string(raw),
	}}}, nil
}

// checkSubscriptions refuses a subscriptions/listen that asks for a resource c may not follow:
// only the global topics, and only with the read scope. A user-scoped topic is refused because
// its one URI serves every user, so its updates would tell one user when another's changed.
func (s *Server) checkSubscriptions(c caller, uris []string) error {
	if len(uris) == 0 {
		return nil
	}
	if !c.p.HasScope("read") {
		return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "subscriptions need the read scope"}
	}
	topics := s.o.Backend.Topics(c.p.User)
	for _, uri := range uris {
		t, ok := topicOf(uri)
		i := slices.IndexFunc(topics, func(x TopicRef) bool { return x.Name == t })
		if !ok || i < 0 {
			return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown resource " + uri}
		}
		if topics[i].Scoped {
			return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "a user-scoped topic cannot be subscribed to: " + uri}
		}
	}
	return nil
}

// watch turns the hub's changes into resource updates, at most one per topic per Debounce.
func (s *Server) watch(ctx context.Context) {
	d := newDebouncer(s.o.Debounce, time.Now, time.AfterFunc, func(topic string) {
		_ = s.srv.ResourceUpdated(ctx, &sdk.ResourceUpdatedNotificationParams{URI: topicURIPrefix + topic})
	})
	for topic := range s.o.Backend.Changes(ctx) {
		d.changed(topic)
	}
}

// debouncer calls fire for a key at most once per every: the first change fires at once, and
// the changes that follow within every fire once more when it has passed.
type debouncer struct {
	every time.Duration
	now   func() time.Time
	after func(time.Duration, func()) *time.Timer
	fire  func(string)

	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]bool
}

func newDebouncer(every time.Duration, now func() time.Time, after func(time.Duration, func()) *time.Timer, fire func(string)) *debouncer {
	return &debouncer{every: every, now: now, after: after, fire: fire, last: map[string]time.Time{}, pending: map[string]bool{}}
}

func (d *debouncer) changed(key string) {
	d.mu.Lock()
	if d.pending[key] {
		d.mu.Unlock()
		return
	}
	now := d.now()
	last, seen := d.last[key]
	if wait := last.Add(d.every).Sub(now); seen && wait > 0 {
		d.pending[key] = true
		d.mu.Unlock()
		d.after(wait, func() {
			d.mu.Lock()
			d.pending[key] = false
			d.last[key] = d.now()
			d.mu.Unlock()
			d.fire(key)
		})
		return
	}
	d.last[key] = now
	d.mu.Unlock()
	d.fire(key)
}

// errLegacySubscribe answers resources/subscribe, which the stateless legacy protocol cannot
// serve: there is no stream to deliver the updates on.
var errLegacySubscribe = &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "subscriptions need protocol version 2026-07-28 (subscriptions/listen)"}
