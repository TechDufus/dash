package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/plugins/alertmanager"
	"github.com/TechDufus/dash/internal/plugins/cnpg"
	"github.com/TechDufus/dash/internal/plugins/flux"
	"github.com/TechDufus/dash/internal/plugins/gatus"
	"github.com/TechDufus/dash/internal/plugins/grafana"
	"github.com/TechDufus/dash/internal/plugins/hardware"
	"github.com/TechDufus/dash/internal/plugins/headlamp"
	"github.com/TechDufus/dash/internal/plugins/kubernetes"
	"github.com/TechDufus/dash/internal/plugins/launcher"
	"github.com/TechDufus/dash/internal/plugins/prometheus"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/plugins/rustfs"
	"github.com/TechDufus/dash/internal/plugins/victorialogs"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
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

// registry is every plugin dash ships, to Add to the supervisor, in display order (design A.1):
// the one table of what exists and the order the plugins are listed in. refs are the k8s:
// references the kubernetes plugin reads (nil: it reads none, which is what the manifests, the
// routes and the settings need).
func registry(refs *secret.K8sRefs) []plugin.Factory {
	return []plugin.Factory{
		prometheus.New, alertmanager.New, gatus.New, victorialogs.New, proxmox.New, hardware.New,
		kubernetes.Factory(refs), flux.New, cnpg.New, rustfs.New, grafana.New, headlamp.New, launcher.New,
	}
}

// builtinManifests are the manifests of every plugin dash ships, in display order, without
// starting any of them.
func builtinManifests() []plugin.Manifest {
	var out []plugin.Manifest
	for _, f := range registry(nil) {
		out = append(out, f().Manifest())
	}
	return out
}

// builtinPlugin is a new instance of the shipped plugin id, not started.
func builtinPlugin(id plugin.ID) (plugin.Plugin, bool) {
	for _, f := range registry(nil) {
		if p := f(); p.Manifest().ID == id {
			return p, true
		}
	}
	return nil, false
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
	refs *secret.K8sRefs, transport func(plugin.ID, http.RoundTripper) http.RoundTripper, demo bool) *framework {
	f := &framework{hub: hub.New()}
	// f.sup is set before the reloader's loop can start, so the hook never sees nil. The auth
	// section is live: its admin lists are applied in-process (reloadAuth).
	opts.Live = func(section string) bool { return section == "auth" || f.sup.Live(section) }
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
	// A Secret read for the first time, a reason that changed, or a reference a reload added: the
	// consumer of the credential starts (or its row says why not) through one Reload. A later
	// rotation of a value does not: the holders are read in place.
	if refs != nil {
		refs.OnChange(f.sup.Reload)
	}
	return f
}

// start adds the plugins, runs the supervisor, and only then runs source, the code that
// publishes for them (the overview publisher, in production and in demo mode): its first
// publishes find every Host running, so none is dropped.
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
