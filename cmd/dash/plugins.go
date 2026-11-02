package main

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/hardware"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
	"github.com/TechDufus/dash/internal/plugins/launcher"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/plugins/victorialogs"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
	"github.com/TechDufus/dash/internal/topics"
)

// actionsConfigurer is a section value that sets the action policy of its plugin
// (plugins.<id>.actions.*), such as *pluginconf.Alertmanager. A section without it leaves the
// instance's actions disabled.
type actionsConfigurer interface{ ActionsConfig() pluginconf.Actions }

// readDenyConfigurer is a section value that sets the namespaces its plugin's read routes refuse
// (plugins.victorialogs.denyNamespaces).
type readDenyConfigurer interface{ ReadDenyConfig() []string }

// treeSettings is the plugin framework's view of the configuration: the current plugins.*
// sections of the last valid resolution.
type treeSettings struct{ tree func() config.Tree }

// Instances implements plugin.Settings.
func (s treeSettings) Instances() []plugin.InstanceConfig {
	var out []plugin.InstanceConfig
	for _, sec := range s.tree().PluginSections() {
		out = append(out, instanceConfig(sec))
	}
	return out
}

// instanceConfig is what the supervisor needs of one section. The action and route policies come
// from the section value itself, so the supervisor, which compares the whole InstanceConfig,
// restarts an instance when either changes.
func instanceConfig(sec config.PluginSection) plugin.InstanceConfig {
	ic := plugin.InstanceConfig{
		Name: plugin.ID(sec.Name), Type: plugin.ID(sec.Type), Enabled: sec.Enabled, Config: sec.Value,
	}
	if a, ok := sec.Value.(actionsConfigurer); ok {
		act := a.ActionsConfig()
		ic.ActionsEnabled, ic.DenyNamespaces = act.Enabled, act.DenyNamespaces
	}
	if r, ok := sec.Value.(readDenyConfigurer); ok {
		ic.ReadDenyNamespaces = r.ReadDenyConfig()
	}
	return ic
}

// displayOrder is the order plugins are listed in (design A.1). It is kept here, and not read
// off the legacy manifests, because a plugin that moves out of the Manager deletes its shim.
var displayOrder = []plugin.ID{
	"prometheus", "alertmanager", "gatus", "victorialogs", "proxmox", "hardware", "kubernetes", "flux",
	"grafana", "headlamp", "launcher",
}

// realPlugins are the plugins that run as plugins of their own, by id. An integration that has
// moved out of the Manager adds its factory here and its shim leaves topics.legacyManifests;
// until then it is absent and the shim stands in.
var realPlugins = map[plugin.ID]plugin.Factory{
	"prometheus":   prometheus.New,
	"alertmanager": alertmanager.New,
	"gatus":        gatus.New,
	"victorialogs": victorialogs.New,
	"proxmox":      proxmox.New,
	"hardware":     hardware.New,
	"grafana":      grafana.New,
	"headlamp":     headlamp.New,
	"launcher":     launcher.New,
}

// hasRealPlugin reports whether id runs as a plugin of its own, so demo mode registers its twin
// (the plugin's Demo) and not a stand-in shim.
func hasRealPlugin(id plugin.ID) bool {
	_, ok := realPlugins[id]
	return ok
}

// builtinManifests are the manifests of every plugin dash ships, in display order, without
// starting any of them.
func builtinManifests() []plugin.Manifest { return manifestsOf(realPlugins, topics.LegacyManifests()) }

// manifestsOf is builtinManifests for a given set of real plugins and legacy manifests.
func manifestsOf(impl map[plugin.ID]plugin.Factory, legacy []plugin.Manifest) []plugin.Manifest {
	byID := make(map[plugin.ID]plugin.Manifest, len(legacy))
	ids := make([]plugin.ID, len(legacy))
	for i, m := range legacy {
		byID[m.ID], ids[i] = m, m.ID
	}
	var out []plugin.Manifest
	for _, id := range inDisplayOrder(impl, ids) {
		if f, ok := impl[id]; ok {
			out = append(out, f().Manifest())
		} else {
			out = append(out, byID[id])
		}
	}
	return out
}

