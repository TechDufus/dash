package api

import "github.com/TechDufus/dash/internal/api/settingstest"

// The fake the settings routes are tested with is the backend they are written against.
var _ SettingsBackend = (*settingstest.Fake)(nil)
