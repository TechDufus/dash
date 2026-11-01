package topics

import (
	"context"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/upstream"
)

// Legacy is the plugin identity of an integration that still runs inside Manager until its G3
// leaf moves the code into a plugin of its own. It starts and stops nothing (the Manager already
// runs), reports the state of the sources the Manager polls for it, and cannot be reconfigured
// in-process: a change of its section needs a restart.
type Legacy struct {
	M      plugin.Manifest
	status func() plugin.Status
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

// owner is the plugin that publishes a topic, and the topic's name inside the plugin.
type owner struct {
	plugin plugin.ID
	local  string
}

// owners says which plugin each topic belongs to, keyed by the un-namespaced name the topic
// keeps until its plugin migrates (TopicSpec.Legacy). It is derived from the manifests, so the
// two cannot drift.
var owners = func() map[model.Topic]owner {
	out := map[model.Topic]owner{}
	for _, m := range legacyManifests() {
		for _, t := range m.Topics {
			out[model.Topic(t.Legacy)] = owner{m.ID, t.Name}
		}
	}
	return out
}()

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
	"vms":     {"proxmox"},
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
		{ID: "proxmox", Name: "Proxmox VE", Description: "Virtual machines, containers and the hypervisor.", Version: "1.0.0",
			Uses:   []plugin.ID{"kubernetes", "prometheus"},
			Config: func() any { return &config.ProxmoxConfig{} },
			Topics: []plugin.TopicSpec{{Name: "vms", Legacy: "vms", Type: model.VMsData{}, Doc: "Virtual machines and containers."}},
			UI:     nav("/vms", "VMs", "Virtual machines", "Server")},
		{ID: "hardware", Name: "Hardware", Description: "The physical host behind the cluster.", Version: "1.0.0",
			Uses:   []plugin.ID{"prometheus", "proxmox"},
			Config: func() any { return &config.HardwareConfig{} },
			Topics: []plugin.TopicSpec{{Name: "hosts", Legacy: "host", Type: model.HostData{}, Doc: "The host's sensors, power, disks and pools."}},
			UI:     nav("/hardware", "Hardware", "Hardware", "Cpu")},
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
			return NewLegacy(man, func() plugin.Status { return m.legacyStatus(man.ID) })
		}
	}
	return out
}

// pollKeys are the source keys (m.srcs) that decide a poller-backed shim's state.
var pollKeys = map[plugin.ID][]string{
	"proxmox":  {"vms:proxmox", "host:proxmox"},
	"hardware": {"host:victoriametrics"},
}

// setting names what to configure for a shim whose source is off, for its status message.
var setting = map[plugin.ID]string{
	"proxmox":  "plugins.proxmox.url, tokenID and tokenSecret",
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
