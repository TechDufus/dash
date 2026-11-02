// Package gatus is the Gatus plugin (design F.0, P3.4): it polls Gatus for the health of the
// checked endpoints, publishes them as the gatus.status topic, raises attention items for the
// failing ones and exports svc.EndpointHealth to the code that decorates its own objects with it.
package gatus

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/svc"
	"github.com/TechDufus/dash/internal/upstream"
)

const (
	pluginID plugin.ID = "gatus"

	// pollEvery is how often Gatus is asked for its statuses.
	pollEvery = 30 * time.Second
	// requestTimeout is the whole-request timeout of one poll.
	requestTimeout = 5 * time.Second
)

// Plugin is the Gatus integration. The supervisor makes a fresh one for every start of the
// instance.
type Plugin struct {
	interval time.Duration // pollEvery; tests shorten it

	mu     sync.Mutex
	state  *service
	cancel context.CancelFunc
	done   chan struct{}
}

// New is the plugin's factory.
func New() plugin.Plugin { return &Plugin{interval: pollEvery} }

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID: pluginID, Name: "Gatus", Description: "Endpoint health.", Version: "1.0.0",
		Config: func() any { return &pluginconf.Gatus{} },
		Topics: []plugin.TopicSpec{{Name: "status", Type: model.StatusData{}, Doc: "Endpoint health checks."}},
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/status", Label: "Status", Title: "Service status", Icon: "Activity"},
		}},
	}
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// settings is the plugin's section as the supervisor hands it over: a pointer, or a value.
func settings(cfg any) (pluginconf.Gatus, error) {
	switch c := cfg.(type) {
	case *pluginconf.Gatus:
		if c != nil {
			return *c, nil
		}
	case pluginconf.Gatus:
		return c, nil
	}
	return pluginconf.Gatus{}, fmt.Errorf("gatus: unexpected configuration type %T", cfg)
}

// Configured is false without a URL: the plugin is then disabled, or unconfigured when it was
// switched on.
func (p *Plugin) Configured(cfg any) bool {
	c, err := settings(cfg)
	return err == nil && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.gatus.url" }

// Validate checks the auth rules and the page size.
func (p *Plugin) Validate(cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	return c.Validate()
}

// authorize sets the credentials of one request, reading the secrets as it goes so a rotated
// secret applies to the next poll, and a value that arrives after Start (a k8s: reference read
// by the kubernetes plugin) is sent from then on.
func authorize(a pluginconf.Auth) func(*http.Request) {
	return func(r *http.Request) {
		if tok := a.BearerToken.Reveal(); tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		} else if a.Basic.Username != "" {
			r.SetBasicAuth(a.Basic.Username, a.Basic.Password.Reveal())
		}
	}
}

// Start builds the client and launches the poller; it does not wait for the first poll.
func (p *Plugin) Start(ctx context.Context, h plugin.Host, cfg any) error {
	c, err := settings(cfg)
	if err != nil {
		return err
	}
	hc, err := h.HTTPClient(plugin.HTTPConfig{
		Timeout: requestTimeout, CAFile: c.TLS.CAFile, InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize: authorize(c.Auth),
	})
	if err != nil {
		return err
	}
	client := upstream.NewGatus(c.URL, hc.Transport, c.PageSize,
		upstream.Timeout(hc.Timeout), upstream.CheckRedirect(hc.CheckRedirect))
	s := newService(append([]string(nil), c.CriticalGroups...))

	pctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.mu.Lock()
	p.state, p.cancel, p.done = s, cancel, done
	p.mu.Unlock()

	h.Go("gatus poll", func() {
		defer close(done)
		p.run(pctx, h, client, s)
	})
	return nil
}

// run polls now and then every interval until ctx ends.
func (p *Plugin) run(ctx context.Context, h plugin.Host, client *upstream.Gatus, s *service) {
	var lastCategory string
	poll := func() {
		eps, err := client.Statuses(ctx)
		if ctx.Err() != nil {
			return // stopping: whatever came back is not an outcome
		}
		// Viewers learn the category only; the detail names URLs and hosts, so it goes to the
		// log, scrubbed, once per change of category rather than on every poll.
		if cat := upstream.Category(err); err != nil && cat != lastCategory {
			h.Logger().Warn("upstream source failed", "source", sourceName, "category", cat, "error", h.ScrubLog(err.Error()))
		}
		lastCategory = upstream.Category(err)
		data, srcs := s.record(eps, err, h.Now())
		h.Publish("status", data, srcs)
	}
	poll()
	every := p.interval
	if every <= 0 {
		every = pollEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			poll()
		}
	}
}

// Stop ends the poller and waits for it, as long as ctx allows.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.Lock()
	cancel, done := p.cancel, p.done
	p.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Plugin) service() *service {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

// Status is starting until the first poll has finished, then healthy, or error with the category
// of the failure.
func (p *Plugin) Status() plugin.Status {
	s := p.service()
	if s == nil {
		return plugin.Status{State: plugin.StateStarting}
	}
	return s.status()
}

// Service exports svc.EndpointHealth.
func (p *Plugin) Service() any {
	if s := p.service(); s != nil {
		return svc.EndpointHealth(s)
	}
	return nil
}

// Attention implements plugin.AttentionSource.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	s := p.service()
	if s == nil {
		return nil
	}
	return attention(s, now)
}

// Demo implements plugin.Demo.
func (p *Plugin) Demo() plugin.Plugin { return newTwin() }

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.Provider        = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