// registry is the plugins to Add to the supervisor, in display order: per id the real factory
// when there is one, else its shim out of shims (topics.Manager.Plugins).
func registry(shims []plugin.Factory) []plugin.Factory { return registryOf(realPlugins, shims) }

// registryOf is registry for a given set of real plugins.
func registryOf(impl map[plugin.ID]plugin.Factory, shims []plugin.Factory) []plugin.Factory {
	byID := make(map[plugin.ID]plugin.Factory, len(shims))
	ids := make([]plugin.ID, len(shims))
	for i, f := range shims {
		id := f().Manifest().ID
		byID[id], ids[i] = f, id
	}
	var out []plugin.Factory
	for _, id := range inDisplayOrder(impl, ids) {
		if f, ok := impl[id]; ok {
			out = append(out, f)
		} else {
			out = append(out, byID[id])
		}
	}
	return out
}

// inDisplayOrder lists each id of real and of legacy once: those in displayOrder first, in that
// order, then any other as legacy gives them. A real plugin outside displayOrder is a bug a
// test catches, so it is not placed here.
func inDisplayOrder(impl map[plugin.ID]plugin.Factory, legacy []plugin.ID) []plugin.ID {
	var out []plugin.ID
	for _, id := range displayOrder {
		if _, ok := impl[id]; ok || slices.Contains(legacy, id) {
			out = append(out, id)
		}
	}
	for _, id := range legacy {
		if !slices.Contains(displayOrder, id) {
			out = append(out, id)
		}
	}
	return out
}

// framework is the plugin side of run: the hub every topic goes to, the supervisor that runs
// the plugins, and the reloader that tells it when a plugins.* section changed. run and the
// tests build it the same way (newFramework) and start it the same way (start).
type framework struct {
	hub      *hub.Hub
	sup      *plugin.Supervisor
	reloader *config.Reloader
}

// newFramework joins the three. A rotated secret file reaches the running credentials through
// the reloader, and a changed plugins.* section reaches the supervisor, which applies it
// in-process where the plugin allows it (the reloader asks Supervisor.Live, so only what needs
// a restart is reported as such). Readiness depends on the supervisor's plugins, not on the
// reloader. The caller runs the reloader's loop and shuts the supervisor down.
//
// reg scrubs the log lines plugins publish (nil scrubs by shape only), transport wraps the
// client of every plugin (nil leaves it alone) and demo runs the plugins' demo twins.
func newFramework(res *config.Resolved, store *settings.Store, opts config.ReloadOptions, reg *secret.Registry,
	transport func(plugin.ID, http.RoundTripper) http.RoundTripper, demo bool) *framework {
	f := &framework{hub: hub.New()}
	// f.sup is set before the reloader's loop can start, so the hook never sees nil.
	opts.Live = func(section string) bool { return f.sup.Live(section) }
	f.reloader = config.NewReloader(res, store, opts)
	ready := make([]plugin.ID, len(res.Config.ReadyRequires))
	for i, id := range res.Config.ReadyRequires {
		ready[i] = plugin.ID(id)
	}
	f.sup = plugin.NewSupervisor(plugin.Options{
		Publisher: f.hub, Settings: treeSettings{f.reloader.Tree}, ReadyRequires: ready, ReadyTimeout: res.Config.ReadyTimeout,
		Transport: transport, ScrubLog: reg.ScrubLogLine, Demo: demo,
	})
	f.hub.SetOrder(f.sup.TopicOrder)
	f.reloader.Subscribe(func(c config.Change) {
		if strings.HasPrefix(c.Section, "plugins.") {
			f.sup.Reload()
		}
	})
	return f
}

// start adds the plugins, runs the supervisor, and only then runs source, the code that
// publishes for them (the topic Manager, or demo mode's source): its first publishes find every
// Host running, so none is dropped.
func (f *framework) start(ctx context.Context, plugins []plugin.Factory, source func()) error {
	if err := f.sup.Add(plugins...); err != nil {
		return err
	}
	if err := f.sup.Run(ctx); err != nil {
		return err
	}
	source()
	return nil
}
