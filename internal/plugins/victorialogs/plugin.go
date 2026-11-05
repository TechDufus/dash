// Package victorialogs is the VictoriaLogs integration: the pod logs the UI shows, as one query
// route and one live tail (design F.5). It publishes no topic and offers no action.
//
// What is configurable is which record fields name the pod (plugins.victorialogs.fields), how
// many lines one query returns, and which namespaces nobody may read (denyNamespaces, which the
// core enforces and audits before a route's handler runs).
package victorialogs

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/upstream"
)

// ID is the plugin's id and, in v1, the name of its only instance.
const ID plugin.ID = "victorialogs"

// source is the part of the upstream client the routes use.
type source interface {
	Query(ctx context.Context, q upstream.LogQuery) ([]model.LogLine, error)
	Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error
}

// running is what a started instance serves with. Routes read it per request, so a request
// that races Stop or a reload answers 503 instead of touching a half-built client.
type running struct {
	src    source
	fields upstream.LogFields
	// scrub removes the secrets the core knows from a line's text (Host.ScrubLog). It runs on
	// every line's Msg before the payload is built, never on the encoded JSON, which escapes
	// the characters of some secrets.
	scrub func(string) string
	now   func() time.Time
}

// Plugin is the VictoriaLogs integration.
type Plugin struct {
	demo bool
	st   atomic.Pointer[running]
}

// New is the plugin's Factory.
func New() plugin.Plugin { return &Plugin{} }

// Manifest implements plugin.Plugin. The topic-less manifest is the same as the legacy shim's.
func (p *Plugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "VictoriaLogs",
		Description: "The pod logs the UI shows.",
		Version:     "1.0.0",
		Config:      func() any { return &pluginconf.VictoriaLogs{} },
	}
}

// Configured implements plugin.Configured: without a URL the plugin is off (or unconfigured
// when it is enabled explicitly), dash makes no request and the UI hides the log tab.
func (p *Plugin) Configured(cfg any) bool {
	if p.demo {
		return true
	}
	c, ok := cfg.(*pluginconf.VictoriaLogs)
	return ok && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.victorialogs.url" }

// Validate implements plugin.Validator: the field names are concatenated into LogsQL, so they
// are checked again here, with the limit and the auth rules.
func (p *Plugin) Validate(cfg any) error {
	if p.demo {
		return nil
	}
	c, ok := cfg.(*pluginconf.VictoriaLogs)
	if !ok {
		return fmt.Errorf("configuration is a %T, not a VictoriaLogs section", cfg)
	}
	return c.Validate()
}

// Start implements plugin.Plugin: it builds the client behind the host's guarded transport
// (TLS, CA, credentials per request, the dial guard and the upstream metrics).
func (p *Plugin) Start(_ context.Context, h plugin.Host, cfg any) error {
	if p.demo {
		return p.startDemo(h)
	}
	c, ok := cfg.(*pluginconf.VictoriaLogs)
	if !ok {
		return fmt.Errorf("configuration is a %T, not a VictoriaLogs section", cfg)
	}
	hc, err := h.HTTPClient(plugin.HTTPConfig{
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorize(c.Auth),
	})
	if err != nil {
		return err
	}
	fields := upstream.LogFields{Namespace: c.Fields.Namespace, Pod: c.Fields.Pod, Container: c.Fields.Container}
	// The transport alone would lose the client's redirect policy: pass it on, and the host's cut.
	src := upstream.NewVLogs(c.URL, hc.Transport, c.Limit, upstream.CheckRedirect(hc.CheckRedirect), upstream.Cut(h.CutLog)).WithFields(fields)
	p.st.Store(&running{src: src, fields: fields, scrub: h.ScrubLog, now: h.Now})
	return nil
}

// Stop implements plugin.Plugin. Requests in flight end with their own contexts.
func (p *Plugin) Stop(context.Context) error {
	p.st.Store(nil)
	return nil
}

// Status implements plugin.Plugin. The plugin polls nothing, so it is healthy while it runs; a
// failing query answers that request with a category (design C.6), not the whole plugin.
func (p *Plugin) Status() plugin.Status {
	if p.st.Load() == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return plugin.Status{State: plugin.StateHealthy}
}

// authorize sets the credentials of every request: HTTP Basic, or the bearer token. The secret
// is read per request, so a rotated value applies at the next one.
func authorize(a pluginconf.Auth) func(*http.Request) {
	if a.Basic.Username != "" {
		return func(r *http.Request) { r.SetBasicAuth(a.Basic.Username, a.Basic.Password.Reveal()) }
	}
	return func(r *http.Request) {
		if tok := a.BearerToken.Reveal(); tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
	}
}
