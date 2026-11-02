package rustfs

import (
	"fmt"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// routeStorage is where every item of the plugin points, and sourceStorage the name the items
// have always had on the overview.
const (
	routeStorage  = "/storage"
	sourceStorage = "storage"
)

// Attention raises an item for every bucket with a quota that is at 80% of it (warning) or 90%
// (critical). A bucket has no time of its own, so the supervisor stamps each item with the moment
// it was first seen. It has no opinion (nil) before the first poll; once polled it is never nil,
// so that a bucket that is gone or back under its limit loses its first sighting.
func (p *Plugin) Attention(_ time.Time) []model.AttentionItem {
	p.mu.Lock()
	polled, snap := p.polled, p.snap
	p.mu.Unlock()
	if !polled {
		return nil
	}
	return attentionOf(bucketsOf(snap))
}

// attentionOf is the bucket rule over a payload's buckets. A bucket without a quota raises
// nothing. Items have a zero Since and an empty, non-nil link list.
func attentionOf(buckets []model.Bucket) []model.AttentionItem {
	items := []model.AttentionItem{}
	for _, b := range buckets {
		if b.QuotaBytes <= 0 {
			continue
		}
		r := float64(b.UsedBytes) / float64(b.QuotaBytes)
		sev := health.RatioHealth(r)
		if sev != model.HealthWarn && sev != model.HealthCrit {
			continue
		}
		items = append(items, model.AttentionItem{
			ID: "bucket:" + b.Name, Severity: model.Severity(sev), Source: sourceStorage, Plugin: string(ID),
			Title: fmt.Sprintf("Bucket %s is %.0f%% of its quota", b.Name, r*100),
			Route: routeStorage, Links: []model.Link{}, Ref: b.Ref,
		})
	}
	return items
}
