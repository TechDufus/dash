package topics

import (
	"sort"

	"github.com/TechDufus/dash/internal/model"
)

// sortAttention orders items critical first, then newest first, then by ID.
func sortAttention(items []model.AttentionItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Severity != items[j].Severity {
			return items[i].Severity == model.SeverityCrit
		}
		if !items[i].Since.Equal(items[j].Since) {
			return items[i].Since.After(items[j].Since)
		}
		return items[i].ID < items[j].ID
	})
}

// OverallStatus is the worst severity of the items.
func OverallStatus(items []model.AttentionItem) model.Health {
	s := model.HealthOK
	for _, it := range items {
		if it.Severity == model.SeverityCrit {
			return model.HealthCrit
		}
		if it.Severity == model.SeverityWarn {
			s = model.HealthWarn
		}
	}
	return s
}
