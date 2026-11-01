package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/TechDufus/dash/internal/jsonschema"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// plugins lists every registered plugin instance with its state, topics, mounted actions and
// navigation (design A.7). The same rows are published as core.plugins; this answer also gives
// each action's input schema, which the stream leaves out to stay small.
func (s *Server) plugins(w http.ResponseWriter, _ *http.Request) {
	var infos []model.PluginInfo
	if s.d.Plugins != nil {
		infos = s.d.Plugins.Infos()
		s.addInputSchemas(infos, s.d.Plugins.Manifests())
	}
	if infos == nil {
		infos = []model.PluginInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

// addInputSchemas fills ActionInfo.InputSchema of every mounted action from the input struct its
// manifest declares.
func (s *Server) addInputSchemas(infos []model.PluginInfo, manifests []plugin.Manifest) {
	specs := map[string]plugin.ActionSpec{}
	for _, m := range manifests {
		for _, a := range m.Actions {
			specs[string(m.ID)+"/"+a.Name] = a
		}
	}
	for i := range infos {
		for j, a := range infos[i].Actions {
			if spec, ok := specs[infos[i].Type+"/"+a.Name]; ok {
				infos[i].Actions[j].InputSchema = s.inputSchema(spec)
			}
		}
	}
}

// inputSchema is the JSON Schema of an action's input, nil (JSON null) for an action without
// one. A type the reflector cannot describe is logged and left null: a request for the plugin
// list must not fail for one action.
func (s *Server) inputSchema(spec plugin.ActionSpec) (schema json.RawMessage) {
	t := inputType(spec)
	if t == nil {
		return nil
	}
	if v, ok := s.schemas.Load(t); ok {
		return v.(json.RawMessage)
	}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("action input schema", "type", t.String(), "panic", p)
			schema = nil
		}
	}()
	b, err := json.Marshal(jsonschema.New().Type(t))
	if err != nil {
		slog.Error("action input schema", "type", t.String(), "error", err)
		return nil
	}
	s.schemas.Store(t, json.RawMessage(b))
	return b
}

// inputType is the struct type of an action's input: what Input returns, through its pointers.
func inputType(spec plugin.ActionSpec) reflect.Type {
	if spec.Input == nil {
		return nil
	}
	t := reflect.TypeOf(spec.Input())
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// pluginRoute serves the GET routes a running plugin registered, under
// /api/v1/plugins/{id}/. The handler Router returns takes the full request path and answers
// 404 itself for a path the plugin did not register; a stopped, disabled or unknown instance
// has no routes.
func (s *Server) pluginRoute(w http.ResponseWriter, r *http.Request) {
	if id := plugin.ID(r.PathValue("id")); s.d.Plugins != nil && id.Valid() {
		if h, ok := s.d.Plugins.Router(id); ok {
			h.ServeHTTP(w, r)
			return
		}
	}
	writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "not found")
}
