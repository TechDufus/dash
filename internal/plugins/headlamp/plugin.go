// Package headlamp is the headlamp plugin: deep links into the Kubernetes UI. It reads nothing
// from Headlamp; it is a plugin.LinkProvider that turns a Kubernetes object (kind, namespace,
// name) into "Open in Headlamp" (design A.9, F.2).
package headlamp

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// ID is the plugin's id: plugins.headlamp, DASH_PLUGINS_HEADLAMP_*.
const ID plugin.ID = "headlamp"

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Headlamp",
		Description: "Deep links into the Kubernetes UI.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.Headlamp{} },
	}
}

// kindPaths maps a Kubernetes kind to its Headlamp path prefix; the object follows as
// "<ns>/<name>" (namespaced) or "<name>" (cluster-scoped).
var kindPaths = map[string]struct {
	prefix     string
	namespaced bool
}{
	"Pod":                   {"pods", true},
	"Deployment":            {"deployments", true},
	"StatefulSet":           {"statefulsets", true},
	"DaemonSet":             {"daemonsets", true},
	"Job":                   {"jobs", true},
	"CronJob":               {"cronjobs", true},
	"Node":                  {"nodes", false},
	"Namespace":             {"namespaces", false},
	"PersistentVolumeClaim": {"storage/persistentvolumeclaims", true},
	"Kustomization":         {"customresources/kustomizations.kustomize.toolkit.fluxcd.io", true},
	"HelmRelease":           {"customresources/helmreleases.helm.toolkit.fluxcd.io", true},
	"GitRepository":         {"customresources/gitrepositories.source.toolkit.fluxcd.io", true},
	"OCIRepository":         {"customresources/ocirepositories.source.toolkit.fluxcd.io", true},
	"HelmRepository":        {"customresources/helmrepositories.source.toolkit.fluxcd.io", true},
	"Cluster":               {"customresources/clusters.postgresql.cnpg.io", true},
	"Certificate":           {"customresources/certificates.cert-manager.io", true},
}

// target is where Start pointed the plugin: Headlamp's base URL and the cluster's name in it.
type target struct{ base, cluster string }

// Plugin is the headlamp plugin. It is a plugin.Configured and a plugin.LinkProvider.
type Plugin struct {
	// t is nil until Start and again after Stop. Links is called while the supervisor resolves
	// links, so it only reads.
	t atomic.Pointer[target]
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
func conf(cfg any) (pluginconf.Headlamp, bool) {
	switch c := cfg.(type) {
	case *pluginconf.Headlamp:
		if c != nil {
			return *c, true
		}
	case pluginconf.Headlamp:
		return c, true
	}
	return pluginconf.Headlamp{}, false
}

// Configured is true with a URL: without one the plugin is disabled, or, with enabled: true,
// unconfigured, and contributes no link.
func (*Plugin) Configured(cfg any) bool {
	c, ok := conf(cfg)
	return ok && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (*Plugin) ConfigHint() string { return "plugins.headlamp.url" }

func (p *Plugin) Start(_ context.Context, _ plugin.Host, cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("headlamp: configuration is %T, want *pluginconf.Headlamp", cfg)
	}
	p.set(c.URL, c.Cluster)
	return nil
}

func (p *Plugin) set(base, cluster string) {
	p.t.Store(&target{base: strings.TrimRight(base, "/"), cluster: cluster})
}

func (p *Plugin) Stop(context.Context) error {
	p.t.Store(nil)
	return nil
}

// Status is healthy once Start has run: the plugin has no upstream to fail.
func (p *Plugin) Status() plugin.Status {
	if p.t.Load() == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy}
}

// Links answers a Kubernetes kind with the object's "Open in Headlamp" link; any other kind gets
// nothing. A plugin that is not running contributes nothing.
func (p *Plugin) Links(req plugin.LinkRequest) []model.Link {
	t := p.t.Load()
	if t == nil || t.base == "" {
		return []model.Link{}
	}
	k, ok := kindPaths[req.Kind]
	if !ok {
		return []model.Link{}
	}
	href := t.base + "/c/" + url.PathEscape(t.cluster) + "/" + k.prefix + "/"
	if k.namespaced {
		href += url.PathEscape(req.Namespace) + "/"
	}
	href += url.PathEscape(req.Name)
	return []model.Link{{Label: "Open in Headlamp", Href: href, Kind: model.LinkKindHeadlamp}}
}
