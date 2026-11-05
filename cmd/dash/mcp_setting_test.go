package main

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/TechDufus/dash/internal/model"
)

// mcp.enabled is saved from the settings page and applies without a restart: the reloader's tree,
// which the API reads on every request, holds it as soon as the PUT answers.
func TestMCPEnabledIsSetLiveFromTheSettingsPage(t *testing.T) {
	u := startUIServer(t, nil, nil, nil)
	if u.fw.reloader.Tree().MCP.Enabled {
		t.Fatal("mcp.enabled is on by default")
	}
	var view model.SettingsView
	ok(t, "GET settings", u.do(t, "alice", http.MethodGet, "/api/v1/settings", nil), &view)
	ifMatch := []string{"If-Match", `"` + strconv.FormatUint(view.Revision, 10) + `"`}
	var saved model.SettingsPatchResult
	ok(t, "PUT general", u.do(t, "alice", http.MethodPut, "/api/v1/settings/general",
		map[string]any{"mcp": map[string]any{"enabled": true}}, ifMatch...), &saved)
	if slices.Contains(saved.RestartRequired, "mcp.enabled") {
		t.Errorf("mcp.enabled needs a restart: %+v", saved)
	}
	if !u.fw.reloader.Tree().MCP.Enabled {
		t.Error("the running configuration does not have mcp.enabled")
	}
}
