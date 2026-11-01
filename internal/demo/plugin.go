package demo

import (
	"context"

	"github.com/TechDufus/dash/internal/plugin"
)

// demoPlugin is the plugin identity of one demo integration. Like the production shim it starts
// nothing (Source publishes for it), but unlike the shim it is not RestartOnly: switching
// plugins.<id>.enabled in the config file reaches Supervisor.Reload, which stops the instance
// and removes its topics, or starts it again. That is what lets the browser smoke and the
// frontend acceptance watch a plugin appear and disappear with no restart.
type demoPlugin struct{ m plugin.Manifest }

func (p demoPlugin) Manifest() plugin.Manifest                   { return p.m }
func (demoPlugin) Start(context.Context, plugin.Host, any) error { return nil }
func (demoPlugin) Stop(context.Context) error                    { return nil }
func (demoPlugin) Status() plugin.Status                         { return plugin.Status{State: plugin.StateHealthy} }
