package plugintest

import (
	"fmt"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// recorder is a testing.TB that collects Errorf calls instead of failing.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Helper()                   {}
func (r *recorder) Errorf(f string, a ...any) { r.errs = append(r.errs, fmt.Sprintf(f, a...)) }

type obj struct {
	Ref  string
	Name string
}

type noRef struct{ Name string }

type payload struct {
	Items  []obj
	Nested map[string]*obj
	Other  []noRef
}

func TestAssertRefs(t *testing.T) {
	ok := payload{
		Items:  []obj{{Ref: "aa:Item/1"}, {Ref: "aa:Item/ns/two"}},
		Nested: map[string]*obj{"k": {Ref: "aa:Item/3"}},
		Other:  []noRef{{Name: "ignored: no Ref field"}},
	}
	for _, c := range []struct {
		name    string
		payload any
		must    []any
		wantErr string // a substring of the first error; "" = none
	}{
		{"all valid", ok, []any{obj{}}, ""},
		{"pointer payload", &ok, nil, ""},
		{"empty ref", payload{Items: []obj{{Ref: ""}}}, nil, "empty ref"},
		{"malformed ref", payload{Items: []obj{{Ref: "not a ref"}}}, nil, "not a valid ref"},
		{"another plugin's ref", payload{Items: []obj{{Ref: "bb:Item/1"}}}, nil, `belongs to plugin "bb"`},
		{"nested value checked", payload{Nested: map[string]*obj{"k": {Ref: ""}}}, nil, "empty ref"},
		{"must-have type without a Ref field", ok, []any{noRef{}}, "has no `Ref string` field"},
		{"must-have type absent from the payload", payload{}, []any{obj{}}, "holds no"},
		{"duplicate ref in one list", payload{Items: []obj{{Ref: "aa:Item/1"}, {Ref: "aa:Item/1"}}}, nil, `ref "aa:Item/1" is also the ref of payload.Items[0]`},
		{"duplicate ref across fields", payload{Items: []obj{{Ref: "aa:Item/1"}}, Nested: map[string]*obj{"k": {Ref: "aa:Item/1"}}}, nil, `is also the ref of payload.Items[0] (plugintest.obj)`},
	} {
		r := &recorder{}
		AssertRefs(r, nil, "aa", c.payload, c.must...)
		switch {
		case c.wantErr == "" && len(r.errs) != 0:
			t.Errorf("%s: unexpected errors %v", c.name, r.errs)
		case c.wantErr != "" && (len(r.errs) == 0 || !contains(r.errs[0], c.wantErr)):
			t.Errorf("%s: errors %v, want one with %q", c.name, r.errs, c.wantErr)
		}
	}
}

func TestAssertAttentionNonNil(t *testing.T) {
	for _, c := range []struct {
		name    string
		fn      func(time.Time) []model.AttentionItem
		wantErr string // a substring of the first error; "" = none
	}{
		{"nil list", func(time.Time) []model.AttentionItem { return nil }, "nil attention list"},
		{"empty list", func(time.Time) []model.AttentionItem { return []model.AttentionItem{} }, ""},
		{"items", func(time.Time) []model.AttentionItem { return []model.AttentionItem{{ID: "x"}} }, ""},
	} {
		f := New("aa")
		f.AttentionFn = c.fn
		r := &recorder{}
		AssertAttentionNonNil(r, plugin.Contributor{ID: "aa", Attention: f})
		switch {
		case c.wantErr == "" && len(r.errs) != 0:
			t.Errorf("%s: unexpected errors %v", c.name, r.errs)
		case c.wantErr != "" && (len(r.errs) != 1 || !contains(r.errs[0], c.wantErr)):
			t.Errorf("%s: errors %v, want one with %q", c.name, r.errs, c.wantErr)
		}
	}

	r := &recorder{}
	AssertAttentionNonNil(r, plugin.Contributor{ID: "aa"})
	if len(r.errs) != 1 || !contains(r.errs[0], "no AttentionSource") {
		t.Errorf("no source: errors %v", r.errs)
	}
}

type withAttention struct {
	Items     []obj
	Attention []model.AttentionItem
}

// An attention item carries the ref of the object it is about, so sharing it with that object is
// not a duplicate; the item's ref is still checked for form and plugin, and two objects sharing
// one are still caught.
func TestAssertRefsLetsAttentionItemsShareTheirObjectsRef(t *testing.T) {
	shared := withAttention{
		Items:     []obj{{Ref: "aa:Item/1"}},
		Attention: []model.AttentionItem{{ID: "x", Ref: "aa:Item/1"}, {ID: "y", Ref: "aa:Item/1"}},
	}
	r := &recorder{}
	AssertRefs(r, nil, "aa", shared, obj{}, model.AttentionItem{})
	if len(r.errs) != 0 {
		t.Errorf("attention items sharing their object's ref: %v", r.errs)
	}

	for name, bad := range map[string]withAttention{
		"an attention item of another plugin": {Items: []obj{{Ref: "aa:Item/1"}}, Attention: []model.AttentionItem{{Ref: "bb:Item/1"}}},
		"an attention item with no ref":       {Items: []obj{{Ref: "aa:Item/1"}}, Attention: []model.AttentionItem{{ID: "x"}}},
		"two objects sharing a ref":           {Items: []obj{{Ref: "aa:Item/1"}, {Ref: "aa:Item/1"}}},
	} {
		r := &recorder{}
		AssertRefs(r, nil, "aa", bad)
		if len(r.errs) == 0 {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestAssertRefsChecksThePluginIsRegistered(t *testing.T) {
	sup := plugin.NewSupervisor(plugin.Options{})
	if err := sup.Add(New("aa").Factory()); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	AssertRefs(r, sup, "aa", payload{Items: []obj{{Ref: "aa:Item/1"}}})
	if len(r.errs) != 0 {
		t.Errorf("registered plugin: %v", r.errs)
	}
	r = &recorder{}
	AssertRefs(r, sup, "zz", payload{})
	if len(r.errs) != 1 || !contains(r.errs[0], "not registered") {
		t.Errorf("unregistered plugin: %v", r.errs)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// AllRunning forwards a topic under the name the supervisor gives it: the Legacy name if the
// manifest declares one, "<plugin>.<name>" otherwise.
func TestAllRunningNamesTopicsLikeTheSupervisor(t *testing.T) {
	rec := &Hub{}
	man := plugin.Manifest{ID: "aa", Topics: []plugin.TopicSpec{{Name: "rows", Legacy: "old"}, {Name: "plain"}}}
	hp := AllRunning(rec, man)
	for i, spec := range man.Topics {
		hp.HostFor(man.ID).Publish(spec.Name, i, nil)
		want := plugin.TopicName(man.ID, spec)
		if ev, ok := rec.Last(want); !ok || ev.Data != i {
			t.Errorf("topic %q (published as %q) was not forwarded under %q: %+v", spec.Name, spec.Name, want, rec.Published())
		}
	}
	if _, ok := rec.Last("old"); !ok {
		t.Error("the legacy name was not used")
	}
	if _, ok := rec.Last("aa.plain"); !ok {
		t.Error("the unnamed topic is not aa.plain")
	}
}
