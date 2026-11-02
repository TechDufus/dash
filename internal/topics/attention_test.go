package topics

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestOverallStatusAndOrdering(t *testing.T) {
	if got := OverallStatus(nil); got != model.HealthOK {
		t.Errorf("empty = %s", got)
	}
	old, recent := testNow.Add(-2*time.Hour), testNow.Add(-time.Minute)
	items := []model.AttentionItem{
		{ID: "old", Severity: model.SeverityWarn, Since: old},
		{ID: "new", Severity: model.SeverityWarn, Since: recent},
	}
	sortAttention(items)
	if OverallStatus(items) != model.HealthWarn {
		t.Errorf("only warnings must give warn, got %s", OverallStatus(items))
	}
	if items[0].ID != "new" {
		t.Errorf("newest first within a severity, got %s", items[0].ID)
	}
	items = append(items, model.AttentionItem{ID: "unverified", Severity: model.SeverityCrit, Since: old})
	sortAttention(items)
	if OverallStatus(items) != model.HealthCrit {
		t.Errorf("a critical item must make the status crit, got %s", OverallStatus(items))
	}
	if items[0].ID != "unverified" {
		t.Errorf("crit sorts first even when older, got %s", items[0].ID)
	}
	// The same time and severity: by id.
	tied := []model.AttentionItem{{ID: "b", Severity: model.SeverityWarn, Since: old}, {ID: "a", Severity: model.SeverityWarn, Since: old}}
	sortAttention(tied)
	if tied[0].ID != "a" {
		t.Errorf("ties sort by id, got %s", tied[0].ID)
	}
}
