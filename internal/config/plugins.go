package config

import (
	"reflect"
	"strings"
)

// PluginBase is embedded in every plugin section. It adds the two settings every instance has,
// plugins.<id>.type and plugins.<id>.enabled (design A.2, B.2), whatever else the section holds.
type PluginBase struct {
	// Type is the plugin type. dash runs one instance per type, so it is empty or the section
	// name (design J.16).
	Type string `json:"type" doc:"The plugin type. dash runs one instance per type, so this is empty or the section name."`
	// Enabled turns the plugin on or off. Unset leaves it on; an unconfigured plugin reports itself unavailable.
	Enabled *bool `json:"enabled" doc:"false stops the plugin and removes its topics; its pages stay until the web UI follows core.plugins (P2.7), and the Kubernetes connection is still required. Unset leaves it on; a plugin without its settings reports itself unavailable."`
}

// PluginSection is one plugins.<Name> section as the plugin framework reads it.
type PluginSection struct {
	Name string // the section, and the instance name
	Type string // plugins.<Name>.type; Name when it is unset
	// Enabled is nil when unset (automatic), otherwise a copy of the explicit choice.
	Enabled *bool
	// Value is a pointer to a copy of the section struct, such as *ProxmoxConfig; changing it
	// leaves the Tree alone. A secret.Value inside is the same handle, so a rotation still
	// reaches the copy.
	Value any
}

var pluginBaseType = reflect.TypeFor[PluginBase]()

// PluginSections lists the sections of t.Plugins in declaration order.
func (t Tree) PluginSections() []PluginSection {
	pv := reflect.ValueOf(t.Plugins)
	pt := pv.Type()
	out := make([]PluginSection, 0, pt.NumField())
	for i := range pt.NumField() {
		name, _, _ := strings.Cut(pt.Field(i).Tag.Get("json"), ",")
		sec := reflect.New(pt.Field(i).Type)
		sec.Elem().Set(pv.Field(i))
		detach(sec.Elem())
		ps := PluginSection{Name: name, Value: sec.Interface()}
		if base := sec.Elem().FieldByName(pluginBaseType.Name()); base.IsValid() && base.Type() == pluginBaseType {
			b := base.Interface().(PluginBase)
			ps.Type, ps.Enabled = b.Type, b.Enabled
		}
		if ps.Type == "" {
			ps.Type = name
		}
		out = append(out, ps)
	}
	return out
}

// detach gives v its own copy of everything reachable through exported slice, pointer and
// struct fields, so that a copy of a section shares no writable memory with the original.
// Unexported fields (such as a secret.Value's handle) are shared on purpose.
func detach(v reflect.Value) {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(c, v)
		for i := range c.Len() {
			detach(c.Index(i))
		}
		v.Set(c)
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		c := reflect.New(v.Type().Elem())
		c.Elem().Set(v.Elem())
		detach(c.Elem())
		v.Set(c)
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				detach(f)
			}
		}
	}
}
