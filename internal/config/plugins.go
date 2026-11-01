package config

import (
	"reflect"
	"strings"

	"github.com/TechDufus/dash/internal/pluginconf"
)

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

var pluginBaseType = reflect.TypeFor[pluginconf.PluginBase]()

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
			b := base.Interface().(pluginconf.PluginBase)
			ps.Type, ps.Enabled = b.Type, b.Enabled
		}
		if ps.Type == "" {
			ps.Type = name
		}
		out = append(out, ps)
	}
	return out
}

// detach gives v its own copy of everything reachable through exported slice, map, pointer and
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
	case reflect.Map:
		if v.IsNil() {
			return
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(it.Value())
			detach(e)
			c.SetMapIndex(it.Key(), e)
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
