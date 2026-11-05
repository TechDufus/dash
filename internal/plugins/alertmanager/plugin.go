// Package alertmanager is the Alertmanager plugin (design F.3): the alerts and silences topic, the
// firing-alert contribution to the overview, and the two silence actions.
//
// It reads Alertmanager through internal/upstream and the settings through internal/pluginconf;
// like every plugin it imports neither internal/config nor internal/api.
package alertmanager

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/upstream"
)

// ID is the plugin's id and its instance name: plugins.alertmanager, topic alertmanager.alerts.
const ID plugin.ID = "alertmanager"

const (
	// topicAlerts is the manifest name of the topic; it is published as "alertmanager.alerts".
	topicAlerts = "alerts"
	// sourceName names the one upstream in the sources of the topic, the status and the overview.
	sourceName = "alertmanager"
	// pollEvery is how often Alertmanager is read.
	pollEvery = 15 * time.Second
	// requestTimeout caps one request to Alertmanager.
	requestTimeout = 5 * time.Second
)

func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          ID,
		Name:        "Alertmanager",
		Description: "Alerts and silences.",
		Version:     "1.0.0",
		Uses:        []plugin.ID{"grafana"},
		Config:      func() any { return &pluginconf.Alertmanager{} },
		Topics:      []plugin.TopicSpec{{Name: topicAlerts, Type: model.AlertsData{}, Doc: "Alerts and silences."}},
		Actions:     actionSpecs(),
		UI: plugin.UISpec{Nav: []model.NavItem{
			{Path: "/alerts", Label: "Alerts", Title: "Alerts and silences", Icon: "BellRing"},
		}},
	}
}

// New returns the plugin; it is the factory registered in the registry.
func New() plugin.Plugin { return newPlugin(pollEvery) }

func newPlugin(every time.Duration) *Plugin { return &Plugin{every: every} }

// Plugin reads Alertmanager every pollEvery and publishes its alerts and silences.
type Plugin struct {
	every time.Duration

	mu   sync.RWMutex
	host plugin.Host
	cfg  pluginconf.Alertmanager
	am   *upstream.Alertmanager
	snap snapshot
	done chan struct{} // closed when the poll loop has returned
}

// snapshot is what the last poll learned. alerts and silences already carry refs and flags.
type snapshot struct {
	alerts   []model.Alert
	silences []model.Silence
	polled   bool      // a poll has ended, whether or not it succeeded
	err      string    // the error category of the last poll; "" when it succeeded
	at       time.Time // when the last poll ended
}

func (p *Plugin) Manifest() plugin.Manifest { return manifest() }

// Configured: with no URL the plugin is "disabled", and "unconfigured" when it was enabled.
func (p *Plugin) Configured(cfg any) bool {
	c, ok := cfg.(*pluginconf.Alertmanager)
	return ok && c.URL != ""
}

// ConfigHint implements plugin.ConfigHinter.
func (p *Plugin) ConfigHint() string { return "plugins.alertmanager.url" }

// Validate applies the section's own rules, so that a configuration the supervisor gets by
// another way than the config loader is refused for the same reasons.
func (p *Plugin) Validate(cfg any) error {
	c, ok := cfg.(*pluginconf.Alertmanager)
	if !ok {
		return fmt.Errorf("configuration is %T, want *pluginconf.Alertmanager", cfg)
	}
	return c.Validate()
}

func (p *Plugin) Start(ctx context.Context, h plugin.Host, cfg any) error {
	c, ok := cfg.(*pluginconf.Alertmanager)
	if !ok {
		return fmt.Errorf("configuration is %T, want *pluginconf.Alertmanager", cfg)
	}
	hc, err := h.HTTPClient(plugin.HTTPConfig{
		Timeout:            requestTimeout,
		CAFile:             c.TLS.CAFile,
		InsecureSkipVerify: c.TLS.InsecureSkipVerify,
		Authorize:          authorizer(c.Auth),
	})
	if err != nil {
		return err
	}
	// The transport carries TLS, the credentials and the dial guard; the timeout and the redirect
	// policy of hc ride as options, because a client built from the transport alone loses them.
	am := upstream.NewAlertmanager(c.URL, hc.Transport, c.IgnoreAlerts,
		upstream.Timeout(hc.Timeout), upstream.CheckRedirect(hc.CheckRedirect), upstream.Cut(h.CutLog))

	done := make(chan struct{})
	p.mu.Lock()
	p.host, p.cfg, p.am, p.done = h, *c, am, done
	p.mu.Unlock()

	h.Go("poll", func() {
		defer close(done)
		p.run(ctx)
	})
	return nil
}

// Stop waits for the poll loop, which ends with the context Start received.
func (p *Plugin) Stop(ctx context.Context) error {
	p.mu.RLock()
	done := p.done
	p.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// Status: starting until the first poll has ended, then healthy or error. Alertmanager is the
// only source, so the plugin is never degraded.
func (p *Plugin) Status() plugin.Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.snap.polled {
		return plugin.Status{State: plugin.StateStarting, Message: "waiting for the first poll"}
	}
	src := model.SourceStatus{Name: sourceName, OK: p.snap.err == "", Error: p.snap.err, CheckedAt: p.snap.at}
	if p.snap.err != "" {
		return plugin.Status{State: plugin.StateError, Message: p.snap.err, Sources: []model.SourceStatus{src}}
	}
	return plugin.Status{State: plugin.StateHealthy, Sources: []model.SourceStatus{src}}
}

// authorizer sets the credentials on every request, so a rotated secret applies at once. The
// bearer token is looked at per request too: a value that arrives after Start (a k8s: reference
// read by the kubernetes plugin) is sent from the next request on.
func authorizer(a pluginconf.Auth) func(*http.Request) {
	return func(r *http.Request) {
		switch {
		case a.Basic.Username != "":
			r.SetBasicAuth(a.Basic.Username, a.Basic.Password.Reveal())
		default:
			if tok := a.BearerToken.Reveal(); tok != "" {
				r.Header.Set("Authorization", "Bearer "+tok)
			}
		}
	}
}

// runtime is what an action needs, read once.
func (p *Plugin) runtime() (cfg pluginconf.Alertmanager, am *upstream.Alertmanager, now time.Time) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg, p.am, p.host.Now()
}

var (
	_ plugin.Plugin          = (*Plugin)(nil)
	_ plugin.Actor           = (*Plugin)(nil)
	_ plugin.Configured      = (*Plugin)(nil)
	_ plugin.Validator       = (*Plugin)(nil)
	_ plugin.AttentionSource = (*Plugin)(nil)
	_ plugin.KPISource       = (*Plugin)(nil)
	_ plugin.Demo            = (*Plugin)(nil)
)
