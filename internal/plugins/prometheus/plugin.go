// Package prometheus is the prometheus plugin (design F.1, P3.1): a Prometheus-compatible
// metrics backend (Prometheus, VictoriaMetrics) that other plugins and the core query. It
// publishes no topic and has no actions; what it offers is the svc.Querier service, and the
// series its consumers register with the core read it through that.
//
// It imports pluginconf, never internal/config.
package prometheus

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

// ID is the plugin's id: plugins.prometheus, DASH_PLUGINS_PROMETHEUS_*, Host.Service("prometheus").
const ID plugin.ID = "prometheus"

// defaultTimeout is the query timeout of a section that does not set one (the tree's default is
// the same: pluginconf.DefaultPrometheus).
const defaultTimeout = 5 * time.Second

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Prometheus-compatible",
		Description: "VictoriaMetrics or Prometheus: the series behind charts, KPIs and host metrics.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.Prometheus{} },
	}
}

// Plugin is the real prometheus plugin. It is also a plugin.Provider (Service is the Querier),
// a plugin.Configured and plugin.Validator, and a plugin.Demo.
type Plugin struct {
	mu   sync.RWMutex
	prom *upstream.Prom // nil until Start has built it, and again after Stop
}

var (
	_ plugin.Plugin     = (*Plugin)(nil)
	_ plugin.Configured = (*Plugin)(nil)
	_ plugin.Validator  = (*Plugin)(nil)
	_ plugin.Provider   = (*Plugin)(nil)
	_ plugin.Demo       = (*Plugin)(nil)
)

// New is the plugin.Factory of the real plugin.
func New() plugin.Plugin { return &Plugin{} }

func (*Plugin) Manifest() plugin.Manifest { return manifest() }

// conf reads the section the supervisor hands over: a pointer to the copy of the struct
// Manifest.Config returns. A section by value is accepted too.
func conf(cfg any) (pluginconf.Prometheus, bool) {
	switch c := cfg.(type) {
	case *pluginconf.Prometheus:
		if c != nil {
			return *c, true
		}
	case pluginconf.Prometheus:
		return c, true
	}
	return pluginconf.Prometheus{}, false
}

// Configured is true with a URL: unset the plugin is disabled, and with enabled: true it is
// unconfigured.
func (*Plugin) Configured(cfg any) bool {
	c, ok := conf(cfg)
	return ok && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (*Plugin) ConfigHint() string { return "plugins.prometheus.url" }

// Validate reports the cross-field rules of the section (auth, header names). The schema has
// already accepted its shape.
func (*Plugin) Validate(cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("prometheus: configuration is %T, want *pluginconf.Prometheus", cfg)
	}
	return c.Validate()
}

// Start builds the client. Nothing polls: a failing query reports on its consumer's source
// status, so the plugin is healthy once it is constructed.
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	c, ok := conf(cfg)
	if !ok {
		return fmt.Errorf("prometheus: configuration is %T, want *pluginconf.Prometheus", cfg)
	}
	timeout := time.Duration(c.Timeout)
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	hc, err := h.HTTPClient(plugin.HTTPConfig{
		Timeout:            timeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorizer(c.Auth, c.Headers),
	})
	if err != nil {
		return err
	}
	// NewProm takes the timeout and the redirect policy as options: it only receives the
	// transport, so it would otherwise lose both (design D7).
	prom := upstream.NewProm(c.URL, hc.Transport, upstream.Timeout(timeout), upstream.CheckRedirect(hc.CheckRedirect))
	p.mu.Lock()
	p.prom = prom
	p.mu.Unlock()
	return nil
}

// authorizer sets the configured headers and the credentials on every request. The secrets are
// read per request, so a rotated file applies at the next query. Basic and bearer are exclusive
// (Validate), and the headers cannot name Authorization (Validate), so nothing here overrides
// the other.
func authorizer(a pluginconf.Auth, headers map[string]string) func(*http.Request) {
	headers = maps.Clone(headers)
	return func(r *http.Request) {
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		switch {
		case a.BearerToken.IsSet():
			r.Header.Set("Authorization", "Bearer "+a.BearerToken.Reveal())
		case a.Basic.Username != "":
			r.SetBasicAuth(a.Basic.Username, a.Basic.Password.Reveal())
		}
	}
}

func (p *Plugin) Stop(context.Context) error {
	p.mu.Lock()
	p.prom = nil
	p.mu.Unlock()
	return nil
}

func (p *Plugin) Status() plugin.Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.prom == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy}
}

// Service is the svc.Querier of the running instance, nil before Start.
func (p *Plugin) Service() any {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.prom == nil {
		return nil
	}
	return svc.Querier(p.prom)
}
