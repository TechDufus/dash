package alertmanager

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// Demo is the twin DASH_DEMO=true runs instead of the plugin (plugin.Demo).
func (p *Plugin) Demo() plugin.Plugin { return &twin{} }

// twin publishes the fake homelab's alerts and fakes the silence actions on them: nothing leaves
// the process. Creating a silence mutes the alerts it matches and expiring it unmutes them, so
// the Alerts page behaves as it would against a real Alertmanager.
type twin struct {
	mu       sync.Mutex
	h        plugin.Host
	cfg      pluginconf.Alertmanager
	alerts   []model.Alert
	silences []model.Silence
	next     int
}

// noLinks is the link source of a world whose alerts need none.
type noLinks struct{}

func (noLinks) Headlamp(string, string, string) []model.Link { return []model.Link{} }
func (noLinks) VMConsole(int, string) []model.Link           { return []model.Link{} }

func (t *twin) Manifest() plugin.Manifest { return manifest() }

func (t *twin) Start(_ context.Context, h plugin.Host, cfg any) error {
	c := pluginconf.DefaultAlertmanager()
	if in, ok := cfg.(*pluginconf.Alertmanager); ok && in != nil {
		c = *in
	}
	if len(c.SilenceDurations) == 0 {
		c.SilenceDurations = pluginconf.DefaultAlertmanager().SilenceDurations
	}
	seed := world.New(h.Now, h.Now(), noLinks{}).Alerts()
	t.mu.Lock()
	t.h, t.cfg = h, c
	t.alerts, t.silences = seed.Alerts, seed.Silences
	t.mu.Unlock()
	t.publish()
	return nil
}

func (t *twin) Stop(context.Context) error { return nil }

func (t *twin) Status() plugin.Status {
	return plugin.Status{
		State:   plugin.StateHealthy,
		Sources: []model.SourceStatus{{Name: sourceName, OK: true}},
	}
}

// publish sends the current lists with the fields the plugin owns. The lists change only
// through Act.
func (t *twin) publish() {
	t.mu.Lock()
	h := t.h
	alerts, silences := decorate(&t.cfg, t.alerts, t.silences)
	d := buildData(&t.cfg, alerts, silences)
	t.mu.Unlock()
	h.Publish(topicAlerts, d, []model.SourceStatus{{Name: sourceName, OK: true, CheckedAt: h.Now()}})
}

// Act creates or expires a silence in memory, under the rules of the real plugin.
func (t *twin) Act(_ context.Context, action string, pr auth.Principal, input any) (any, error) {
	t.mu.Lock()
	var out any
	var err error
	switch in := input.(type) {
	case *silenceCreateInput:
		out, err = t.create(pr, in)
	case *silenceExpireInput:
		out, err = t.expire(in)
	default:
		err = refuse(http.StatusNotFound, model.ErrCodeNotFound, "no such action: %s", action)
	}
	t.mu.Unlock()
	if err == nil {
		t.publish()
	}
	return out, err
}

func (t *twin) create(pr auth.Principal, in *silenceCreateInput) (any, error) {
	dur, comment, refusal := planSilence(&t.cfg, in)
	if refusal != nil {
		return nil, refusal
	}
	t.next++
	// An id shaped like Alertmanager's, so the expire action's own check accepts it.
	id := fmt.Sprintf("00000000-0000-4000-8000-%012x", t.next)
	now := t.h.Now().UTC()
	s := model.Silence{
		ID: id, CreatedBy: dashCreator + pr.User, Comment: comment,
		StartsAt: now, EndsAt: now.Add(dur), State: "active",
		Matchers: make([]model.Matcher, 0, len(in.Matchers)),
	}
	for _, m := range in.Matchers {
		s.Matchers = append(s.Matchers, model.Matcher{Name: m.Name, Value: m.Value, IsEqual: true})
	}
	t.silences = append(t.silences, s)
	t.resilence()
	return plugin.Outcome{
		Body:   model.SilenceResponse{SilenceID: id},
		Target: &plugin.ActionTarget{Kind: "Silence", Name: id},
	}, nil
}

func (t *twin) expire(in *silenceExpireInput) (any, error) {
	if refusal := checkExpire(t.silences, in.ID); refusal != nil {
		return nil, refusal
	}
	t.silences = slices.DeleteFunc(t.silences, func(s model.Silence) bool { return s.ID == in.ID })
	t.resilence()
	return nil, nil
}

// resilence recomputes which silences mute which alert, and the alert's state with it: an alert
// is suppressed while a silence (or an inhibitor) holds it.
func (t *twin) resilence() {
	for i, a := range t.alerts {
		a.SilencedBy = []string{}
		for _, s := range t.silences {
			if matches(s, a.Labels) {
				a.SilencedBy = append(a.SilencedBy, s.ID)
			}
		}
		a.State = "active"
		if len(a.SilencedBy) > 0 || len(a.InhibitedBy) > 0 {
			a.State = "suppressed"
		}
		t.alerts[i] = a
	}
}

// matches reports whether every matcher of s holds for labels. The twin's silences are exact.
func matches(s model.Silence, labels map[string]string) bool {
	for _, m := range s.Matchers {
		if labels[m.Name] != m.Value {
			return false
		}
	}
	return len(s.Matchers) > 0
}

// Attention and KPIs of the twin, over its in-memory lists, so the overview agrees with the page.
func (t *twin) Attention(time.Time) []model.AttentionItem {
	t.mu.Lock()
	alerts, sev, h := slices.Clone(t.alerts), t.cfg.SeverityMap, t.h
	t.mu.Unlock()
	return attentionOf(alerts, sev, func() []model.Link {
		return h.Links().Resolve(plugin.LinkRequest{Kind: "Alerts"})
	})
}

func (t *twin) KPIs() []model.KPI {
	t.mu.Lock()
	alerts, sev := slices.Clone(t.alerts), t.cfg.SeverityMap
	t.mu.Unlock()
	return kpisOf(alerts, sev)
}

var (
	_ plugin.Plugin          = (*twin)(nil)
	_ plugin.Actor           = (*twin)(nil)
	_ plugin.AttentionSource = (*twin)(nil)
	_ plugin.KPISource       = (*twin)(nil)
)
