package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// getSettings answers GET /api/v1/settings: every leaf outside plugins.* (General) and one row per
// plugin, with the effective values and their provenance from the running configuration, what
// the store holds, and the restart list.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	b, ok := s.settingsBackend(w)
	if !ok {
		return
	}
	res, stored, rev, restart := viewState(b)
	store := b.Store()
	view := model.SettingsView{
		Revision: rev, Store: store, RestartRequired: nonNil(restart),
		General: config.SettingFields(res, stored, store.Readonly, func(p string) bool { return !strings.HasPrefix(p, "plugins.") }),
		Plugins: []model.SettingsPlugin{},
	}
	infos := s.pluginInfos()
	for _, sec := range res.Tree.PluginSections() {
		row := model.SettingsPlugin{ID: sec.Name, Name: sec.Name, Configurable: true, Testable: b.Testable(sec.Name)}
		in, live := infos[sec.Name]
		if live {
			row.Name, row.State, row.Message = in.Name, in.State, in.Message
		}
		row.Enabled, _ = config.SettingField(res, stored, store.Readonly, "plugins."+sec.Name+".enabled")
		runningEnabled(&row.Enabled, in, live)
		view.Plugins = append(view.Plugins, row)
	}
	writeJSON(w, http.StatusOK, view)
}

// pluginInfos is the live row of every plugin instance by id; empty without plugins.
func (s *Server) pluginInfos() map[string]model.PluginInfo {
	infos := map[string]model.PluginInfo{}
	if s.d.Plugins != nil {
		for _, in := range s.d.Plugins.Infos() {
			infos[in.ID] = in
		}
	}
	return infos
}

// runningEnabled gives an enabled field that no layer sets the value the running instance has:
// true unless its live row is disabled. Without a live row the value stays absent. The field
// stays unset, from the default: the value is what runs, not a setting.
func runningEnabled(f *model.SettingField, in model.PluginInfo, live bool) {
	if live && !f.Set && f.Source == model.SettingSourceDefault && f.Value == nil {
		f.Value = in.State != model.PluginDisabled
	}
}

// getSettingsPlugin answers GET /api/v1/settings/plugins/{id}: the section's own schema and every
// leaf of plugins.<id>.
func (s *Server) getSettingsPlugin(w http.ResponseWriter, r *http.Request) {
	b, id, ok := s.settingsPlugin(w, r)
	if !ok {
		return
	}
	doc, _, err := config.PluginSchemaDocument(id)
	var schema []byte
	if err == nil {
		schema, err = json.Marshal(doc)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the plugin's schema could not be built")
		return
	}
	res, stored, rev, _ := viewState(b)
	store := b.Store()
	prefix := "plugins." + id + "."
	fields := config.SettingFields(res, stored, store.Readonly, func(p string) bool { return strings.HasPrefix(p, prefix) })
	if i := slices.IndexFunc(fields, func(f model.SettingField) bool { return f.Path == prefix+"enabled" }); i >= 0 {
		in, live := s.pluginInfos()[id]
		runningEnabled(&fields[i], in, live)
	}
	writeJSON(w, http.StatusOK, model.SettingsPluginView{
		ID: id, Schema: schema, Revision: rev, Testable: b.Testable(id), Fields: fields,
	})
}

// viewState reads what a view shows under the reloader lock: the running configuration, the
// store's values, its revision and the restart list as one state. Read apart, a write between
// them could pair a revision with values it did not produce, and an If-Match from the view
// would pass over a change the admin never saw.
func viewState(b SettingsBackend) (res *config.Resolved, stored map[string]any, rev uint64, restart []string) {
	_ = b.WithLock(func() error {
		res, stored, rev, restart = b.Resolved(), b.Stored(), b.Revision(), b.RestartRequired()
		return nil
	})
	return res, stored, rev, restart
}

// getSettingsSchema answers GET /api/v1/settings/schema: the schema of the whole configuration,
// the document `dash config schema` prints.
func (s *Server) getSettingsSchema(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.settingsBackend(w); !ok {
		return
	}
	doc, err := config.SchemaDocument()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "the schema could not be built")
		return
	}
	writeJSON(w, http.StatusOK, doc)
}
