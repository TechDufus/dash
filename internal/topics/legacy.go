package topics

import (
	"context"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Legacy is the plugin identity of an integration that still runs inside Manager until its G3
// leaf moves the code into a plugin of its own. It starts and stops nothing (the Manager already
// runs), reports the state of the kubernetes service the Manager reads for it, and cannot be
// reconfigured in-process: a change of its section needs a restart.
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

// legacyManifests are the shims in display order: flux, the only integration that still runs inside
// the Manager. Nothing here is a hard dependency (Requires): a missing source keeps its page,
// which says "source not configured".
func legacyManifests() []plugin.Manifest {
	nav := func(path, label, title, icon string) plugin.UISpec {
		return plugin.UISpec{Nav: []model.NavItem{{Path: path, Label: label, Title: title, Icon: icon}}}
	}
	return []plugin.Manifest{
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

// Plugins returns one shim per manifest, its Status read from what the Manager sees of the
// kubernetes plugin. Register them with plugin.Supervisor.Add before Manager.Run.
func (m *Manager) Plugins() []plugin.Factory {
	ms := legacyManifests()
	out := make([]plugin.Factory, len(ms))
	for i, man := range ms {
		out[i] = func() plugin.Plugin { return NewLegacy(man, m.legacyStatus) }
	}
	return out
}

// legacyStatus is the state of the flux shim:
//
//   - unavailable (hidden) while the kubernetes plugin's service is absent;
//   - unavailable (hidden) while the hub holds a flux payload that was not built from the service
//     running now (the emptied topic of a withdrawal, the last build of a service that a reload
//     replaced): a row that turned visible then would resend it, and the GitOps page and the
//     overview would flash empty until the Manager's next round;
//   - starting until the informers have synced and each of the five flux resources has listed,
//     and until the first build;
//   - degraded while the discovery of a flux API group fails (its objects are missing);
//   - healthy otherwise.
//
// The message is viewer-visible, so it names nothing but the state and API group names.
func (m *Manager) legacyStatus() plugin.Status {
	c, ok := m.cluster()
	if !ok {
		return plugin.Status{State: plugin.StateUnavailable, Message: "kubernetes is not available", Sources: m.kubeSources()}
	}
	m.mu.RLock()
	builtFor, published := m.fluxFor, m.fluxPublished
	m.mu.RUnlock()
	switch {
	case published && builtFor != c:
		return plugin.Status{State: plugin.StateUnavailable, Message: "reading the flux resources", Sources: m.kubeSources()}
	case !c.Synced():
		return plugin.Status{State: plugin.StateStarting, Message: "informers syncing", Sources: m.kubeSources()}
	case !fluxListed(c):
		return plugin.Status{State: plugin.StateStarting, Message: "listing the flux resources", Sources: m.kubeSources()}
	case builtFor != c:
		return plugin.Status{State: plugin.StateStarting, Message: "reading the flux resources", Sources: m.kubeSources()}
	}
	if groups := fluxFailedGroups(c); len(groups) > 0 {
		return plugin.Status{State: plugin.StateDegraded, Message: "discovery failed for: " + strings.Join(groups, ", "), Sources: m.fluxSources(c)}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: m.kubeSources()}
}
