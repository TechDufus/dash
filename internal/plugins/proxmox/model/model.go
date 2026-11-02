// Package model holds the payload types only the proxmox plugin publishes. It is an import leaf:
// it may import nothing from dash (TestPluginModelPackagesAreImportLeaves), because
// internal/model.TopicTypes lists every topic's payload type and so imports this package.
// A payload that needs a core type (Link, Health, SourceStatus) stays in internal/model.
package model

// NodeData is the proxmox.node topic: the facts of the Proxmox node the plugin reads.
type NodeData struct {
	Ref           string `json:"ref"` // proxmox:Node/<node>
	Node          string `json:"node"`
	Version       string `json:"version"` // pve-manager version, "9.2.2"
	Kernel        string `json:"kernel"`
	CPUModel      string `json:"cpuModel"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
}

// PVEStorage is one Proxmox storage with its usage.
type PVEStorage struct {
	Ref       string `json:"ref"` // proxmox:Storage/<name>
	Name      string `json:"name"`
	UsedBytes int64  `json:"usedBytes"`
	SizeBytes int64  `json:"sizeBytes"`
}

// PVEStorageData is the proxmox.storage topic. It is not called StorageData because that name
// is taken in internal/model, and the OpenAPI generator qualifies a name two packages share.
type PVEStorageData struct {
	Storages []PVEStorage `json:"storages"` // never null
}
