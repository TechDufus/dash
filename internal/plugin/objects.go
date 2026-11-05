package plugin

import (
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Objects is implemented by a plugin whose objects a client can find, read and ask a context
// bundle for (design N.4): the MCP tools dash_search, dash_get and dash_context, and
// GET /api/v1/context. Every ref it takes and returns is one of its own (<plugin>:<Kind>/<id>).
type Objects interface {
	// Search returns at most limit objects whose ref or title holds every word of query
	// (ignoring case); an empty query matches every object. The order is the plugin's and stable.
	Search(query string, limit int) []model.SearchHit
	// Get returns one object. A ref the plugin does not know is ErrNotFound (NotFound).
	Get(ctx context.Context, ref string) (any, error)
	// Context returns the plugin's part of the object's context bundle.
	Context(ctx context.Context, ref string, o model.ContextOptions) (model.ContextPart, error)
}

// refusedKinds are the kinds no object tool ever returns (design N.5): their data is secret or
// configuration a workload reads, not state.
var refusedKinds = []string{"Secret", "ConfigMap"}

// RefusedKind reports whether objects of kind are never returned.
func RefusedKind(kind string) bool {
	return slices.ContainsFunc(refusedKinds, func(k string) bool { return strings.EqualFold(k, kind) })
}

// ObjectStore answers Search and Get from the payloads a running plugin last published: every
// struct in them with a non-empty Ref string field is an object. A plugin embeds it, publishes
// through the Host that Wrap returns, and implements Context (with Part for the common half).
// The zero value is ready; it knows nothing until the first publish.
type ObjectStore struct {
	mu     sync.RWMutex
	topics []string       // in the order of their first publish
	latest map[string]any // the last payload by topic
}

// Wrap returns h with its Publish recording each payload before it is published.
func (s *ObjectStore) Wrap(h Host) Host {
	if h == nil {
		return nil
	}
	return storeHost{Host: h, s: s}
}

type storeHost struct {
	Host
	s *ObjectStore
}

func (h storeHost) Publish(topic string, data any, sources []model.SourceStatus) {
	h.s.record(topic, data)
	h.Host.Publish(topic, data, sources)
}

func (s *ObjectStore) record(topic string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest == nil {
		s.latest = map[string]any{}
	}
	if _, ok := s.latest[topic]; !ok {
		s.topics = append(s.topics, topic)
	}
	s.latest[topic] = data
}

// Latest returns the payload last published on topic (its short name, as given to Publish).
func (s *ObjectStore) Latest(topic string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.latest[topic]
	return d, ok
}

// objects lists every object of every payload, topic by topic in publish order.
func (s *ObjectStore) objects() []reflect.Value {
	s.mu.RLock()
	payloads := make([]any, 0, len(s.topics))
	for _, t := range s.topics {
		payloads = append(payloads, s.latest[t])
	}
	s.mu.RUnlock()
	var out []reflect.Value
	seen := map[string]bool{}
	for _, p := range payloads {
		walkObjects(reflect.ValueOf(p), func(v reflect.Value) {
			ref := v.FieldByName("Ref").String()
			if !seen[ref] {
				seen[ref] = true
				out = append(out, v)
			}
		})
	}
	return out
}

// walkObjects calls fn for every struct below v that has a non-empty string field Ref, and
// goes on into its fields.
func walkObjects(v reflect.Value, fn func(reflect.Value)) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkObjects(v.Elem(), fn)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			walkObjects(v.Index(i), fn)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			walkObjects(v.MapIndex(k), fn)
		}
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			return
		}
		if f := v.FieldByName("Ref"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
			if _, kind, _, ok := model.ParseRef(f.String()); ok && !RefusedKind(kind) {
				fn(v)
			}
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				walkObjects(v.Field(i), fn)
			}
		}
	}
}

// Find returns the object ref names, as the struct value it is in the payload.
func (s *ObjectStore) Find(ref string) (any, bool) {
	for _, v := range s.objects() {
		if v.FieldByName("Ref").String() == ref {
			return v.Interface(), true
		}
	}
	return nil, false
}

