package alertmanager

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// kpiFiring is the key of the plugin's one KPI.
const kpiFiring = "firing"

// firing reports whether an alert counts: active, and neither silenced nor inhibited. The names
// plugins.alertmanager.ignoreAlerts drops never get this far: the upstream client removes them.
func firing(a model.Alert) bool {
	return a.State == "active" && len(a.SilencedBy) == 0 && len(a.InhibitedBy) == 0
}

// severityOf maps an alert's severity label through plugins.alertmanager.severityMap. An alert
// whose label maps to nothing (info, none, a label the operator did not list) is not attention.
func severityOf(severityMap map[string]string, label string) (model.Severity, bool) {
	switch severityMap[label] {
	case "crit":
		return model.SeverityCrit, true
	case "warn":
		return model.SeverityWarn, true
	}
	return "", false
}

// Attention raises an item for every firing alert whose severity maps to crit or warn.
func (p *Plugin) Attention(time.Time) []model.AttentionItem {
	p.mu.RLock()
	alerts, sev, h := p.snap.alerts, p.cfg.SeverityMap, p.host
	p.mu.RUnlock()
	return attentionOf(alerts, sev, func() []model.Link {
		return h.Links().Resolve(plugin.LinkRequest{Kind: "Alerts"})
	})
}

// attentionOf is Attention over a list. links is asked once, and only when there is an item.
func attentionOf(alerts []model.Alert, severityMap map[string]string, links func() []model.Link) []model.AttentionItem {
	items := []model.AttentionItem{}
	var ls []model.Link
	for _, a := range alerts {
		sev, ok := severityOf(severityMap, a.Severity)
		if !ok || !firing(a) {
			continue
		}
		if ls == nil {
			ls = links()
			if ls == nil {
				ls = []model.Link{}
			}
		}
		detail := a.Summary
		if detail == "" {
			detail = a.Description
		}
		items = append(items, model.AttentionItem{
			ID: "alert:" + a.Fingerprint, Severity: sev, Source: sourceName, Plugin: string(ID),
			Title: a.Name, Detail: detail, Since: a.StartsAt, Route: "/alerts", Links: ls,
			Ref: plugin.Ref(ID, "Alert", a.Fingerprint),
		})
	}
	return items
}

// KPIs is the number of firing alerts. Its health is crit when a firing alert maps to crit, warn
// when anything fires, else ok.
func (p *Plugin) KPIs() []model.KPI {
	p.mu.RLock()
	alerts, sev := p.snap.alerts, p.cfg.SeverityMap
	p.mu.RUnlock()
	return kpisOf(alerts, sev)
}

func kpisOf(alerts []model.Alert, severityMap map[string]string) []model.KPI {
	n, health := 0, model.HealthOK
	for _, a := range alerts {
		if !firing(a) {
			continue
		}
		n++
		if health == model.HealthOK {
			health = model.HealthWarn
		}
		if s, ok := severityOf(severityMap, a.Severity); ok && s == model.SeverityCrit {
			health = model.HealthCrit
		}
	}
	return []model.KPI{{Key: kpiFiring, Plugin: string(ID), Label: "Alerts firing", Value: float64(n), Health: health}}
}
