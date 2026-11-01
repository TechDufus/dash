package gatus

import (
	"fmt"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// attention is one item per failing check, in Gatus' order (the overview sorts them): critical in
// a critical group, a warning elsewhere. A check with no results yet says nothing. Since is when
// the current streak of failures began.
func attention(s *service, now time.Time) []model.AttentionItem {
	eps, _, _ := s.snapshot()
	items := []model.AttentionItem{}
	for _, e := range eps {
		if len(e.Results) == 0 {
			continue
		}
		sev := EndpointHealth(e.Group, e.Healthy, s.critical)
		if sev != model.HealthWarn && sev != model.HealthCrit {
			continue
		}
		since := latestFailureStart(e)
		if since.IsZero() {
			since = now
		}
		items = append(items, model.AttentionItem{
			ID: "gatus:" + e.Key, Severity: model.Severity(sev), Source: sourceName, Plugin: string(pluginID),
			Title: fmt.Sprintf("%s check failing", e.Name), Detail: e.Group, Since: since,
			Route: "/status", Links: []model.Link{}, Ref: e.Ref,
		})
	}
	return items
}

// latestFailureStart is the timestamp of the first failure in the current failing streak.
func latestFailureStart(e model.Endpoint) time.Time {
	var t time.Time
	for i := len(e.Results) - 1; i >= 0 && !e.Results[i].Success; i-- {
		t = e.Results[i].Timestamp
	}
	return t
}