// Search implements Objects.Search over the published objects.
func (s *ObjectStore) Search(query string, limit int) []model.SearchHit {
	words := strings.Fields(strings.ToLower(query))
	out := []model.SearchHit{}
	if limit <= 0 {
		return out
	}
	for _, v := range s.objects() {
		h := hitOf(v)
		hay := strings.ToLower(h.Ref + " " + h.Title)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			out = append(out, h)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// Get implements Objects.Get: the object as the payload carries it.
func (s *ObjectStore) Get(_ context.Context, ref string) (any, error) {
	if v, ok := s.Find(ref); ok {
		return v, nil
	}
	return nil, NotFound("unknown object")
}

// Part is the common half of a context part: the ref, kind, title, summary and facts of the
// object, and its links. It is ErrNotFound for an unknown ref.
func (s *ObjectStore) Part(ref string) (model.ContextPart, any, error) {
	for _, v := range s.objects() {
		if v.FieldByName("Ref").String() != ref {
			continue
		}
		h := hitOf(v)
		p := model.ContextPart{
			Ref: h.Ref, Kind: h.Kind, Title: h.Title, Summary: h.Summary, Facts: factsOf(v),
			Conditions: []model.Condition{}, Events: []model.Event{}, Related: []string{},
			Links: []model.Link{}, Series: []model.SeriesRef{},
		}
		if f := v.FieldByName("Links"); f.IsValid() && f.CanInterface() {
			if l, ok := f.Interface().([]model.Link); ok && l != nil {
				p.Links = slices.Clone(l)
			}
		}
		return p, v.Interface(), nil
	}
	return model.ContextPart{}, nil, NotFound("unknown object")
}

// StoreContext implements Objects.Context with the common half alone, for a plugin whose
// objects have nothing more to say.
func (s *ObjectStore) StoreContext(ref string) (model.ContextPart, error) {
	p, _, err := s.Part(ref)
	return p, err
}

// summaryFields are the JSON names of the fields a hit's summary shows, in order.
var summaryFields = []string{"health", "healthy", "status", "phase", "state", "severity", "ready", "up"}

func hitOf(v reflect.Value) model.SearchHit {
	ref := v.FieldByName("Ref").String()
	_, kind, id, _ := model.ParseRef(ref)
	title := ""
	for _, name := range []string{"Name", "Title"} {
		if f := v.FieldByName(name); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
			title = f.String()
			break
		}
	}
	if title == "" {
		title = id[strings.LastIndexByte(id, '/')+1:]
	}
	var parts []string
	facts := factsOf(v)
	for _, name := range summaryFields {
		if i := slices.IndexFunc(facts, func(f model.ContextFact) bool { return f.Name == name }); i >= 0 && facts[i].Value != "" {
			parts = append(parts, name+" "+facts[i].Value)
		}
	}
	return model.SearchHit{Ref: ref, Kind: kind, Title: title, Summary: strings.Join(parts, ", ")}
}

// factsOf lists the scalar fields of struct v by their JSON names, in field order: strings,
// numbers, booleans and times, through pointers, and the entries of a map[string]string as
// <name>.<key>. The ref, links and lists are not facts.
func factsOf(v reflect.Value) []model.ContextFact {
	out := []model.ContextFact{}
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		name := jsonName(sf)
		if name == "" || name == "ref" {
			continue
		}
		f := v.Field(i)
		if f.Kind() == reflect.Pointer {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		if f.Kind() == reflect.Map && f.Type().Key().Kind() == reflect.String && f.Type().Elem().Kind() == reflect.String {
			keys := f.MapKeys()
			sort.Slice(keys, func(a, b int) bool { return keys[a].String() < keys[b].String() })
			for _, k := range keys {
				out = append(out, model.ContextFact{Name: name + "." + k.String(), Value: f.MapIndex(k).String()})
			}
			continue
		}
		if s, ok := scalar(f); ok {
			out = append(out, model.ContextFact{Name: name, Value: s})
		}
	}
	return out
}

func jsonName(sf reflect.StructField) string {
	if !sf.IsExported() {
		return ""
	}
	tag := sf.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if n, _, _ := strings.Cut(tag, ","); n != "" {
		return n
	}
	return sf.Name
}

func scalar(f reflect.Value) (string, bool) {
	if f.Type() == reflect.TypeFor[time.Time]() {
		t := f.Interface().(time.Time)
		if t.IsZero() {
			return "", false
		}
		return t.UTC().Format(time.RFC3339), true
	}
	switch f.Kind() {
	case reflect.String:
		return f.String(), true
	case reflect.Bool:
		return strconv.FormatBool(f.Bool()), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(f.Int(), 10), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(f.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(f.Float(), 'g', 6, 64), true
	}
	return "", false
}

// NewestEvents returns at most model.ContextMaxEvents of evs, newest first, never nil.
func NewestEvents(evs []model.Event) []model.Event {
	out := slices.Clone(evs)
	slices.SortStableFunc(out, func(a, b model.Event) int { return b.LastSeen.Compare(a.LastSeen) })
	if len(out) > model.ContextMaxEvents {
		out = out[:model.ContextMaxEvents]
	}
	if out == nil {
		out = []model.Event{}
	}
	return out
}

// Objects returns the Objects of running instance id, each call guarded: a panic in the plugin
// is logged and answers as an internal error (an empty search).
func (s *Supervisor) Objects(id ID) (Objects, bool) {
	in := s.lookup(id)
	if in == nil {
		return nil, false
	}
	in.mu.RLock()
	running, p := in.running, in.p
	in.mu.RUnlock()
	o, ok := p.(Objects)
	if !running || !ok {
		return nil, false
	}
	return guardedObjects{s: s, id: id, o: o}, true
}

// ObjectInstances lists the running instances that implement Objects, in registry order.
func (s *Supervisor) ObjectInstances() []ID {
	var out []ID
	for _, in := range s.registry() {
		in.mu.RLock()
		running, p := in.running, in.p
		in.mu.RUnlock()
		if _, ok := p.(Objects); ok && running {
			out = append(out, in.id)
		}
	}
	return out
}

// errObjectsPanic is what a guarded call answers when the plugin panicked.
var errObjectsPanic = fmt.Errorf("internal error")

type guardedObjects struct {
	s  *Supervisor
	id ID
	o  Objects
}

func (g guardedObjects) recovered(method string, rec any) {
	g.s.log().Error("panic in "+method, "plugin", g.id, "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
}

func (g guardedObjects) Search(query string, limit int) (out []model.SearchHit) {
	defer func() {
		if r := recover(); r != nil {
			g.recovered("Search", r)
			out = []model.SearchHit{}
		}
	}()
	return g.o.Search(query, limit)
}

func (g guardedObjects) Get(ctx context.Context, ref string) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			g.recovered("Get", r)
			out, err = nil, errObjectsPanic
		}
	}()
	return g.o.Get(ctx, ref)
}

func (g guardedObjects) Context(ctx context.Context, ref string, o model.ContextOptions) (out model.ContextPart, err error) {
	defer func() {
		if r := recover(); r != nil {
			g.recovered("Context", r)
			out, err = model.ContextPart{}, errObjectsPanic
		}
	}()
	return g.o.Context(ctx, ref, o)
}
