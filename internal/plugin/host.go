package plugin

import (
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"slices"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// host is the Host of one instance. A Host made for Start (gen != 0) acts only for that start:
// a goroutine that outlives its instance cannot publish into the next one. The Host that
// HostFor hands out (gen 0) follows whatever generation is current. A host with no instance
// belongs to an id that is not registered; everything it is asked is dropped.
type host struct {
	s   *Supervisor
	in  *instance
	gen uint64
}

func (h *host) logger() *slog.Logger {
	id := "unknown"
	if h.in != nil {
		id = string(h.in.id)
	}
	return h.s.log().With("plugin", id)
}

// current reports whether the instance is live for this host. The caller holds in.mu.
func (h *host) current() bool {
	return h.in != nil && h.in.live && (h.gen == 0 || h.gen == h.in.gen)
}

func (h *host) Publish(topic string, data any, sources []model.SourceStatus) {
	in, pub := h.in, h.s.opt.Publisher
	if in == nil || pub == nil {
		return
	}
	in.mu.RLock()
	defer in.mu.RUnlock()
	if !h.current() {
		h.logger().Warn("publish dropped: the plugin is not running", "topic", topic)
		return
	}
	i := slices.IndexFunc(in.man.Topics, func(t TopicSpec) bool { return t.Name == topic })
	if i < 0 {
		h.logger().Error("publish dropped: topic is not declared in the manifest", "topic", topic)
		return
	}
	spec := in.man.Topics[i]
	if spec.Type != nil && payloadType(data) != payloadType(spec.Type) {
		h.logger().Error("publish dropped: payload has the wrong type", "topic", topic, "got", fmt.Sprintf("%T", data), "want", fmt.Sprintf("%T", spec.Type))
		return
	}
	if _, err := pub.Publish(topicName(in.id, spec), data, sources); err != nil {
		h.logger().Error("publish failed", "topic", topic, "error", err)
	}
}

// payloadType is the type of v with pointers dereferenced; nil for no value.
func payloadType(v any) reflect.Type {
	t := reflect.TypeOf(v)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func (h *host) Logger() *slog.Logger { return h.logger() }

func (h *host) Now() time.Time { return h.s.opt.Now() }

func (h *host) HTTPClient(c HTTPConfig) (*http.Client, error) {
	if c.InsecureSkipVerify {
		h.logger().Warn("TLS certificate verification is disabled for this plugin's upstream")
	}
	cl, err := newHTTPClient(c)
	if err != nil {
		return nil, err
	}
	if t := h.s.opt.Transport; t != nil && h.in != nil {
		cl.Transport = t(h.in.id, cl.Transport)
	}
	return cl, nil
}

func (h *host) ScrubLog(s string) string { return h.s.scrubLog(s) }

func (h *host) Links() LinkResolver { return h.s.Links() }

// Service returns what the Provider instance id exports, to an instance that lists id in its
// Requires or Uses while that instance is running.
func (h *host) Service(id ID) (any, bool) {
	if h.in == nil || !slices.Contains(h.in.deps, id) {
		return nil, false
	}
	return h.s.service(id, h.logger())
}

// Service returns what the Provider instance id exports while that instance runs, without the
// declared-dependency check of Host.Service: it is for the core's own code (the topic Manager,
// the API), which is not a plugin and so declares no Requires or Uses.
func (s *Supervisor) Service(id ID) (any, bool) { return s.service(id, s.log()) }

func (s *Supervisor) service(id ID, log *slog.Logger) (any, bool) {
	dep := s.lookup(id)
	if dep == nil {
		return nil, false
	}
	dep.mu.RLock()
	running, p := dep.running, dep.p
	dep.mu.RUnlock()
	if !running {
		return nil, false
	}
	prov, ok := p.(Provider)
	if !ok {
		return nil, false
	}
	var svc any
	if rec, stack := protect(func() { svc = prov.Service() }); rec != nil {
		log.Error("panic in Service", "provider", id, "panic", fmt.Sprint(rec), "stack", string(stack))
		return nil, false
	}
	return svc, svc != nil
}

func (s *Supervisor) scrubLog(line string) string {
	if s.opt.ScrubLog == nil {
		return line
	}
	return s.opt.ScrubLog(line)
}

func (h *host) Go(name string, fn func()) {
	if h.in == nil {
		return
	}
	h.in.mu.RLock()
	ok := h.current()
	h.in.mu.RUnlock()
	if !ok {
		h.logger().Warn("goroutine not started: the plugin is not running", "name", name)
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				h.goPanic(name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// goPanic logs a panic of one of the plugin's goroutines and puts the instance in "error" until
// it is restarted. The process survives.
func (h *host) goPanic(name string, r any, stack []byte) {
	h.logger().Error("panic in plugin goroutine", "name", name, "panic", fmt.Sprint(r), "stack", string(stack))
	in := h.in
	in.mu.Lock()
	if h.current() {
		in.panicMsg = "panic in " + name
	}
	in.mu.Unlock()
	h.s.publishInfos(false)
}

// coreHost is the Host of dash itself: the core.* topics.
type coreHost struct{ s *Supervisor }

func (c coreHost) Publish(topic string, data any, sources []model.SourceStatus) {
	if c.s.opt.Publisher == nil || c.s.shutdown.Load() {
		return
	}
	if !topicRE.MatchString(topic) {
		c.s.log().Error("publish dropped: invalid core topic name", "plugin", Core, "topic", topic)
		return
	}
	if _, err := c.s.opt.Publisher.Publish(model.Topic(string(Core)+"."+topic), data, sources); err != nil {
		c.s.log().Error("publish failed", "plugin", Core, "topic", topic, "error", err)
	}
}

func (c coreHost) Logger() *slog.Logger { return c.s.log().With("plugin", string(Core)) }
func (c coreHost) Now() time.Time       { return c.s.opt.Now() }

func (c coreHost) HTTPClient(cfg HTTPConfig) (*http.Client, error) { return newHTTPClient(cfg) }

func (c coreHost) Service(ID) (any, bool) { return nil, false }

func (c coreHost) ScrubLog(s string) string { return c.s.scrubLog(s) }

func (c coreHost) Links() LinkResolver { return c.s.Links() }

func (c coreHost) Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.s.log().Error("panic in core goroutine", "name", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
