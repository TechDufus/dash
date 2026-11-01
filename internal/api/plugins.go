package api

import (
	"net/http"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// plugins lists every registered plugin instance with its state, topics, mounted actions and
// navigation (design A.7). The same rows are published as core.plugins.
func (s *Server) plugins(w http.ResponseWriter, _ *http.Request) {
	var infos []model.PluginInfo
	if s.d.Plugins != nil {
		infos = s.d.Plugins.Infos()
	}
	if infos == nil {
		infos = []model.PluginInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
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
