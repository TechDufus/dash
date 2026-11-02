package pluginconf

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The flux section moved here from internal/config with its one setting and its tags unchanged,
// so the environment name, the documentation and the file key stay the ones of v0.9.0. It has no
// Validate and no policy hook: the setting is any string.
func TestFluxSectionIsTheOneSettingOfV090(t *testing.T) {
	ty := reflect.TypeFor[Flux]()
	if ty.NumField() != 2 {
		t.Fatalf("Flux has %d fields, want PluginBase and SuspendReasonAnnotation", ty.NumField())
	}
	if f := ty.Field(0); f.Name != "PluginBase" || !f.Anonymous {
		t.Errorf("field 0 = %s, want the embedded PluginBase", f.Name)
	}
	f, ok := ty.FieldByName("SuspendReasonAnnotation")
	if !ok {
		t.Fatal("no SuspendReasonAnnotation")
	}
	if got := f.Tag.Get("json"); got != "suspendReasonAnnotation" {
		t.Errorf("json tag = %q", got)
	}
	if got := f.Tag.Get("dash"); got != "" {
		t.Errorf("dash tag = %q, want none: the setting reloads live and is not a url or secret", got)
	}
	const doc = "Flux object annotation whose value is shown as the reason the object is suspended. Empty hides the reason."
	if got := f.Tag.Get("doc"); got != doc {
		t.Errorf("doc tag = %q", got)
	}

	var v any = &Flux{}
	if _, ok := v.(interface{ Validate() error }); ok {
		t.Error("*Flux has a Validate: the setting is any string")
	}
	if _, ok := v.(interface{ ActionsConfig() Actions }); ok {
		t.Error("*Flux has an action policy before the plugin has an action")
	}
	if _, ok := v.(interface{ ReadDenyConfig() []string }); ok {
		t.Error("*Flux has a read deny list")
	}
}

func TestFluxSectionRoundTripsThroughJSON(t *testing.T) {
	var f Flux
	if err := json.Unmarshal([]byte(`{"enabled":true,"suspendReasonAnnotation":"example.com/suspended"}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.SuspendReasonAnnotation != "example.com/suspended" || f.Enabled == nil || !*f.Enabled {
		t.Errorf("flux = %+v", f)
	}
}
