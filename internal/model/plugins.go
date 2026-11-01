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

// GenericKind is how the SPA renders a GenericView.
type GenericKind string

const (
	GenericTable GenericKind = "table"
	GenericList  GenericKind = "list"
	GenericStat  GenericKind = "stat"
)

// GenericColumn is one column of a table or list view.
type GenericColumn struct {
	Key    string `json:"key"` // field of each row object
	Label  string `json:"label"`
	Format string `json:"format,omitempty"` // text | number | bytes | percent | time | health
}

// GenericView is a view the SPA renders from a topic for a plugin that has no SPA module of its
// own. Path selects the data inside the topic's payload; the kind decides which of the other
// fields apply.
type GenericView struct {
	ID      string          `json:"id"` // unique within the plugin
	Title   string          `json:"title"`
	Kind    GenericKind     `json:"kind"`
	Topic   string          `json:"topic"`            // in a manifest: a declared TopicSpec.Name; in PluginInfo: the full SSE name
	Path    string          `json:"path"`             // RFC 6901 pointer into the topic's data; "" is the whole payload
	Columns []GenericColumn `json:"columns"`          // table and list
	Label   string          `json:"label,omitempty"`  // stat
	Unit    string          `json:"unit,omitempty"`   // stat
	Format  string          `json:"format,omitempty"` // stat: one of the column formats
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
	ID           string        `json:"id"`   // instance name: topic, route and env prefix
	Type         string        `json:"type"` // plugin type (= Manifest.ID); equals ID in v1
	Name         string        `json:"name"`
	Version      string        `json:"version"`
	State        PluginState   `json:"state"`
	Message      string        `json:"message"`
	Topics       []string      `json:"topics"`  // full SSE names: "proxmox.vms", or the un-namespaced legacy name ("vms") until the plugin's G3 leaf
	Actions      []ActionInfo  `json:"actions"` // only the ones mounted (actions.enabled)
	Nav          []NavItem     `json:"nav"`
	Generic      []GenericView `json:"generic"` // views the SPA renders for this plugin; empty when it has a module of its own
	Configurable bool          `json:"configurable"`
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
