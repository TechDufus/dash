package topics

import (
	"context"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	hwmodel "github.com/TechDufus/dash/internal/plugins/hardware/model"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// Legacy is the plugin identity of an integration that still runs inside Manager until its G3
// leaf moves the code into a plugin of its own. It starts and stops nothing (the Manager already
// runs), reports the state of the sources the Manager polls for it, and cannot be reconfigured
// in-process: a change of its section needs a restart.
type Legacy struct {
	M      plugin.Manifest
	status func() plugin.Status
	// Provide is what the shim exports to the plugins that use it (plugin.Provider): nil for a
	// shim that exports nothing. It is read on every Service call, so it can answer from
	// state that is built later.
	Provide func() any
}

// NewLegacy returns the shim of manifest m whose Status is status.
func NewLegacy(m plugin.Manifest, status func() plugin.Status) *Legacy {
	return &Legacy{M: m, status: status}
}

func (l *Legacy) Manifest() plugin.Manifest                     { return l.M }
func (l *Legacy) Start(context.Context, plugin.Host, any) error { return nil }
func (l *Legacy) Stop(context.Context) error                    { return nil }
func (l *Legacy) Status() plugin.Status                         { return l.status() }
func (l *Legacy) RestartOnly()                                  {}

// Service is what the shim exports: an untyped nil unless Provide is set, which the supervisor
// reads as "no service".
func (l *Legacy) Service() any {
	if l.Provide == nil {
		return nil
	}
	return l.Provide()
}

// owner is the plugin that publishes a topic, and the topic's name inside the plugin.
type owner struct {
	plugin plugin.ID
	local  string
}

// owners says which plugin each topic belongs to, keyed by the topic's wire name: the
// un-namespaced name it keeps until its plugin migrates (TopicSpec.Legacy), else
// "<plugin>.<name>", which is what the supervisor publishes it under. It is derived from the
// manifests, so the two cannot drift.
var owners = ownersOf(legacyManifests())

func ownersOf(ms []plugin.Manifest) map[model.Topic]owner {
	out := map[model.Topic]owner{}
	for _, m := range ms {
		for _, t := range m.Topics {
			out[plugin.TopicName(m.ID, t)] = owner{m.ID, t.Name}
		}
	}
	return out
}

// Owner is the plugin that publishes topic and the topic's name inside that plugin, for code
// that publishes on a plugin's behalf (demo mode). The overview and core.plugins have none.
func Owner(topic model.Topic) (id plugin.ID, local string, ok bool) {
	o, ok := owners[topic]
	return o.plugin, o.local, ok
}

// pollUsers lists the plugins that read each poller's source. A poller runs while at least one of
// them does, so a disabled integration costs no upstream request.
var pollUsers = map[string][]plugin.ID{
	"host":    {"hardware"},
	"vms":     {"hardware"},
	"usage":   {"kubernetes"},
	"storage": {"kubernetes"},
}

// legacyManifests are the shims in display order: design A.1 without cnpg and rustfs, which have
// no shim until G3. Nothing here is a hard dependency (Requires): a missing source keeps its
// page, which says "source not configured".
func legacyManifests() []plugin.Manifest {
	nav := func(path, label, title, icon string) plugin.UISpec {
		return plugin.UISpec{Nav: []model.NavItem{{Path: path, Label: label, Title: title, Icon: icon}}}
	}
	return []plugin.Manifest{
		{ID: "hardware", Name: "Hardware", Description: "The physical host behind the cluster.", Version: "1.0.0",
			Uses:   []plugin.ID{"prometheus", "proxmox"},
			Config: func() any { return &pluginconf.Hardware{} },
			Topics: []plugin.TopicSpec{
				{Name: "hosts", Legacy: "host", Type: model.HostData{}, Doc: "The host's sensors, power, disks and pools."},
				// No Legacy name: its wire name is hardware.machines (see owners). The Manager
				// publishes it until the hardware plugin does.
				{Name: "machines", Type: hwmodel.MachinesData{}, Doc: "The machines dash watches that are neither VMs nor cluster nodes."},
			},
			UI: nav("/hardware", "Hardware", "Hardware", "Cpu")},
		{ID: "kubernetes", Name: "Kubernetes", Description: "How dash reaches and reads the cluster.", Version: "1.0.0",
			Uses:   []plugin.ID{"prometheus", "gatus"},
			Config: func() any { return &config.KubernetesConfig{} },
			Topics: []plugin.TopicSpec{
				{Name: "cluster", Legacy: "cluster", Type: model.ClusterData{}, Doc: "Nodes, namespaces, pods, workloads and certificates."},
				{Name: "storage", Legacy: "storage", Type: model.StorageData{}, Doc: "Volumes, buckets and backups."},
				{Name: "apps", Legacy: "apps", Type: model.AppsData{}, Doc: "The apps behind HTTP routes."},
			},
			UI: nav("/cluster", "Cluster", "Kubernetes cluster", "Boxes")},
		{ID: "flux", Name: "Flux", Description: "Flux sources, kustomizations and Helm releases.", Version: "1.0.0",
			Uses:   []plugin.ID{"kubernetes"},
			Config: func() any { return &config.FluxConfig{} },
			Topics: []plugin.TopicSpec{{Name: "objects", Legacy: "flux", Type: model.FluxData{}, Doc: "Flux objects."}},
			UI:     nav("/gitops", "GitOps", "GitOps (Flux)", "GitBranch")},
	}
}

// LegacyManifests returns the manifests of the shims, in display order. Their Config functions
// return the section types of config.PluginsConfig.
func LegacyManifests() []plugin.Manifest { return legacyManifests() }

// Plugins returns one shim per manifest, its Status read from what the Manager polls. Register
// them with plugin.Supervisor.Add before Manager.Run.
func (m *Manager) Plugins() []plugin.Factory {
	ms := legacyManifests()
	out := make([]plugin.Factory, len(ms))
	for i, man := range ms {
		out[i] = func() plugin.Plugin {
			l := NewLegacy(man, func() plugin.Status { return m.legacyStatus(man.ID) })
			if man.ID == "kubernetes" {
				// Until the kubernetes plugin exists the Manager is its node index (D14): the
				// proxmox plugin reads it through Host.Service and needs no change then.
				l.Provide = func() any { return svc.NodeIndex(m) }
			}
			return l
		}
	}
	return out
}

// pollKeys are the source keys (m.srcs) that decide a poller-backed shim's state.
var pollKeys = map[plugin.ID][]string{
	"hardware": {"host:victoriametrics"},
}

// setting names what to configure for a shim whose source is off, for its status message.
var setting = map[plugin.ID]string{
	"hardware": "plugins.prometheus.url",
}

// legacyStatus is the state of shim id (design: how the Manager reports for its plugins). The
// message is viewer-visible, so it names a setting or an error category and nothing else.
func (m *Manager) legacyStatus(id plugin.ID) plugin.Status {
	switch id {
	case "kubernetes", "flux":
		if m.d.Cluster.HasSynced() {
			return plugin.Status{State: plugin.StateHealthy, Sources: m.sources("kubernetes")}
		}
		return plugin.Status{State: plugin.StateStarting, Message: "informers syncing", Sources: m.sources("kubernetes")}
	}
	all := pollKeys[id]
	// A key whose poller is skipped (nothing that reads it runs) would stay "not polled yet".
	keys := m.polledKeys(all)
	if len(keys) == 0 {
		return plugin.Status{State: plugin.StateUnavailable, Message: "no enabled plugin reads this source"}
	}

	m.mu.RLock()
	var ok, notConfigured, pending, failing int
	failure := ""
	for _, k := range keys {
		st := m.srcs[k]
		switch {
		case st.at.IsZero():
			pending++
		case st.ok:
			ok++
		case st.err == upstream.CategoryNotConfigured:
			notConfigured++
		default:
			failing++
			if failure == "" {
				failure = st.err
			}
		}
	}
	m.mu.RUnlock()

	status := func(s plugin.State, msg string) plugin.Status {
		return plugin.Status{State: s, Message: msg, Sources: m.sources(keys...)}
	}
	switch {
	case notConfigured == len(keys):
		return status(plugin.StateUnavailable, "not configured: set "+setting[id])
	case pending > 0:
		return status(plugin.StateStarting, "waiting for the first poll")
	case failing == 0:
		return status(plugin.StateHealthy, "")
	case ok > 0:
		return status(plugin.StateDegraded, failure)
	default:
		return status(plugin.StateError, failure)
	}
}
