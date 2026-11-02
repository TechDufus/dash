package plugintest

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

var attentionItem = reflect.TypeFor[model.AttentionItem]()

// Links is LinksFn, so a Fake is a plugin.LinkProvider that offers nothing until LinksFn is set.
func (f *Fake) Links(req plugin.LinkRequest) []model.Link {
	if f.LinksFn == nil {
		return nil
	}
	return f.LinksFn(req)
}

type withDemo struct {
	*Fake
	twin plugin.Plugin
}

func (d withDemo) Demo() plugin.Plugin { return d.twin }

// WithDemo is f as a factory for a plugin that also implements plugin.Demo: under Options.Demo
// the supervisor runs twin instead. Fakes are not demo-capable otherwise, so a test can also
// check that a plugin without a twin is left alone.
func WithDemo(f *Fake, twin plugin.Plugin) plugin.Factory {
	return func() plugin.Plugin { return withDemo{f, twin} }
}

// AssertRefs walks payload and checks every object reference in it (design L.6): each struct with
// a `Ref string` field has it non-empty, plugin.ParseRef accepts it, its plugin is pluginID,
// which must be registered in sup (nil skips that check), and no two objects of the payload share
// one (an error names both; a model.AttentionItem is exempt from that last rule, since it names
// the object it is about by that object's ref). mustHave lists a zero value of every
// type the leaf's payload is expected to carry refs on: each must declare a Ref field and occur
// in the payload at least once, so a type that forgot its field, or a payload that does not
// reach it, fails instead of being skipped.
func AssertRefs(t testing.TB, sup *plugin.Supervisor, pluginID plugin.ID, payload any, mustHave ...any) {
	t.Helper()
	if sup != nil {
		found := false
		for _, m := range sup.Manifests() {
			found = found || m.ID == pluginID
		}
		if !found {
			t.Errorf("plugin %q is not registered in the supervisor", pluginID)
		}
	}
	seen := map[reflect.Type]int{}
	owners := map[string]string{} // ref -> where it was first seen
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(path, v.Elem())
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(path+"["+strconv.Itoa(i)+"]", v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(path+"["+k.String()+"]", v.MapIndex(k))
			}
		case reflect.Struct:
			seen[v.Type()]++
			if f := v.FieldByName("Ref"); f.IsValid() && f.Kind() == reflect.String {
				ref := f.String()
				p, _, _, ok := plugin.ParseRef(ref)
				switch {
				case ref == "":
					t.Errorf("%s (%s): empty ref", path, v.Type())
				case !ok:
					t.Errorf("%s (%s): %q is not a valid ref", path, v.Type(), ref)
				case p != pluginID:
					t.Errorf("%s (%s): ref %q belongs to plugin %q, not %q", path, v.Type(), ref, p, pluginID)
				case v.Type() == attentionItem:
					// An attention item carries the ref of the object it is about, which is also
					// the ref of that object in the payload: it is checked for form, not for
					// uniqueness.
				case owners[ref] != "":
					t.Errorf("%s (%s): ref %q is also the ref of %s", path, v.Type(), ref, owners[ref])
				default:
					owners[ref] = path + " (" + v.Type().String() + ")"
				}
			}
			for i := range v.NumField() {
				if v.Type().Field(i).IsExported() {
					walk(path+"."+v.Type().Field(i).Name, v.Field(i))
				}
			}
		}
	}
	walk("payload", reflect.ValueOf(payload))
	for _, zero := range mustHave {
		typ := reflect.TypeOf(zero)
		if f, ok := typ.FieldByName("Ref"); !ok || f.Type.Kind() != reflect.String {
			t.Errorf("%s has no `Ref string` field", typ)
			continue
		}
		if seen[typ] == 0 {
			t.Errorf("the payload holds no %s, so its refs were not checked", typ)
		}
	}
}
