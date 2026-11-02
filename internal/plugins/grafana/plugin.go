// Package grafana is the grafana plugin: links into Grafana for the alert list and the host.
// It reads nothing from Grafana; it is a plugin.LinkProvider, so dash's own pages can offer
// "Open in Grafana" without importing a tool-specific package (design A.9, F.2).
package grafana

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// ID is the plugin's id: plugins.grafana, DASH_PLUGINS_GRAFANA_*.
const ID plugin.ID = "grafana"

// The dash-wide kinds Grafana answers (plugin.LinkRequest.Kind).
const (
	kindAlerts = "Alerts" // the alert list
	kindHost   = "Host"   // the hypervisor host: Grafana's home page
)

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Grafana",
		Description: "Links to dashboards.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.Grafana{} },
	}
}

// Plugin is the grafana plugin. It is a plugin.Configured and a plugin.LinkProvider.
type Plugin struct {
	// base is the configured URL without a trailing slash, nil until Start and again after Stop:
	// Links is called while the supervisor resolves links, so it only reads.
	base atomic.Pointer[string]
}

var (
	_ plugin.Plugin       = (*Plugin)(nil)
	_ plugin.Configured   = (*Plugin)(nil)
	_ plugin.LinkProvider = (*Plugin)(nil)
	_ plugin.Demo         = (*Plugin)(nil)
)

// New is the plugin.Factory of the real plugin.
func New() plugin.Plugin { return &Plugin{} }

func (*Plugin) Manifest() plugin.Manifest { return manifest() }

// conf reads the section the supervisor hands over: a pointer to the copy of the struct
// Manifest.Config returns. A section by value is accepted too.
func conf(cfg any) (pluginconf.Grafana, bool) {
	switch c := cfg.(type) {
	case *pluginconf.Grafana:
		if c != nil {
			return *c, true
		}
	case pluginconf.Grafana:
		return c, true
	}
	return pluginconf.Grafana{}, false
}

// Configured is true with a URL: without one the plugin is disabled, or, with enabled: true,
// unconfigured, and contributes no link.
func (*Plugin) Configured(cfg any) bool {
	c, ok := conf(cfg)
	return ok && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (*Plugin) ConfigHint() string { return "plugins.grafana.url" }

func (p *Plugin) Start(_ context.Context, _ plugin.Host, cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("grafana: configuration is %T, want *pluginconf.Grafana", cfg)
	}
	p.set(c.URL)
	return nil
}

func (p *Plugin) set(url string) {
	url = strings.TrimRight(url, "/") // the tree already trims; a direct caller may not
	p.base.Store(&url)
}

func (p *Plugin) Stop(context.Context) error {
	p.base.Store(nil)
	return nil
}

// Status is healthy once Start has run: the plugin has no upstream to fail.
func (p *Plugin) Status() plugin.Status {
	if p.base.Load() == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy}
}

// Links answers "Alerts" with Grafana's alert list and "Host" with its home page; any other kind
// gets nothing. A plugin that is not running contributes nothing.
func (p *Plugin) Links(req plugin.LinkRequest) []model.Link {
	base := p.base.Load()
	if base == nil || *base == "" {
		return []model.Link{}
	}
	switch req.Kind {
	case kindAlerts:
		return []model.Link{{Label: "Grafana alerts", Href: *base + "/alerting/list", Kind: model.LinkKindGrafana}}
	case kindHost:
		return []model.Link{{Label: "Grafana", Href: *base + "/", Kind: model.LinkKindGrafana}}
	}
	return []model.Link{}
}
