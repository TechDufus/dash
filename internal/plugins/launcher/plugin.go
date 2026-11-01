// Package launcher is the launcher plugin: the list of infrastructure links (Proxmox, iDRAC, the
// router, …) on the Apps page and the overview. The links come from plugins.launcher.links or
// from the YAML file plugins.launcher.file, and travel as the launcher.links topic, so the SPA
// gets them from the stream like any other data (design F.2, D8).
package launcher

import (
	"context"
	"fmt"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// ID is the plugin's id: plugins.launcher, DASH_PLUGINS_LAUNCHER_*.
const ID plugin.ID = "launcher"

// topicLinks is the plugin's one topic, published as "launcher.links".
const topicLinks = "links"

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Launcher",
		Description: "Links to your infrastructure.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.Launcher{} },
		Topics: []plugin.TopicSpec{{
			Name: topicLinks,
			Doc:  "The launcher's infrastructure links.",
			Type: model.LauncherData{},
		}},
	}
}

// Plugin is the launcher plugin. It is a plugin.Configured and a plugin.Validator.
type Plugin struct{}

var (
	_ plugin.Plugin     = (*Plugin)(nil)
	_ plugin.Configured = (*Plugin)(nil)
	_ plugin.Validator  = (*Plugin)(nil)
	_ plugin.Demo       = (*Plugin)(nil)
)

// New is the plugin.Factory of the real plugin.
func New() plugin.Plugin { return &Plugin{} }

func (*Plugin) Manifest() plugin.Manifest { return manifest() }

// conf reads the section the supervisor hands over: a pointer to the copy of the struct
// Manifest.Config returns. A section by value is accepted too.
func conf(cfg any) (pluginconf.Launcher, bool) {
	switch c := cfg.(type) {
	case *pluginconf.Launcher:
		if c != nil {
			return *c, true
		}
	case pluginconf.Launcher:
		return c, true
	}
	return pluginconf.Launcher{}, false
}

// Configured is true with a file or inline links: without either the plugin is disabled, or, with
// enabled: true, unconfigured.
func (*Plugin) Configured(cfg any) bool {
	c, ok := conf(cfg)
	return ok && (c.File != "" || len(c.Links) > 0)
}

// ConfigHint implements plugin.ConfigHinter.
func (*Plugin) ConfigHint() string { return "plugins.launcher.links or plugins.launcher.file" }

// Validate repeats the static rules the configuration load already enforces (a file or links but
// not both, every link with a name and an http(s) href), so a section that reached the
// supervisor by another way cannot publish a malformed list. It reads no file.
func (*Plugin) Validate(cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("launcher: configuration is %T, want *pluginconf.Launcher", cfg)
	}
	return c.Validate()
}

// Start publishes the links: the inline list, or the file's, read now (a change of the file
// applies at the next start, as before). A file that cannot be read or is malformed fails the
// start, which shows as the plugin's row in state "error" with the cause in the dash log; the rest
// of dash keeps running.
func (*Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("launcher: configuration is %T, want *pluginconf.Launcher", cfg)
	}
	links, err := load(c)
	if err != nil {
		h.Logger().Error("launcher links not loaded", "error", err)
		return err
	}
	h.Publish(topicLinks, model.LauncherData{Links: links}, nil)
	return nil
}

// load is the links of c: never nil, so the payload encodes "links": [] for an empty file. The
// result is a copy, so a later change of the section cannot reach what was published.
func load(c pluginconf.Launcher) ([]model.LauncherLink, error) {
	links := c.Links
	if c.File != "" {
		var err error
		if links, err = pluginconf.LoadLauncher(c.File); err != nil {
			return nil, err
		}
	}
	return append([]model.LauncherLink{}, links...), nil
}

func (*Plugin) Stop(context.Context) error { return nil }

// Status is healthy: the list is read once, so there is nothing to poll.
func (*Plugin) Status() plugin.Status { return plugin.Status{State: plugin.StateHealthy} }
