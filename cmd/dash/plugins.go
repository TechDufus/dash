package main

import (
	"context"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/settings"
	"github.com/TechDufus/dash/internal/topics"
)

// treeSettings is the plugin framework's view of the configuration: the current plugins.*
// sections of the last valid resolution.
type treeSettings struct{ tree func() config.Tree }

// Instances implements plugin.Settings.
func (s treeSettings) Instances() []plugin.InstanceConfig {
	var out []plugin.InstanceConfig
	for _, sec := range s.tree().PluginSections() {
		out = append(out, plugin.InstanceConfig{
			Name: plugin.ID(sec.Name), Type: plugin.ID(sec.Type), Enabled: sec.Enabled, Config: sec.Value,
		})
	}
	return out
}

// builtinManifests are the manifests of every plugin dash ships, in display order, without
// starting any of them: the legacy shims until each integration moves into a plugin (G3).
func builtinManifests() []plugin.Manifest { return topics.LegacyManifests() }

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
func newFramework(res *config.Resolved, store *settings.Store, opts config.ReloadOptions) *framework {
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
