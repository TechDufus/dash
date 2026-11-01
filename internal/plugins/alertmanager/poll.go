package alertmanager

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/upstream"
)

// dashCreator prefixes the createdBy of every silence dash creates. It is how dash recognises its
// own silences: only those can be expired through it (design F.3).
const dashCreator = "dash:"

// run polls until ctx ends: once at once, then every p.every.
func (p *Plugin) run(ctx context.Context) {
	p.poll(ctx)
	t := time.NewTicker(p.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx)
		}
	}
}

// poll reads the alerts and then the silences, and publishes what it has: after a failure the
// last good lists go out again with the failing source, as the Manager did.
func (p *Plugin) poll(ctx context.Context) {
	p.mu.RLock()
	am, h, c := p.am, p.host, p.cfg
	p.mu.RUnlock()

	alerts, err := am.Alerts(ctx)
	var silences []model.Silence
	if err == nil {
		silences, err = am.Silences(ctx)
	}
	if ctx.Err() != nil {
		return // stopping: the instance's topics are about to be removed
	}

	p.mu.Lock()
	prev := p.snap
	p.snap.polled, p.snap.at, p.snap.err = true, h.Now(), upstream.Category(err)
	if err == nil {
		p.snap.alerts, p.snap.silences = decorate(&c, alerts, silences)
	}
	cur := p.snap
	p.mu.Unlock()

	if cur.err != prev.err || !prev.polled {
		// The detail (URL, host, upstream body) goes to the log only; viewers get the category.
		if err != nil {
			h.Logger().Warn("alertmanager poll failed", "category", cur.err, "error", err)
		} else if prev.err != "" {
			h.Logger().Info("alertmanager poll recovered")
		}
	}
	h.Publish(topicAlerts, payload(&c, cur), []model.SourceStatus{{
		Name: sourceName, OK: cur.err == "", Error: cur.err, CheckedAt: cur.at,
	}})
}

// decorate adds the fields the plugin owns: refs, Alert.Protected and Silence.Dash. It returns
// new slices and leaves its inputs alone.
func decorate(c *pluginconf.Alertmanager, alerts []model.Alert, silences []model.Silence) ([]model.Alert, []model.Silence) {
	as := make([]model.Alert, len(alerts))
	for i, a := range alerts {
		a.Ref = plugin.Ref(ID, "Alert", a.Fingerprint)
		a.Protected = slices.Contains(c.ProtectedAlerts, a.Name)
		as[i] = a
	}
	ss := make([]model.Silence, len(silences))
	for i, s := range silences {
		s.Ref = plugin.Ref(ID, "Silence", s.ID)
		s.Dash = strings.HasPrefix(s.CreatedBy, dashCreator)
		ss[i] = s
	}
	return as, ss
}

// payload is the topic's data: the lists sorted (alerts newest first, silences by expiry), never
// nil, with the durations the dialog offers.
func payload(c *pluginconf.Alertmanager, s snapshot) model.AlertsData {
	return buildData(c, s.alerts, s.silences)
}

func buildData(c *pluginconf.Alertmanager, alerts []model.Alert, silences []model.Silence) model.AlertsData {
	d := model.AlertsData{
		Alerts:           append([]model.Alert{}, alerts...),
		Silences:         append([]model.Silence{}, silences...),
		SilenceDurations: slices.Clone(c.SilenceDurations),
	}
	sort.SliceStable(d.Alerts, func(i, j int) bool { return d.Alerts[i].StartsAt.After(d.Alerts[j].StartsAt) })
	sort.SliceStable(d.Silences, func(i, j int) bool { return d.Silences[i].EndsAt.Before(d.Silences[j].EndsAt) })
	return d
}
