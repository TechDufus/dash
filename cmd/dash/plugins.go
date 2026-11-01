package main

import (
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/plugin"
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
