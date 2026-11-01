package plugintest

import (
	"fmt"
	"testing"

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
