package model

import "encoding/json"

// PluginState is where a plugin instance is in its life (design A.5, A.6).
type PluginState string

const (
	PluginDisabled     PluginState = "disabled"
	PluginUnavailable  PluginState = "unavailable"
	PluginUnconfigured PluginState = "unconfigured"
	PluginBlocked      PluginState = "blocked"
	PluginStarting     PluginState = "starting"
	PluginHealthy      PluginState = "healthy"
	PluginDegraded     PluginState = "degraded"
	PluginError        PluginState = "error"
)

// Confirm is how an action is confirmed before it runs.
type Confirm string

const (
	ConfirmNone     Confirm = "none"
	ConfirmDialog   Confirm = "dialog"   // UI-only
	ConfirmTypeName Confirm = "typeName" // the plugin checks the typed name in Act
)

// NavItem is one entry a plugin adds to the navigation.
type NavItem struct {
	Path  string `json:"path"`
	Label string `json:"label"`
	Title string `json:"title"`
	Icon  string `json:"icon"` // a lucide icon name; the SPA module maps it
}

// ActionInfo describes one action a plugin mounts.
type ActionInfo struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Destructive bool            `json:"destructive"`
	Idempotent  bool            `json:"idempotent"`
	Confirm     Confirm         `json:"confirm"`
	InputSchema json.RawMessage `json:"inputSchema" tstype:"unknown"` // filled by P2.10; null before
}

// PluginInfo is one row of GET /api/v1/plugins and of core.plugins. Message is viewer-visible:
// plugin authors write no URL, host or credential into Status.Message.
type PluginInfo struct {
	ID           string       `json:"id"`   // instance name: topic, route and env prefix
	Type         string       `json:"type"` // plugin type (= Manifest.ID); equals ID in v1
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	State        PluginState  `json:"state"`
	Message      string       `json:"message"`
	Topics       []string     `json:"topics"`  // full SSE names: "proxmox.vms", or the un-namespaced legacy name ("vms") until the plugin's G3 leaf
	Actions      []ActionInfo `json:"actions"` // only the ones mounted (actions.enabled)
	Nav          []NavItem    `json:"nav"`
	Configurable bool         `json:"configurable"`
}

// PluginsData is the payload of core.plugins.
type PluginsData struct {
	Plugins []PluginInfo `json:"plugins"`
}

// KPI is one headline number a plugin contributes to the overview.
type KPI struct {
	Key    string  `json:"key"`
	Plugin string  `json:"plugin"`
	Label  string  `json:"label"`
	Unit   string  `json:"unit"`
	Value  float64 `json:"value"`
	Health Health  `json:"health"`
}
